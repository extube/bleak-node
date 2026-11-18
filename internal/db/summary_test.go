package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// summarizeStub returns a fixed SummaryInput: the first n checkpoints
// closed, the description given.
func summarizeStub(n int, description string) SummarizeFunc {
	return func(ctx context.Context, chat model.Chat, msgs []model.Message) (SummaryInput, error) {
		closed := make([]bool, n)
		for i := range closed {
			closed[i] = true
		}
		return SummaryInput{Description: description, Closed: closed}, nil
	}
}

func TestHardMatchCreatesOnce(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	seedMessage(t, d, "0xchat", "0xa", "0xb", "Hello there!", time.Now().UTC())
	seedMessage(t, d, "0xchat", "0xb", "0xa", "Nice to meet you.", time.Now().UTC())
	ctx := context.Background()

	// No relations: the soft score is the neutral 50; 3 of 5 checkpoints
	// closed grade as the neutral hard 50; the merge is (50 + 50) / 2 = 50.
	summary, created, err := d.HardMatch(ctx, "0xroom", "0xa", "0xb",
		summarizeStub(3, "A friendly exchange."))
	if err != nil {
		t.Fatalf("hard match: %v", err)
	}
	if !created {
		t.Fatal("first hard match must create the record")
	}
	if summary.Score != 50 {
		t.Fatalf("merged score: want 50, got %v", summary.Score)
	}
	if summary.Chat != "0xchat" || summary.Room != "0xroom" {
		t.Fatalf("chat/room wrong: %+v", summary)
	}
	// The canonical pair order: 0xa < 0xb.
	if summary.UserA != "0xa" || summary.UserB != "0xb" {
		t.Fatalf("canonical pair wrong: %+v", summary)
	}
	if len(summary.Checkpoints) != 5 {
		t.Fatalf("checkpoints: want 5, got %d", len(summary.Checkpoints))
	}
	closedCount := 0
	for _, cp := range summary.Checkpoints {
		if cp.Closed {
			closedCount++
		}
	}
	if closedCount != 3 {
		t.Fatalf("closed checkpoints: want 3, got %d", closedCount)
	}
	if summary.Description != "A friendly exchange." {
		t.Fatalf("description: got %q", summary.Description)
	}

	// A repeated call returns the STORED record without re-summarizing.
	called := false
	tracing := func(ctx context.Context, chat model.Chat, msgs []model.Message) (SummaryInput, error) {
		called = true
		return SummaryInput{Description: "should not be used"}, nil
	}
	again, created, err := d.HardMatch(ctx, "0xroom", "0xa", "0xb", tracing)
	if err != nil {
		t.Fatalf("hard match repeat: %v", err)
	}
	if created {
		t.Fatal("second hard match must not create a new record")
	}
	if called {
		t.Fatal("second hard match must not call the summarizer")
	}
	if again.Score != summary.Score || again.Description != summary.Description {
		t.Fatalf("stored summary changed: %+v vs %+v", again, summary)
	}

	// The pair order does not matter: b,a resolves to the same record.
	reversed, created, err := d.HardMatch(ctx, "0xroom", "0xb", "0xa", tracing)
	if err != nil || created {
		t.Fatalf("reversed pair: created=%t err=%v", created, err)
	}
	if reversed.Score != summary.Score {
		t.Fatalf("reversed pair scored differently: %v vs %v", reversed.Score, summary.Score)
	}
}

func TestHardMatchIgnoresHandshakes(t *testing.T) {
	// Owner directive: handshakes impact NO value for hard matching - the
	// method is only reachable for mutual pairs anyway (the chat requires
	// both directions), so handshake points would be a constant +8 for
	// everyone. A mutual pair with NO shared interests must grade the same
	// as a pair without any relations: soft stays the neutral 50.
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, dir := range []struct{ from, to, tx string }{
		{"0xa", "0xb", "0xtx1"}, {"0xb", "0xa", "0xtx2"},
	} {
		if _, err := d.ExecContext(ctx,
			`INSERT INTO handshakes (tx_id, from_address, to_address, room, status, value, created_at)
			 VALUES (?, ?, ?, '0xroom', 1, 1, ?)`,
			dir.tx, dir.from, dir.to, now); err != nil {
			t.Fatalf("seed handshake: %v", err)
		}
	}

	// 3 of 5 checkpoints closed grade the neutral hard 50; the soft score
	// must NOT carry the mutual handshake (+8 would make it 79.14).
	summary, _, err := d.HardMatch(ctx, "0xroom", "0xa", "0xb",
		summarizeStub(3, "A friendly exchange."))
	if err != nil {
		t.Fatalf("hard match: %v", err)
	}
	if summary.Score != 50 {
		t.Fatalf("hard match must ignore handshakes: want merged 50, got %v", summary.Score)
	}
}

func TestHardMatchValidation(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ctx := context.Background()

	// Same user.
	if _, _, err := d.HardMatch(ctx, "0xroom", "0xa", "0xa", summarizeStub(0, "")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same user: want ErrInvalid, got %v", err)
	}
	// Unknown room/user.
	if _, _, err := d.HardMatch(ctx, "0xnone", "0xa", "0xb", summarizeStub(0, "")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown room: want ErrNotFound, got %v", err)
	}
	// The pair has no chat: hard matching is impossible.
	if _, _, err := d.HardMatch(ctx, "0xroom", "0xa", "0xzz", summarizeStub(0, "")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: want ErrNotFound, got %v", err)
	}

	// A pair with handshakes but no chat yet.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.ExecContext(ctx,
		`INSERT INTO users (address, name, room, created_at, updated_at) VALUES ('0xc', 'c', '0xroom', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, _, err := d.HardMatch(ctx, "0xroom", "0xa", "0xc", summarizeStub(0, "")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing chat: want ErrNotFound, got %v", err)
	}
}

func TestHardMatchScoreStaysOpen(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	seedMessage(t, d, "0xchat", "0xa", "0xb", "Hello!", time.Now().UTC())
	ctx := context.Background()

	// Every checkpoint closed AND an extreme soft score still merges
	// strictly inside (0, 100).
	for i := 0; i < 30; i++ {
		for _, addr := range []string{"0xa", "0xb"} {
			if _, err := d.ExecContext(ctx,
				`INSERT INTO interests (user_address, title, "group", "key") VALUES (?, ?, 1, ?)`,
				addr, "x", i); err != nil {
				t.Fatalf("seed interest: %v", err)
			}
		}
	}
	for _, dir := range []struct{ from, to, tx string }{
		{"0xa", "0xb", "0xtx1"}, {"0xb", "0xa", "0xtx2"},
	} {
		if _, err := d.ExecContext(ctx,
			`INSERT INTO handshakes (tx_id, from_address, to_address, room, status, value, created_at)
			 VALUES (?, ?, ?, '0xroom', 1, 1, ?)`,
			dir.tx, dir.from, dir.to, time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatalf("seed handshake: %v", err)
		}
	}

	summary, _, err := d.HardMatch(ctx, "0xroom", "0xa", "0xb", summarizeStub(5, "All closed."))
	if err != nil {
		t.Fatalf("hard match: %v", err)
	}
	if summary.Score <= 0 || summary.Score >= 100 {
		t.Fatalf("merged score left the open interval: %v", summary.Score)
	}
}
