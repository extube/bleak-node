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

CREATE TABLE IF NOT EXISTS handshakes (
	tx_id        TEXT PRIMARY KEY,
	from_address TEXT NOT NULL REFERENCES users(address),
	to_address   TEXT NOT NULL REFERENCES users(address),
	status       INTEGER NOT NULL CHECK (status IN (0, 1, 2, 3, 4)),
	created_at   TEXT NOT NULL
);
`
	if _, err := d.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Migration for databases created before created_at existed on handshakes.
	return d.ensureColumn(ctx, "handshakes", "created_at", "TEXT NOT NULL DEFAULT ''")
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
