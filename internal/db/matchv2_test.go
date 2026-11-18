package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

func TestMatchPointsV2OpenInterval(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ctx := context.Background()

	// No relations at all: the neutral midpoint.
	res, err := d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.Points != 50 {
		t.Fatalf("neutral score: want 50, got %v", res.Points)
	}

	// A one-sided handshake lifts the score, still inside the interval.
	if _, err := d.ExecContext(ctx,
		`INSERT INTO handshakes (tx_id, from_address, to_address, room, status, value, created_at)
		 VALUES ('0xtx1', '0xa', '0xb', '0xroom', 1, 1, ?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed handshake: %v", err)
	}
	res, err = d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	// coef 4: 50 + 50 * tanh(4/12) = 66.08.
	if res.Points != 66.08 {
		t.Fatalf("handshake score: want 66.08, got %v", res.Points)
	}
	if !res.Handshake || res.MutualHandshake {
		t.Fatalf("handshake flags wrong: %+v", res)
	}

	// Extreme relations (mutual handshake + many exact matches) never touch
	// the endpoints: 0 and 100 are impossible.
	for i := 0; i < 30; i++ {
		for _, addr := range []string{"0xa", "0xb"} {
			if _, err := d.ExecContext(ctx,
				`INSERT INTO interests (user_address, title, "group", "key") VALUES (?, ?, 1, ?)`,
				addr, "x", i); err != nil {
				t.Fatalf("seed interest: %v", err)
			}
		}
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO handshakes (tx_id, from_address, to_address, room, status, value, created_at)
		 VALUES ('0xtx2', '0xb', '0xa', '0xroom', 1, 1, ?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed handshake: %v", err)
	}
	res, err = d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.Points <= 0 || res.Points >= 100 {
		t.Fatalf("extreme score left the open interval: %v", res.Points)
	}
	if res.Points != 99.99 {
		t.Fatalf("extreme score: want the 99.99 ceiling, got %v", res.Points)
	}
	if !res.MutualHandshake {
		t.Fatalf("mutual handshake flag wrong: %+v", res)
	}
}

func TestMatchPointsV2NoFullMatchFromHandshakeAlone(t *testing.T) {
	// Difficulty calibration: a mutual handshake without shared interests
	// (coef 8) must stay BELOW the 80 full-match line (79.14).
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
	res, err := d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.Points != 79.14 {
		t.Fatalf("mutual-only score: want 79.14, got %v", res.Points)
	}
}

func TestMatchPointsV2TimeIndependentAndStable(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ctx := context.Background()

	// One exact interest match (coef 5) plus a shared group (coef 2).
	for _, addr := range []string{"0xa", "0xb"} {
		if _, err := d.AddUserInterests(ctx, "0xroom", addr,
			[]model.Interest{{Title: "Chess", Group: 1, Key: 7}}); err != nil {
			t.Fatalf("add interests: %v", err)
		}
	}
	first, err := d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	// coef 7: 50 + 50 * tanh(7/12) = 76.25.
	if first.Points != 76.25 {
		t.Fatalf("interest score: want 76.25, got %v", first.Points)
	}
	second, err := d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if first.Points != second.Points {
		t.Fatalf("score is not stable over time: %v then %v", first.Points, second.Points)
	}
}

func TestMatchPointsV2Validation(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ctx := context.Background()

	if _, err := d.MatchPointsV2(ctx, "0xroom", "0xa", "0xa"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same user: want ErrInvalid, got %v", err)
	}
	if _, err := d.MatchPointsV2(ctx, "0xroom", "0xa", "0xunknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: want ErrNotFound, got %v", err)
	}
}
