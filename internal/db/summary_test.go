package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// summarizeStub returns a fixed SummaryInput: the first n checkpoints
// closed, the description given. It echoes the subject it was called for.
func summarizeStub(n int, description string) SummarizeFunc {
	return func(ctx context.Context, chat model.Chat, msgs []model.Message, subject string) (SummaryInput, error) {
		closed := make([]bool, n)
		for i := range closed {
			closed[i] = true
		}
		return SummaryInput{Description: description, Closed: closed}, nil
	}
}

func TestHardMatchPerDirection(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	seedMessage(t, d, "0xchat", "0xa", "0xb", "Hello there!", time.Now().UTC())
	seedMessage(t, d, "0xchat", "0xb", "0xa", "Nice to meet you.", time.Now().UTC())
	ctx := context.Background()

	// No interests: MatchV2 = 0; 2 of 3 criteria closed grade 2 * 16 = 32.
	summary, created, err := d.HardMatch(ctx, "0xroom", "0xa", "0xb",
		summarizeStub(2, "A useful exchange."))
	if err != nil {
		t.Fatalf("hard match: %v", err)
	}
	if !created {
		t.Fatal("first hard match must create the record")
	}
	if summary.Score != 32 {
		t.Fatalf("merged score: want 32 (16 * 2 closed, MatchV2 = 0), got %v", summary.Score)
	}
	if summary.Chat != "0xchat" || summary.Room != "0xroom" {
		t.Fatalf("chat/room wrong: %+v", summary)
	}
	// The DIRECTIONAL pair: user_a is the requester (0xa -> 0xb).
	if summary.UserA != "0xa" || summary.UserB != "0xb" {
		t.Fatalf("direction wrong: %+v", summary)
	}
	if len(summary.Checkpoints) != 3 {
		t.Fatalf("checkpoints: want 3, got %d", len(summary.Checkpoints))
	}
	closedCount := 0
	for _, cp := range summary.Checkpoints {
		if cp.Closed {
			closedCount++
		}
	}
	if closedCount != 2 {
		t.Fatalf("closed checkpoints: want 2, got %d", closedCount)
	}
	if summary.Description != "A useful exchange." {
		t.Fatalf("description: got %q", summary.Description)
	}

	// A repeated call for the SAME direction returns the STORED record
	// without re-summarizing.
	called := false
	tracing := func(ctx context.Context, chat model.Chat, msgs []model.Message, subject string) (SummaryInput, error) {
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

	// The REVERSE direction is a separate record of its own: b -> a
	// creates a fresh summary with its own grading (the formula is
	// direction-symmetric, so the score matches, but the stored direction
	// flips).
	reversed, created, err := d.HardMatch(ctx, "0xroom", "0xb", "0xa", summarizeStub(2, "B's own view."))
	if err != nil {
		t.Fatalf("reversed pair: %v", err)
	}
	if !created {
		t.Fatal("the reverse direction must create its own record")
	}
	if reversed.UserA != "0xb" || reversed.UserB != "0xa" {
		t.Fatalf("reversed direction wrong: %+v", reversed)
	}
	if reversed.Score != summary.Score {
		t.Fatalf("reversed score must match the formula: %v vs %v", reversed.Score, summary.Score)
	}
	if reversed.Description != "B's own view." {
		t.Fatalf("reversed summary must carry its own description: %q", reversed.Description)
	}
}

func TestHardMatchChecklistWeight(t *testing.T) {
	// score = 16 * closed + 0.52 * MatchV2. With no interests MatchV2 = 0,
	// so each closed criterion is exactly 16 points.
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ctx := context.Background()
	for _, tc := range []struct{ closed int }{{0}, {1}, {3}} {
		if _, err := d.ExecContext(ctx, `DELETE FROM summaries`); err != nil {
			t.Fatalf("clear summaries: %v", err)
		}
		summary, created, err := d.HardMatch(ctx, "0xroom", "0xa", "0xb", summarizeStub(tc.closed, "d"))
		if err != nil {
			t.Fatalf("hard match: %v", err)
		}
		if !created {
			t.Fatalf("direction must be created once per stub run (closed=%d)", tc.closed)
		}
		if closed := tc.closed; closed > 0 {
			if want := 16.0 * float64(closed); summary.Score != want {
				t.Fatalf("closed=%d: want %.0f, got %v", closed, want, summary.Score)
			}
		} else if summary.Score != 0.01 {
			t.Fatalf("nothing closed: want the clamped 0.01, got %v", summary.Score)
		}
	}
}

func TestHardMatchHandshakesWeighNothing(t *testing.T) {
	// Owner directive: handshakes impact NO value — the formula is
	// 16 * closed + 0.52 * MatchV2, and MatchV2 weighs interests alone.
	// A mutual pair with NO shared interests grades by the checklist only.
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

	summary, _, err := d.HardMatch(ctx, "0xroom", "0xa", "0xb", summarizeStub(2, "d"))
	if err != nil {
		t.Fatalf("hard match: %v", err)
	}
	if summary.Score != 32 {
		t.Fatalf("handshakes must weigh nothing: want 32, got %v", summary.Score)
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

	// Identical interest sets grade the full MatchV2 = 100, and every
	// criterion closed adds 48: the sum is exactly 100 and must still land
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

	summary, _, err := d.HardMatch(ctx, "0xroom", "0xa", "0xb", summarizeStub(3, "All closed."))
	if err != nil {
		t.Fatalf("hard match: %v", err)
	}
	if summary.Score <= 0 || summary.Score >= 100 {
		t.Fatalf("merged score left the open interval: %v", summary.Score)
	}
	if summary.Score != 99.99 {
		t.Fatalf("perfect score must clamp to 99.99, got %v", summary.Score)
	}
}
