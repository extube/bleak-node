package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// Queue event statuses.
const (
	EventPending    = "pending"
	EventProcessing = "processing"
	EventDone       = "done"
	EventFailed     = "failed"
)

// enqueueEvent inserts a queue row on the given executor (tx or db), so the
// event becomes visible atomically with the change that caused it.
func enqueueEvent(ctx context.Context, q rowQuerier, typ, chat, speaker, listener string) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO events (type, chat, speaker, listener, status, attempts, error, created_at)
VALUES (?, ?, ?, ?, ?, 0, '', ?)`,
		typ, chat, speaker, listener, EventPending, time.Now().UTC().Format(time.RFC3339))
	return err
}

// EnqueueEvent appends a task to the queue.
func (d *DB) EnqueueEvent(ctx context.Context, typ, chat, speaker, listener string) error {
	return enqueueEvent(ctx, d, typ, chat, speaker, listener)
}

// NextEvent returns the oldest pending queue entry without claiming it.
func (d *DB) NextEvent(ctx context.Context) (model.Event, bool, error) {
	row := d.QueryRowContext(ctx, `
SELECT id, type, chat, speaker, listener, status, attempts, error, created_at, processed_at
FROM events WHERE status = ? ORDER BY id ASC LIMIT 1`, EventPending)

	ev, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Event{}, false, nil
	}
	if err != nil {
		return model.Event{}, false, err
	}
	return ev, true, nil
}

// StartEvent claims an entry: it moves to "processing" and bumps attempts.
func (d *DB) StartEvent(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx,
		`UPDATE events SET status = ?, attempts = attempts + 1 WHERE id = ? AND status = ?`,
		EventProcessing, id, EventPending)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return errors.Join(ErrConflict, errors.New("event already claimed"))
	}
	return nil
}

// RequeueEvent puts an entry back to pending for another attempt.
func (d *DB) RequeueEvent(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx,
		`UPDATE events SET status = ? WHERE id = ?`, EventPending, id)
	return err
}

// CompleteEvent marks an entry done (errMsg empty) or failed.
func (d *DB) CompleteEvent(ctx context.Context, id int64, errMsg string) error {
	status := EventDone
	if errMsg != "" {
		status = EventFailed
	}
	_, err := d.ExecContext(ctx,
		`UPDATE events SET status = ?, error = ?, processed_at = ? WHERE id = ?`,
		status, errMsg, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func scanEvent(s scanner) (model.Event, error) {
	var (
		ev        model.Event
		createdAt string
		processed sql.NullString
	)
	if err := s.Scan(&ev.ID, &ev.Type, &ev.Chat, &ev.Speaker, &ev.Listener,
		&ev.Status, &ev.Attempts, &ev.Error, &createdAt, &processed); err != nil {
		return model.Event{}, err
	}
	ev.CreatedAt = parseTS(createdAt)
	ev.ProcessedAt = parseNullTS(processed)
	return ev, nil
}
