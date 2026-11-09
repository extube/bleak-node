package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// openTestDB creates a temporary migrated database.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestEmptyDatabaseOpen(t *testing.T) {
	_ = openTestDB(t)
}

func TestReclaimProcessing(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	chat := "0xchat"

	// events carry foreign keys to chats, participants and users; build them.
	now := time.Now().UTC().Format(time.RFC3339)
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO rooms (address, name, created_at, updated_at) VALUES ('0xroom', 'r', ?, ?)`, []any{now, now}},
		{`INSERT INTO users (address, name, room, created_at, updated_at) VALUES ('0xa', 'a', '0xroom', ?, ?)`, []any{now, now}},
		{`INSERT INTO users (address, name, room, created_at, updated_at) VALUES ('0xb', 'b', '0xroom', ?, ?)`, []any{now, now}},
		{`INSERT INTO chats (address, room, created_at) VALUES ('0xchat', '0xroom', ?)`, []any{now}},
		{`INSERT INTO participants (chat, user) VALUES ('0xchat', '0xa'), ('0xchat', '0xb')`, nil},
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

	if err := d.EnqueueEvent(ctx, model.EventChatInteract, chat, "0xa", "0xb"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Reclaim of a non-processing event changes nothing.
	n, err := d.ReclaimProcessing(ctx, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if n != 0 {
		t.Fatalf("reclaimed %d events that were not processing", n)
	}

	// Simulate a crashed run: claim the event and leave it processing.
	ev, found, err := d.NextEvent(ctx)
	if err != nil || !found {
		t.Fatalf("next event: found=%t err=%v", found, err)
	}
	if err := d.StartEvent(ctx, ev.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	n, err = d.ReclaimProcessing(ctx, 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if n != 1 {
		t.Fatalf("reclaimed %d events, want 1", n)
	}

	// The event is pending again and gets picked up with its attempts kept.
	ev2, found, err := d.NextEvent(ctx)
	if err != nil || !found {
		t.Fatalf("next after reclaim: found=%t err=%v", found, err)
	}
	if ev2.ID != ev.ID || ev2.Status != EventPending || ev2.Attempts != 1 {
		t.Fatalf("event after reclaim: %+v", ev2)
	}
}
