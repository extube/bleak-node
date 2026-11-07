// Package db provides the SQLite persistence layer for bleak-node.
package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver ("sqlite")
)

// Well-known sentinel errors returned by the persistence layer.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid")
	// ErrBalance signals the sender has no positive spendable balance.
	ErrBalance = errors.New("insufficient balance")
)

// DB wraps *sql.DB with bleak-node specific helpers.
type DB struct {
	*sql.DB
}

// Open opens (creating when missing) the SQLite database at path and applies migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", path)
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite is a single-writer store; a small pool is plenty and avoids lock contention.
	sqldb.SetMaxOpenConns(1)

	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	d := &DB{sqldb}
	if err := d.migrate(ctx); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

// migrate creates the schema if it does not exist yet.
func (d *DB) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS rooms (
	address     TEXT PRIMARY KEY,
	name        TEXT NOT NULL CHECK (length(name) <= 32),
	description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 120),
	max_user    INTEGER NOT NULL DEFAULT 100 CHECK (max_user > 0),
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL,
	deleted_at  TEXT
);

CREATE TABLE IF NOT EXISTS users (
	address     TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	room        TEXT NOT NULL REFERENCES rooms(address),
	public_bio  TEXT NOT NULL DEFAULT '' CHECK (length(public_bio) <= 64),
	private_bio TEXT NOT NULL DEFAULT '' CHECK (length(private_bio) <= 64),
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL,
	deleted_at  TEXT
);

CREATE INDEX IF NOT EXISTS idx_users_room ON users(room);

-- from_address may be a room (system "welcome" handshake), so foreign keys
-- are intentionally not declared here; the application validates addresses.
CREATE TABLE IF NOT EXISTS handshakes (
	tx_id        TEXT PRIMARY KEY,
	from_address TEXT NOT NULL,
	to_address   TEXT NOT NULL,
	status       INTEGER NOT NULL CHECK (status IN (1, 2, 3, 4)),
	value        INTEGER NOT NULL CHECK (value > 0),
	created_at   TEXT NOT NULL
);

-- Chats are created automatically once both handshake directions exist.
CREATE TABLE IF NOT EXISTS chats (
	address    TEXT PRIMARY KEY,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS participants (
	chat TEXT NOT NULL REFERENCES chats(address),
	user TEXT NOT NULL REFERENCES users(address),
	PRIMARY KEY (chat, user)
);

CREATE INDEX IF NOT EXISTS idx_participants_user ON participants(user);

CREATE TABLE IF NOT EXISTS messages (
	tx_id      TEXT PRIMARY KEY,
	"from"     TEXT NOT NULL REFERENCES users(address),
	"to"       TEXT NOT NULL REFERENCES users(address),
	created_at TEXT NOT NULL,
	content    TEXT NOT NULL CHECK (length(content) <= 255),
	chat       TEXT NOT NULL REFERENCES chats(address)
);

CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat);

-- Lightweight queue over SQLite. The agent worker consumes pending rows.
CREATE TABLE IF NOT EXISTS events (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	type         TEXT NOT NULL,
	chat         TEXT NOT NULL REFERENCES chats(address),
	speaker      TEXT NOT NULL,
	listener     TEXT NOT NULL,
	status       TEXT NOT NULL DEFAULT 'pending',
	attempts     INTEGER NOT NULL DEFAULT 0,
	error        TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	processed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_events_status ON events(status, id);
`
	if _, err := d.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// In-place migrations for older databases.
	if err := d.ensureColumn(ctx, "rooms", "max_user", "INTEGER NOT NULL DEFAULT 100"); err != nil {
		return err
	}
	if err := d.ensureColumn(ctx, "handshakes", "created_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := d.ensureColumn(ctx, "handshakes", "value", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	// Older handshakes tables had FOREIGN KEYS to users; rooms now act as
	// senders of the welcome handshake, so the table is rebuilt without them.
	return d.rebuildHandshakesWithoutFK(ctx)
}

// rebuildHandshakesWithoutFK recreates the handshakes table without foreign
// keys when any are present (SQLite cannot drop a constraint in place).
func (d *DB) rebuildHandshakesWithoutFK(ctx context.Context) error {
	var fkCount int
	rows, err := d.QueryContext(ctx, "PRAGMA foreign_key_list(handshakes)")
	if err != nil {
		return fmt.Errorf("foreign_key_list: %w", err)
	}
	for rows.Next() {
		fkCount++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()
	if fkCount == 0 {
		return nil
	}

	const rebuild = `
ALTER TABLE handshakes RENAME TO handshakes_old;

CREATE TABLE handshakes (
	tx_id        TEXT PRIMARY KEY,
	from_address TEXT NOT NULL,
	to_address   TEXT NOT NULL,
	status       INTEGER NOT NULL CHECK (status IN (1, 2, 3, 4)),
	value        INTEGER NOT NULL CHECK (value > 0),
	created_at   TEXT NOT NULL
);

-- Legacy rows may have status 0 or empty created_at; normalize them on the way over.
INSERT INTO handshakes (tx_id, from_address, to_address, status, value, created_at)
SELECT tx_id, from_address, to_address,
       CASE WHEN status = 0 THEN 1 ELSE status END,
       COALESCE(value, 1),
       CASE WHEN created_at IS NULL OR created_at = '' THEN '' ELSE created_at END
FROM handshakes_old;

DROP TABLE handshakes_old;
`
	if _, err := d.ExecContext(ctx, rebuild); err != nil {
		return fmt.Errorf("rebuild handshakes: %w", err)
	}
	// The empty created_at rows from very old schemas get a sensible stamp.
	if _, err := d.ExecContext(ctx,
		`UPDATE handshakes
		 SET created_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE created_at = ''`); err != nil {
		return fmt.Errorf("stamp legacy handshakes: %w", err)
	}
	return nil
}

// ensureColumn adds a column to a table when it is not present yet.
func (d *DB) ensureColumn(ctx context.Context, table, column, decl string) error {
	rows, err := d.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("table_info %s: %w", table, err)
	}
	defer rows.Close()

	var found bool
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found {
		return nil
	}
	// #nosec G201 -- table/column names are compile-time constants, not user input.
	if _, err := d.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

// isUnique reports whether err is a SQLite unique-constraint violation.
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// newAddress generates a unique random address of the form "0x" + hex(nBytes).
// The caller is expected to retry on ErrConflict in the (astronomically
// unlikely) event of a collision.
func newAddress(nBytes int) string {
	buf := make([]byte, nBytes)
	_, _ = rand.Read(buf)
	return "0x" + hex.EncodeToString(buf)
}

// newTxID generates a tx id of the form "0x" + 64 hex chars (256 bits).
func newTxID() string {
	return newAddress(32)
}

// parseTS parses an RFC3339 timestamp string; zero time when empty/invalid.
func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// parseNullTS parses an optional RFC3339 timestamp; nil when empty/invalid.
func parseNullTS(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

// exists checks whether exactly one row matches the query args.
func (d *DB) exists(ctx context.Context, query string, args ...any) (bool, error) {
	var one bool
	if err := d.QueryRowContext(ctx, query, args...).Scan(&one); err != nil {
		return false, err
	}
	return one, nil
}
