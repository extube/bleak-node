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
	public_bio  TEXT NOT NULL DEFAULT '' CHECK (length(public_bio) <= 255),
	private_bio TEXT NOT NULL DEFAULT '' CHECK (length(private_bio) <= 255),
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
	room         TEXT NOT NULL DEFAULT '',
	status       INTEGER NOT NULL CHECK (status IN (1, 2, 3, 4)),
	value        INTEGER NOT NULL CHECK (value > 0),
	created_at   TEXT NOT NULL
);

-- Chats are created automatically once both handshake directions exist.
-- They always live in the same room as their participants.
CREATE TABLE IF NOT EXISTS chats (
	address          TEXT PRIMARY KEY,
	room             TEXT NOT NULL DEFAULT '',
	scenario         TEXT NOT NULL DEFAULT '',
	language         TEXT NOT NULL DEFAULT 'en',
	phase            TEXT NOT NULL DEFAULT '',
	phase_started_at TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL
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
	// Room-scoping: users interact only inside one room, so handshakes and
	// chats are tied to it. Older rows get the room backfilled from users.
	if err := d.ensureColumn(ctx, "handshakes", "room", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := d.ensureColumn(ctx, "chats", "room", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Chat themes: chats carry the scenario key resolved by the agent.
	if err := d.ensureColumn(ctx, "chats", "scenario", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := d.ensureColumn(ctx, "chats", "language", "TEXT NOT NULL DEFAULT 'en'"); err != nil {
		return err
	}
	if err := d.ensureColumn(ctx, "users", "greeting", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Two-part conversations: the mandatory networking phase precedes the
	// themed debate.
	if err := d.ensureColumn(ctx, "chats", "phase", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := d.ensureColumn(ctx, "chats", "phase_started_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := d.ExecContext(ctx, `
UPDATE handshakes
SET room = COALESCE(
	(SELECT u.room FROM users u WHERE u.address = handshakes.to_address),
	(SELECT u.room FROM users u WHERE u.address = handshakes.from_address), '')
WHERE room = ''`); err != nil {
		return fmt.Errorf("backfill handshake rooms: %w", err)
	}
	if _, err := d.ExecContext(ctx, `
UPDATE chats
SET room = COALESCE(
	(SELECT u.room FROM users u WHERE u.address = (
		SELECT p.user FROM participants p WHERE p.chat = chats.address LIMIT 1)), '')
WHERE room = ''`); err != nil {
		return fmt.Errorf("backfill chat rooms: %w", err)
	}
	// Older handshakes tables had FOREIGN KEYS to users; rooms now act as
	// senders of the welcome handshake, so the table is rebuilt without them.
	if err := d.rebuildHandshakesWithoutFK(ctx); err != nil {
		return err
	}
	// Richer agent personas: bio limit was raised from 64 to 255 symbols.
	if err := d.rebuildUsersWithLongBio(ctx); err != nil {
		return err
	}
	// Message content cap: rebuild legacy messages tables that deviate from
	// the 255-symbol schema (e.g. the short-lived 511 experiment) and trim
	// rows that no longer fit.
	return d.rebuildMessagesWithCap(ctx)
}

// rebuildMessagesWithCap recreates the messages table when its content
// CHECK constraint deviates from the 255-symbol cap (SQLite cannot alter a
// constraint in place). Every content is trimmed to 255 symbols with SQLite
// substr, character-based, so the copy stays valid.
func (d *DB) rebuildMessagesWithCap(ctx context.Context) error {
	var ddl sql.NullString
	err := d.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'messages'`).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		// Fresh database: the base schema above already creates it with the cap.
		return nil
	}
	if err != nil {
		return fmt.Errorf("sqlite_master messages: %w", err)
	}
	if !ddl.Valid || strings.Contains(ddl.String, "length(content) <= 255") {
		// Already at the 255-symbol schema.
		return nil
	}

	const rebuild = `
ALTER TABLE messages RENAME TO messages_old;

CREATE TABLE messages (
	tx_id      TEXT PRIMARY KEY,
	"from"     TEXT NOT NULL REFERENCES users(address),
	"to"       TEXT NOT NULL REFERENCES users(address),
	created_at TEXT NOT NULL,
	content    TEXT NOT NULL CHECK (length(content) <= 255),
	chat       TEXT NOT NULL REFERENCES chats(address)
);

INSERT INTO messages (tx_id, "from", "to", created_at, content, chat)
SELECT tx_id, "from", "to", created_at, substr(content, 1, 255), chat FROM messages_old;

DROP TABLE messages_old;
`
	if _, err := d.ExecContext(ctx, rebuild); err != nil {
		return fmt.Errorf("rebuild messages: %w", err)
	}
	if _, err := d.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat)`); err != nil {
		return fmt.Errorf("rebuild messages index: %w", err)
	}
	return d.vacuum(ctx)
}

// rebuildUsersWithLongBio recreates the users table when its bio CHECK
// constraints still cap the bios at 64 symbols (SQLite cannot alter a
// constraint in place). It also repairs a half-applied rename left behind by
// an interrupted older run (users_old present).
func (d *DB) rebuildUsersWithLongBio(ctx context.Context) error {
	// Foreign keys must be off while renaming: without this, SQLite rewrites
	// the FKs of referencing tables (participants, messages) to users_old
	// and the later DROP fails with constraint errors. legacy_alter_table
	// keeps the referencing tables pointing at the final "users" name.
	for _, p := range []string{`PRAGMA foreign_keys=OFF`, `PRAGMA legacy_alter_table=ON`} {
		if _, err := d.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	defer func() {
		_, _ = d.ExecContext(ctx, `PRAGMA legacy_alter_table=OFF`)
		_, _ = d.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	}()

	var (
		curr, old sql.NullString
	)
	if err := d.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'users'`).Scan(&curr); err != nil {
		return fmt.Errorf("sqlite_master users: %w", err)
	}
	if err := d.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'users_old'`).Scan(&old); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sqlite_master users_old: %w", err)
	}

	if curr.Valid && strings.Contains(curr.String, "length(public_bio) <= 255") && !old.Valid {
		// Already at the long-bio schema and no leftover.
		return nil
	}

	if !curr.Valid || old.Valid {
		// Leftover from an interrupted rename: recreate the long-bio schema
		// fresh and copy everything back from users_old.
		if !curr.Valid {
			if _, err := d.ExecContext(ctx,
				`CREATE TABLE users (
		address     TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		room        TEXT NOT NULL REFERENCES rooms(address),
		public_bio  TEXT NOT NULL DEFAULT '' CHECK (length(public_bio) <= 255),
		private_bio TEXT NOT NULL DEFAULT '' CHECK (length(private_bio) <= 255),
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL,
		deleted_at  TEXT
	)`); err != nil {
				return fmt.Errorf("create users: %w", err)
			}
		}
		if _, err := d.ExecContext(ctx, `
INSERT OR REPLACE INTO users (address, name, room, public_bio, private_bio, created_at, updated_at, deleted_at)
SELECT address, name, room, public_bio, private_bio, created_at, updated_at, deleted_at FROM users_old`); err != nil {
			return fmt.Errorf("copy users_old back: %w", err)
		}
		if _, err := d.ExecContext(ctx, `DROP TABLE users_old`); err != nil {
			return fmt.Errorf("drop users_old: %w", err)
		}
		// The referencing tables may still carry FKs that point at
		// users_old (renamed under a different pragma mode): rebuild them.
		if err := d.repairUsersReference(ctx); err != nil {
			return err
		}
	} else {
		// Regular upgrade: 64-legacy schema present.
		const rebuild = `
ALTER TABLE users RENAME TO users_old;

CREATE TABLE users (
	address     TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	room        TEXT NOT NULL REFERENCES rooms(address),
	public_bio  TEXT NOT NULL DEFAULT '' CHECK (length(public_bio) <= 255),
	private_bio TEXT NOT NULL DEFAULT '' CHECK (length(private_bio) <= 255),
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL,
	deleted_at  TEXT
);

INSERT INTO users (address, name, room, public_bio, private_bio, created_at, updated_at, deleted_at)
SELECT address, name, room, public_bio, private_bio, created_at, updated_at, deleted_at
FROM users_old;

DROP TABLE users_old;
`
		if _, err := d.ExecContext(ctx, rebuild); err != nil {
			return fmt.Errorf("rebuild users: %w", err)
		}
	}

	if _, err := d.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_users_room ON users(room)`); err != nil {
		return fmt.Errorf("rebuild users index: %w", err)
	}
	// Also repair references left over from an earlier partial run even
	// when users_old itself is already gone.
	if err := d.repairUsersReference(ctx); err != nil {
		return err
	}
	if err := d.vacuum(ctx); err != nil {
		return err
	}
	return nil
}

// repairUsersReference rebuilds tables that still hold foreign keys pointing
// at users_old so they reference users again.
func (d *DB) repairUsersReference(ctx context.Context) error {
	const participantsDDL = `
CREATE TABLE participants (
	chat TEXT NOT NULL REFERENCES chats(address),
	user TEXT NOT NULL REFERENCES users(address),
	PRIMARY KEY (chat, user)
)`
	const messagesDDL = `
CREATE TABLE messages (
	tx_id      TEXT PRIMARY KEY,
	"from"     TEXT NOT NULL REFERENCES users(address),
	"to"       TEXT NOT NULL REFERENCES users(address),
	created_at TEXT NOT NULL,
	content    TEXT NOT NULL CHECK (length(content) <= 255),
	chat       TEXT NOT NULL REFERENCES chats(address)
)`
	if err := d.rebuildTableIfFK(ctx, "participants", participantsDDL); err != nil {
		return err
	}
	if err := d.rebuildTableIfFK(ctx, "messages", messagesDDL); err != nil {
		return err
	}
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_participants_user ON participants(user)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat)`,
	}
	for _, create := range indexes {
		if _, err := d.ExecContext(ctx, create); err != nil {
			return fmt.Errorf("recreate index: %w", err)
		}
	}
	return nil
}

// rebuildTableIfFK recreates a table (same columns, new DDL) when its stored
// CREATE statement still references users_old in a foreign key.
func (d *DB) rebuildTableIfFK(ctx context.Context, table, ddl string) error {
	var sqlText sql.NullString
	if err := d.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = '%s'`, table)).Scan(&sqlText); err != nil {
		return fmt.Errorf("sqlite_master %s: %w", table, err)
	}
	current := sqlText.String
	if !sqlText.Valid || !strings.Contains(current, `"users_old"`) { // #nosec G201 -- table name is an internal constant.
		return nil
	}

	const rebuild = `
ALTER TABLE %s RENAME TO %s_old;

%s;

INSERT INTO "%s" SELECT * FROM "%s_old";

DROP TABLE "%s_old";
`
	// #nosec G201 -- identifiers are internal constants, not user input.
	if _, err := d.ExecContext(ctx, fmt.Sprintf(rebuild, table, table, ddl, table, table, table)); err != nil {
		return fmt.Errorf("rebuild %s: %w", table, err)
	}
	return nil
}

// vacuum compacts the database file after table rebuilds.
func (d *DB) vacuum(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
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
	room         TEXT NOT NULL DEFAULT '',
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
