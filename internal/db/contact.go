// Shared contacts (feature #35): one persona shares its contact details
// with another. The (from, to) pair is the unique key - a share happens
// only once per pair - and only the recipient reads the record.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/extube/bleak-node/internal/model"
)

// ShareContact stores the contact details `from` shares with `to`. Both
// users must exist (non-deleted) and differ; the content is required and
// capped at MaxContactContentLen symbols. A repeated share of the same
// (from, to) pair is ErrConflict: contacts can be shared only once.
func (d *DB) ShareContact(ctx context.Context, from, to, content string) (model.Contact, error) {
	var out model.Contact
	if from == to {
		return out, errors.Join(ErrInvalid, errors.New("from and to must differ"))
	}
	if n := utf8.RuneCountInString(content); n == 0 || n > model.MaxContactContentLen {
		return out, errors.Join(ErrInvalid, fmt.Errorf(
			"contact content must be 1..%d symbols (got %d)", model.MaxContactContentLen, n))
	}

	for _, addr := range []string{from, to} {
		ok, err := d.UserExists(ctx, addr)
		if err != nil {
			return out, err
		}
		if !ok {
			return out, errors.Join(ErrNotFound, fmt.Errorf("user %q not found", addr))
		}
	}

	out = model.Contact{From: from, To: to, Content: content, CreatedAt: time.Now().UTC()}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO contacts ("from", "to", content, created_at)
		VALUES (?, ?, ?, ?)`,
		from, to, content, out.CreatedAt.Format(time.RFC3339)); err != nil {
		if isUnique(err) {
			return model.Contact{}, errors.Join(ErrConflict, fmt.Errorf(
				"contacts from %q to %q are already shared", from, to))
		}
		return model.Contact{}, err
	}
	return out, nil
}

// ListContactsTo returns the contacts shared with the given user (only the
// recipient has access; the caller's authorization enforces that), newest
// first.
func (d *DB) ListContactsTo(ctx context.Context, address string) ([]model.Contact, error) {
	ok, err := d.UserExists(ctx, address)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.Join(ErrNotFound, fmt.Errorf("user %q not found", address))
	}

	rows, err := d.QueryContext(ctx, `
SELECT "from", "to", content, created_at
FROM contacts
WHERE "to" = ?
ORDER BY created_at DESC, "from" ASC`, address)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Contact{}
	for rows.Next() {
		var (
			c         model.Contact
			createdAt string
		)
		if err := rows.Scan(&c.From, &c.To, &c.Content, &createdAt); err != nil {
			return nil, err
		}
		c.CreatedAt = parseTS(createdAt)
		out = append(out, c)
	}
	return out, rows.Err()
}
