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

// ListChatMessages returns all messages of a chat, oldest first.
func (d *DB) ListChatMessages(ctx context.Context, chatAddress string) ([]model.Message, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT tx_id, "from", "to", created_at, content, chat
		 FROM messages WHERE chat = ?
		 ORDER BY created_at ASC, tx_id ASC`, chatAddress)
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
