package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// CreateMessage posts a message from a user to another inside the chat of
// the pair. The chat must exist (created automatically once both handshake
// directions A -> B and B -> A are present). The tx_id is generated.
func (d *DB) CreateMessage(ctx context.Context, m *model.Message) error {
	chat, found, err := findChat(ctx, d, m.From, m.To)
	if err != nil {
		return err
	}
	if !found {
		return errors.Join(ErrNotFound, fmt.Errorf(
			"chat between %q and %q not found: handshakes in both directions are required", m.From, m.To))
	}

	for _, addr := range []string{m.From, m.To} {
		ok, err := d.UserExists(ctx, addr)
		if err != nil {
			return err
		}
		if !ok {
			return errors.Join(ErrNotFound, fmt.Errorf("user %q not found", addr))
		}
	}

	m.Chat = chat.Address
	m.CreatedAt = time.Now().UTC()

	// Single-language rooms: a message must be written in the room (chat)
	// language.
	if !model.TextMatchesLanguage(m.Content, chat.Language) {
		return errors.Join(ErrInvalid, fmt.Errorf(
			"message must be in the room language %q", chat.Language))
	}

	// tx_id is generated server-side; retry on the unlikely collision.
	for attempt := 0; attempt < 3; attempt++ {
		m.TxID = newTxID()
		_, err := d.ExecContext(ctx,
			`INSERT INTO messages (tx_id, "from", "to", created_at, content, chat) VALUES (?, ?, ?, ?, ?, ?)`,
			m.TxID, m.From, m.To, m.CreatedAt.Format(time.RFC3339), m.Content, m.Chat)
		if isUnique(err) {
			continue
		}
		if err != nil {
			return err
		}
		return nil
	}
	return errors.Join(ErrConflict, errors.New("could not generate unique message tx_id"))
}

// CreateMessages writes several messages of ONE interaction atomically:
// the chat lookup and the user checks run once, and every message is
// inserted within the same transaction. It is used by the agent worker for
// replies saved as one natural message bubble per turn; the caller queues a
// single next-turn event after the message.
func (d *DB) CreateMessages(ctx context.Context, from, to string, contents []string) ([]model.Message, error) {
	if len(contents) == 0 {
		return nil, errors.Join(ErrInvalid, errors.New("no messages to create"))
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	chat, found, err := findChat(ctx, tx, from, to)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.Join(ErrNotFound, fmt.Errorf(
			"chat between %q and %q not found: handshakes in both directions are required", from, to))
	}
	// Single-language rooms: every message must be written in the room
	// (chat) language; a wrong-language batch fails the whole turn.
	for _, content := range contents {
		if !model.TextMatchesLanguage(content, chat.Language) {
			return nil, errors.Join(ErrInvalid, fmt.Errorf(
				"message must be in the room language %q", chat.Language))
		}
	}
	for _, addr := range []string{from, to} {
		var ok bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM users WHERE address = ? AND deleted_at IS NULL)`, addr).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.Join(ErrNotFound, fmt.Errorf("user %q not found", addr))
		}
	}

	out := make([]model.Message, 0, len(contents))
	for _, content := range contents {
		m := model.Message{
			From:      from,
			To:        to,
			Content:   content,
			Chat:      chat.Address,
			CreatedAt: time.Now().UTC(),
		}
		for attempt := 0; attempt < 3; attempt++ {
			m.TxID = newTxID()
			_, err := tx.ExecContext(ctx,
				`INSERT INTO messages (tx_id, "from", "to", created_at, content, chat) VALUES (?, ?, ?, ?, ?, ?)`,
				m.TxID, m.From, m.To, m.CreatedAt.Format(time.RFC3339), m.Content, m.Chat)
			if isUnique(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			break
		}
		if m.TxID == "" {
			return nil, errors.Join(ErrConflict, errors.New("could not generate unique message tx_id"))
		}
		out = append(out, m)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListChatMessages returns all messages of a chat, oldest first. The rowid
// tie-break keeps sentence-split batches (one interaction, one second
// timestamp) in real insertion order; tx ids are random hex and would
// shuffle the bubbles.
func (d *DB) ListChatMessages(ctx context.Context, chatAddress string) ([]model.Message, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT tx_id, "from", "to", created_at, content, chat
		 FROM messages WHERE chat = ?
		 ORDER BY created_at ASC, rowid ASC`, chatAddress)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := []model.Message{}
	for rows.Next() {
		var (
			m         model.Message
			createdAt string
		)
		if err := rows.Scan(&m.TxID, &m.From, &m.To, &createdAt, &m.Content, &m.Chat); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTS(createdAt)
		messages = append(messages, m)
	}
	return messages, rows.Err()
}
