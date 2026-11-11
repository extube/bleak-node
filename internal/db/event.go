package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// NextEvent returns the oldest pending queue entry that is eligible to run
// (its not_before moment, if any, has passed) without claiming it.
func (d *DB) NextEvent(ctx context.Context) (model.Event, bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	row := d.QueryRowContext(ctx, `
SELECT id, type, chat, speaker, listener, status, attempts, error, created_at, processed_at, not_before
FROM events
WHERE status = ? AND (not_before = '' OR not_before <= ?)
ORDER BY id ASC LIMIT 1`, EventPending, now)

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

// RequeueEvent puts an entry back to pending for another attempt. The delay
// is the retry backoff: the entry may not start again before now+delay
// (zero or negative delay makes it immediately eligible).
func (d *DB) RequeueEvent(ctx context.Context, id int64, delay time.Duration) error {
	notBefore := ""
	if delay > 0 {
		notBefore = time.Now().UTC().Add(delay).Format(time.RFC3339)
	}
	_, err := d.ExecContext(ctx,
		`UPDATE events SET status = ?, not_before = ? WHERE id = ?`, EventPending, notBefore, id)
	return err
}

// ReclaimProcessing returns processing events back to pending. olderThan
// bounds the staleness relative to the row creation time: a value of 0
// reclaims every processing row (safe for a single-node worker, where a
// processing row at startup can only come from a crashed run), a positive
// value reclaims only rows older than the cutoff as a defensive sweep.
// Reclaimed rows lose their backoff stamp so an interrupted turn retries
// right away.
func (d *DB) ReclaimProcessing(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339)
	res, err := d.ExecContext(ctx,
		`UPDATE events SET status = ?, error = '', not_before = ''
		 WHERE status = ? AND created_at <= ?`, EventPending, EventProcessing, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// TurnBubble is one message group of a processed turn: bubbles written by
// one author to another (a turn may carry several groups, e.g. the farewell
// of the speaker followed by the listener's pattern goodbye).
type TurnBubble struct {
	From     string
	To       string
	Contents []string
}

// TurnPhase stamps a chat phase change inside the turn transaction.
type TurnPhase struct {
	Phase     string
	StartedAt time.Time
}

// TurnNext is the next-turn event enqueued inside the turn transaction.
type TurnNext struct {
	Speaker  string
	Listener string
}

// CompleteTurn applies everything ONE processed event changes in ONE
// transaction: the turn's message bubbles (each group from its own author,
// every bubble validated against the room language), an optional chat phase
// stamp, an optional next-turn event, and the completion of the processed
// event itself (an empty bubbles list just completes the event - used for
// unknown event types). Either all of it commits or none: a crash or reload
// before the commit leaves the event reclaimable and the transcript
// untouched, so the retry regenerates the turn cleanly - no duplicated
// bubbles, no lost hand-off to the other agent, no broken dialog chain.
func (d *DB) CompleteTurn(ctx context.Context, eventID int64, bubbles []TurnBubble, phase *TurnPhase, next *TurnNext) ([]model.Message, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// All groups reference the same pair of users; validate the chat and
	// every involved address once.
	addresses := make([]string, 0, 2)
	var chat model.Chat
	for i, g := range bubbles {
		for _, addr := range []string{g.From, g.To} {
			seen := false
			for _, a := range addresses {
				if a == addr {
					seen = true
					break
				}
			}
			if !seen {
				addresses = append(addresses, addr)
			}
		}
		c, found, err := findChat(ctx, tx, g.From, g.To)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errors.Join(ErrNotFound, fmt.Errorf(
				"chat between %q and %q not found: handshakes in both directions are required", g.From, g.To))
		}
		if i == 0 {
			chat = c
		} else if c.Address != chat.Address {
			return nil, errors.Join(ErrInvalid, fmt.Errorf(
				"turn bubbles belong to different chats: %q and %q", chat.Address, c.Address))
		}
	}
	for _, addr := range addresses {
		var ok bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM users WHERE address = ? AND deleted_at IS NULL)`, addr).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.Join(ErrNotFound, fmt.Errorf("user %q not found", addr))
		}
	}
	// Single-language rooms: every bubble of the turn must be written in
	// the room (chat) language; a wrong-language bubble fails the whole
	// turn, keeping the transcript consistent.
	for _, g := range bubbles {
		for _, content := range g.Contents {
			if !model.TextMatchesLanguage(content, chat.Language) {
				return nil, errors.Join(ErrInvalid, fmt.Errorf(
					"message must be in the room language %q", chat.Language))
			}
		}
	}

	out := make([]model.Message, 0, 8)
	for _, g := range bubbles {
		for _, content := range g.Contents {
			m := model.Message{
				From:      g.From,
				To:        g.To,
				Content:   content,
				Chat:      chat.Address,
				CreatedAt: time.Now().UTC(),
			}
			if err := insertMessageOn(ctx, tx, &m); err != nil {
				return nil, err
			}
			out = append(out, m)
		}
	}

	if phase != nil {
		if err := setChatPhaseOn(ctx, tx, chat.Address, phase.Phase, phase.StartedAt); err != nil {
			return nil, err
		}
	}
	if next != nil {
		if err := enqueueEvent(ctx, tx, model.EventChatInteract, chat.Address, next.Speaker, next.Listener); err != nil {
			return nil, err
		}
	}
	if err := completeEventOn(ctx, tx, eventID, ""); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// completeEventOn marks an event done on the given executor.
func completeEventOn(ctx context.Context, q execContext, id int64, errMsg string) error {
	_, err := q.ExecContext(ctx,
		`UPDATE events SET status = ?, error = ?, processed_at = ? WHERE id = ?`,
		EventDone, errMsg, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// StalledChat is a chat the revival sweep considers abandoned: it has
// messages but no queue activity, and its last message is not a farewell.
type StalledChat struct {
	Chat        string
	LastFrom    string // author of the last message
	Other       string // the other participant: the next speaker
	LastAt      time.Time
	LastContent string
}

// StalledChats returns chats that look stalled: they carry messages, have no
// pending or processing event (so neither a running turn nor a backed-off
// retry explains the silence), and their last message is older than
// messageSilence. Chats whose last message matches one of the closedMarkers
// (the pattern goodbyes that end every conversation) are healthy and skipped.
func (d *DB) StalledChats(ctx context.Context, messageSilence time.Duration, closedMarkers []string) ([]StalledChat, error) {
	msgCutoff := time.Now().UTC().Add(-messageSilence).Format(time.RFC3339)
	rows, err := d.QueryContext(ctx, `
SELECT c.address,
       MIN(p.user), MAX(p.user),
       (SELECT m."from"    FROM messages m WHERE m.chat = c.address ORDER BY m.created_at DESC, m.rowid DESC LIMIT 1),
       (SELECT m.content   FROM messages m WHERE m.chat = c.address ORDER BY m.created_at DESC, m.rowid DESC LIMIT 1),
       (SELECT m.created_at FROM messages m WHERE m.chat = c.address ORDER BY m.created_at DESC, m.rowid DESC LIMIT 1)
FROM chats c
JOIN participants p ON p.chat = c.address
WHERE EXISTS (SELECT 1 FROM messages WHERE chat = c.address)
  AND NOT EXISTS (SELECT 1 FROM events WHERE chat = c.address AND status IN (?, ?))
GROUP BY c.address
HAVING (SELECT m.created_at FROM messages m WHERE m.chat = c.address ORDER BY m.created_at DESC, m.rowid DESC LIMIT 1) <= ?`,
		EventPending, EventProcessing, msgCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	closed := make(map[string]struct{}, len(closedMarkers))
	for _, m := range closedMarkers {
		closed[m] = struct{}{}
	}

	out := []StalledChat{}
	for rows.Next() {
		var (
			s         StalledChat
			a, b      string
			createdAt string
		)
		if err := rows.Scan(&s.Chat, &a, &b, &s.LastFrom, &s.LastContent, &createdAt); err != nil {
			return nil, err
		}
		s.LastAt = parseTS(createdAt)
		// The participant who did NOT write the last message speaks next -
		// the same hand-off the live chain performs after every turn.
		if s.LastFrom == a {
			s.Other = b
		} else {
			s.Other = a
		}
		if _, ok := closed[s.LastContent]; ok {
			continue // the conversation ended with a farewell: healthy
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanEvent(s scanner) (model.Event, error) {
	var (
		ev        model.Event
		createdAt string
		processed sql.NullString
		notBefore sql.NullString
	)
	if err := s.Scan(&ev.ID, &ev.Type, &ev.Chat, &ev.Speaker, &ev.Listener,
		&ev.Status, &ev.Attempts, &ev.Error, &createdAt, &processed, &notBefore); err != nil {
		return model.Event{}, err
	}
	ev.CreatedAt = parseTS(createdAt)
	ev.ProcessedAt = parseNullTS(processed)
	ev.NotBefore = parseNullTS(notBefore)
	return ev, nil
}
