package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// rowQuerier is the subset of *sql.DB / *sql.Tx used for chat helpers.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// findChat returns the chat linking exactly two users, if any.
// from == to never matches anything by construction.
func findChat(ctx context.Context, q rowQuerier, a, b string) (model.Chat, bool, error) {
	row := q.QueryRowContext(ctx, `
SELECT c.address, c.created_at
FROM chats c
JOIN participants p1 ON p1.chat = c.address AND p1.user = ?
JOIN participants p2 ON p2.chat = c.address AND p2.user = ?
WHERE p1.user <> p2.user
ORDER BY c.created_at ASC
LIMIT 1`, a, b)

	var (
		c         model.Chat
		createdAt string
	)
	if err := row.Scan(&c.Address, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Chat{}, false, nil
		}
		return model.Chat{}, false, err
	}
	c.CreatedAt = parseTS(createdAt)
	return c, true, nil
}

// ensureChat creates the chat plus its two participants when the pair does
// not have one yet; idempotent. Runs on the given executor (tx or db).
func ensureChat(ctx context.Context, q rowQuerier, a, b string) (model.Chat, error) {
	if c, found, err := findChat(ctx, q, a, b); err != nil || found {
		return c, err
	}

	c := model.Chat{CreatedAt: time.Now().UTC()}
	for attempt := 0; attempt < 3; attempt++ {
		c.Address = newAddress(16)
		if _, err := q.ExecContext(ctx,
			`INSERT INTO chats (address, created_at) VALUES (?, ?)`,
			c.Address, c.CreatedAt.Format(time.RFC3339)); !isUnique(err) {
			if err != nil {
				return model.Chat{}, err
			}
			break
		}
	}
	if c.Address == "" {
		return model.Chat{}, errors.Join(ErrConflict, errors.New("could not generate unique chat address"))
	}

	// Participants are created automatically with the chat.
	for _, u := range []string{a, b} {
		if _, err := q.ExecContext(ctx,
			`INSERT INTO participants (chat, user) VALUES (?, ?)`, c.Address, u); err != nil && !isUnique(err) {
			return model.Chat{}, err
		}
	}
	return c, nil
}

// bothHandshakeDirections reports whether the handshakes a -> b and b -> a
// both exist. It runs on the given executor so a just-inserted row counts.
func bothHandshakeDirections(ctx context.Context, q rowQuerier, a, b string) (bool, error) {
	var both bool
	err := q.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1 FROM handshakes WHERE from_address = ? AND to_address = ?
) AND EXISTS (
	SELECT 1 FROM handshakes WHERE from_address = ? AND to_address = ?
)`, a, b, b, a).Scan(&both)
	return both, err
}

// GetChat returns a chat with its participants.
func (d *DB) GetChat(ctx context.Context, address string) (model.Chat, []model.Participant, error) {
	c, found, err := d.chatByAddress(ctx, address)
	if err != nil || !found {
		return model.Chat{}, nil, errors.Join(ErrNotFound, sql.ErrNoRows)
	}
	parts, err := d.ChatParticipants(ctx, address)
	if err != nil {
		return model.Chat{}, nil, err
	}
	return c, parts, nil
}

func (d *DB) chatByAddress(ctx context.Context, address string) (model.Chat, bool, error) {
	row := d.QueryRowContext(ctx,
		`SELECT address, created_at FROM chats WHERE address = ?`, address)
	var (
		c         model.Chat
		createdAt string
	)
	if err := row.Scan(&c.Address, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Chat{}, false, nil
		}
		return model.Chat{}, false, err
	}
	c.CreatedAt = parseTS(createdAt)
	return c, true, nil
}

// ChatParticipants returns the participants of a chat.
func (d *DB) ChatParticipants(ctx context.Context, chatAddress string) ([]model.Participant, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT chat, user FROM participants WHERE chat = ? ORDER BY user ASC`, chatAddress)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	parts := []model.Participant{}
	for rows.Next() {
		var p model.Participant
		if err := rows.Scan(&p.Chat, &p.User); err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	return parts, rows.Err()
}

// ListUserChats returns all chats the user participates in, newest first.
func (d *DB) ListUserChats(ctx context.Context, user string) ([]model.Chat, error) {
	rows, err := d.QueryContext(ctx, `
SELECT c.address, c.created_at
FROM chats c
JOIN participants p ON p.chat = c.address
WHERE p.user = ?
ORDER BY c.created_at DESC, c.address ASC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chats := []model.Chat{}
	for rows.Next() {
		var (
			c         model.Chat
			createdAt string
		)
		if err := rows.Scan(&c.Address, &createdAt); err != nil {
			return nil, err
		}
		c.CreatedAt = parseTS(createdAt)
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

// ChatExists reports whether a chat address is live.
func (d *DB) ChatExists(ctx context.Context, address string) (bool, error) {
	return d.exists(ctx, `SELECT EXISTS (SELECT 1 FROM chats WHERE address = ?)`, address)
}
