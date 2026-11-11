package db

import (
	"context"
	"testing"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// seedPairFixture creates a room with two users, so chats can be built on
// top of it.
func seedPairFixture(t *testing.T, d *DB, chat string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`INSERT OR IGNORE INTO rooms (address, name, created_at, updated_at) VALUES ('0xroom', 'r', ?, ?)`, []any{now, now}},
		{`INSERT OR IGNORE INTO users (address, name, room, created_at, updated_at) VALUES ('0xa', 'a', '0xroom', ?, ?)`, []any{now, now}},
		{`INSERT OR IGNORE INTO users (address, name, room, created_at, updated_at) VALUES ('0xb', 'b', '0xroom', ?, ?)`, []any{now, now}},
		{`INSERT OR IGNORE INTO chats (address, room, created_at) VALUES (?, '0xroom', ?)`, []any{chat, now}},
		{`INSERT OR IGNORE INTO participants (chat, user) VALUES (?, '0xa'), (?, '0xb')`, []any{chat, chat}},
	} {
		if stmt.args == nil {
			if _, err := d.ExecContext(ctx, stmt.q); err != nil {
				t.Fatalf("fixture: %v", err)
			}
			continue
		}
		if _, err := d.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
}

// seedMessage inserts a message row with an explicit timestamp.
func seedMessage(t *testing.T, d *DB, chat, from, to, content string, createdAt time.Time) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO messages (tx_id, "from", "to", created_at, content, chat)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"0xmsg"+newTxID(), from, to, createdAt.UTC().Format(time.RFC3339), content, chat); err != nil {
		t.Fatalf("seed message: %v", err)
	}
}

// queuedEvent enqueues and claims one event, returning it as the worker sees it.
func queuedEvent(t *testing.T, d *DB, chat, speaker, listener string) model.Event {
	t.Helper()
	ctx := context.Background()
	if err := d.EnqueueEvent(ctx, model.EventChatInteract, chat, speaker, listener); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	ev, found, err := d.NextEvent(ctx)
	if err != nil || !found {
		t.Fatalf("next event: found=%t err=%v", found, err)
	}
	if err := d.StartEvent(ctx, ev.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	return ev
}

func TestCompleteTurnCommitsEverything(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ev := queuedEvent(t, d, "0xchat", "0xa", "0xb")

	// One turn: two reply bubbles of the speaker, a phase stamp and the
	// hand-off event to the listener.
	saved, err := d.CompleteTurn(context.Background(), ev.ID,
		[]TurnBubble{
			{From: "0xa", To: "0xb", Contents: []string{"Hello there!", "Nice to meet you."}},
		},
		&TurnPhase{Phase: "networking", StartedAt: time.Now()},
		&TurnNext{Speaker: "0xb", Listener: "0xa"})
	if err != nil {
		t.Fatalf("complete turn: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("saved %d messages, want 2", len(saved))
	}

	ctx := context.Background()
	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM events WHERE id = ?`, ev.ID).Scan(&status); err != nil {
		t.Fatalf("event lookup: %v", err)
	}
	if status != EventDone {
		t.Fatalf("processed event status = %q, want done", status)
	}

	var speaker, listener, nextStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT speaker, listener, status FROM events WHERE id <> ?`, ev.ID).
		Scan(&speaker, &listener, &nextStatus); err != nil {
		t.Fatalf("next event lookup: %v", err)
	}
	if speaker != "0xb" || listener != "0xa" || nextStatus != EventPending {
		t.Fatalf("next event = (%s -> %s, %s)", speaker, listener, nextStatus)
	}

	var phase string
	if err := d.QueryRowContext(ctx,
		`SELECT phase FROM chats WHERE address = '0xchat'`).Scan(&phase); err != nil {
		t.Fatalf("chat lookup: %v", err)
	}
	if phase != "networking" {
		t.Fatalf("chat phase = %q, want networking", phase)
	}
}

func TestCompleteTurnRejectsWrongLanguageAtomically(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ev := queuedEvent(t, d, "0xchat", "0xa", "0xb")

	// The chat inherits the room default language 'en'; a Russian bubble
	// fails the WHOLE turn: no messages, no next event, event stays
	// processing (retryable), transcript untouched.
	_, err := d.CompleteTurn(context.Background(), ev.ID,
		[]TurnBubble{{From: "0xa", To: "0xb", Contents: []string{"Hello!", "Привет, это по-русски."}}},
		nil, &TurnNext{Speaker: "0xb", Listener: "0xa"})
	if err == nil {
		t.Fatal("wrong-language turn accepted")
	}

	ctx := context.Background()
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d messages saved by a failed turn", n)
	}
	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM events WHERE id = ?`, ev.ID).Scan(&status); err != nil {
		t.Fatalf("event lookup: %v", err)
	}
	if status != EventProcessing {
		t.Fatalf("event status after failed turn = %q, want processing", status)
	}
}

func TestRequeueEventBackoff(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ev := queuedEvent(t, d, "0xchat", "0xa", "0xb")

	// Requeue with a backoff delay: the event is pending but not eligible.
	if err := d.RequeueEvent(context.Background(), ev.ID, 2*time.Minute); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if _, found, err := d.NextEvent(context.Background()); err != nil || found {
		t.Fatalf("backed-off event was picked: found=%t err=%v", found, err)
	}

	// Once the not_before moment passes, the event becomes eligible again;
	// the parse gives the caller the visibility into the schedule.
	if _, err := d.ExecContext(context.Background(),
		`UPDATE events SET not_before = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), ev.ID); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	ev2, found, err := d.NextEvent(context.Background())
	if err != nil || !found {
		t.Fatalf("next after backoff: found=%t err=%v", found, err)
	}
	if ev2.ID != ev.ID {
		t.Fatalf("got event %d, want %d", ev2.ID, ev.ID)
	}
	if ev2.NotBefore == nil || ev2.NotBefore.After(time.Now().UTC()) {
		t.Fatalf("not_before not parsed: %v", ev2.NotBefore)
	}

	// A requeue without delay makes the event immediately eligible.
	if err := d.RequeueEvent(context.Background(), ev.ID, 0); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if _, found, _ := d.NextEvent(context.Background()); !found {
		t.Fatal("immediate requeue not picked")
	}
}

