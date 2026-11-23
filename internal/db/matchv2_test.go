package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

func TestMatchPointsV2InterestPercent(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ctx := context.Background()

	// No interests at all: 0 (the empty percent).
	res, err := d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.Points != 0 {
		t.Fatalf("empty percent: want 0, got %v", res.Points)
	}

	// Identical interest sets: every match counts on both sides, so
	// 2 * 2 / (2 + 2) = 1 -> 100 points.
	for _, key := range []int{7, 8} {
		for _, addr := range []string{"0xa", "0xb"} {
			if _, err := d.AddUserInterests(ctx, "0xroom", addr,
				[]model.Interest{{Title: "Chess", Group: 1, Key: key}}); err != nil {
				t.Fatalf("add interests: %v", err)
			}
		}
	}
	res, err = d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.Points != 100 || res.ExactMatches != 2 {
		t.Fatalf("identical sets: want 100, got %v (%+v)", res.Points, res)
	}

	// Partial overlap: A {7, 8}, B {7} -> 2 * 1 / (2 + 1) -> 66.67.
	if _, _, err := d.DeleteUserInterests(ctx, "0xroom", "0xb", []int{8}); err != nil {
		t.Fatalf("delete interests: %v", err)
	}
	res, err = d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.Points != 66.67 || res.ExactMatches != 1 {
		t.Fatalf("partial overlap: want 66.67, got %v (%+v)", res.Points, res)
	}

	// Same group, different keys: an interest itself does not match, so
	// the percent is 0 again (the shared group stays in the breakdown).
	if _, _, err := d.DeleteUserInterests(ctx, "0xroom", "0xb", []int{7}); err != nil {
		t.Fatalf("delete interests: %v", err)
	}
	if _, err := d.AddUserInterests(ctx, "0xroom", "0xb",
		[]model.Interest{{Title: "Go", Group: 1, Key: 9}}); err != nil {
		t.Fatalf("add interests: %v", err)
	}
	res, err = d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.Points != 0 || res.SharedGroups != 1 || res.ExactMatches != 0 {
		t.Fatalf("group-only overlap: want 0, got %v (%+v)", res.Points, res)
	}
}

func TestMatchPointsV2HandshakesAreInformational(t *testing.T) {
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

	// Handshakes weigh NOTHING: even a mutual pair with no interests stays
	// at 0 (the flags stay informational).
	res, err := d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.Points != 0 || !res.MutualHandshake || !res.Handshake {
		t.Fatalf("mutual handshake must not change the score: %+v", res)
	}
}

func TestMatchPointsV2Stable(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ctx := context.Background()

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
	if first.Points != 100 {
		t.Fatalf("identical single interests: want 100, got %v", first.Points)
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