func TestStalledChats(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	seedPairFixture(t, d, "0xchat2")
	seedPairFixture(t, d, "0xchat3")
	ctx := context.Background()

	old := time.Now().UTC().Add(-30 * time.Minute)
	// 0xchat: a live conversation with no queue activity - stalled.
	seedMessage(t, d, "0xchat", "0xa", "0xb", "Hello there!", old)
	// 0xchat2: ends with a pattern goodbye - closed, healthy.
	seedMessage(t, d, "0xchat2", "0xa", "0xb", "Hello there!", old)
	seedMessage(t, d, "0xchat2", "0xb", "0xa", "Давай, до связи! Может, ещё как-нибудь поболтаем.", old)
	// 0xchat3: a pending event explains the silence - busy.
	seedMessage(t, d, "0xchat3", "0xb", "0xa", "Hi!", old)
	if err := d.EnqueueEvent(ctx, model.EventChatInteract, "0xchat3", "0xa", "0xb"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	markers := []string{"Давай, до связи! Может, ещё как-нибудь поболтаем."}
	stalled, err := d.StalledChats(ctx, 3*time.Minute, 10*time.Minute, markers)
	if err != nil {
		t.Fatalf("stalled: %v", err)
	}
	if len(stalled) != 1 || stalled[0].Chat != "0xchat" {
		t.Fatalf("stalled chats = %+v, want only 0xchat", stalled)
	}
	s := stalled[0]
	// The last message of 0xchat is from 0xa, so 0xb speaks next.
	if s.LastFrom != "0xa" || s.Other != "0xb" {
		t.Fatalf("revival hand-off = %s -> %s, want 0xa -> 0xb", s.Other, s.LastFrom)
	}
}
