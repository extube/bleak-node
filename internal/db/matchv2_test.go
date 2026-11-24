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

	// Identical interest sets, one mutual group: mutualTotal = 4, so
	// 2 * 2 / (4 + 1) = 0.8 -> 80 points (issue #53: the +1 damper keeps
	// identical sets close to, but not at, the full 100).
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
	if res.Points != 80 || res.ExactMatches != 2 {
		t.Fatalf("identical sets: want 80, got %v (%+v)", res.Points, res)
	}

	// Partial overlap: A {7, 8}, B {7} -> mutualTotal = 3, so
	// 2 * 1 / (3 + 1) -> 50.
	if _, _, err := d.DeleteUserInterests(ctx, "0xroom", "0xb", []int{8}); err != nil {
		t.Fatalf("delete interests: %v", err)
	}
	res, err = d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.Points != 50 || res.ExactMatches != 1 {
		t.Fatalf("partial overlap: want 50, got %v (%+v)", res.Points, res)
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
	if first.Points != 66.67 {
		t.Fatalf("identical single interests: want 66.67 (2 * 1 / (2 + 1)), got %v", first.Points)
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

// TestMatchPointsV2MutualGroupsOnly reproduces the owner's example
// (issue #53): user1 with 3×A + 2×B + 1×C and user2 with 7×A + 1×B + 3×D
// count ONLY their A+B interests in the denominator (5 + 8 = 13), because
// C belongs to user1 alone and D to user2 alone. With two exact matches
// (one in A, one in B): 100 * 2 * 2 / (13 + 1) = 28.57.
func TestMatchPointsV2MutualGroupsOnly(t *testing.T) {
	d := openTestDB(t)
	seedPairFixture(t, d, "0xchat")
	ctx := context.Background()

	add := func(addr string, group int, keys ...int) {
		interests := make([]model.Interest, 0, len(keys))
		for _, key := range keys {
			interests = append(interests, model.Interest{Title: "x", Group: group, Key: key})
		}
		if _, err := d.AddUserInterests(ctx, "0xroom", addr, interests); err != nil {
			t.Fatalf("add interests %s/%d: %v", addr, group, err)
		}
	}
	// user1: 3×A, 2×B, 1×C.
	add("0xa", 1, 101, 102, 103)
	add("0xa", 2, 201, 202)
	add("0xa", 3, 301)
	// user2: 7×A (the 101 matches user1's), 1×B (the 202 matches), 3×D.
	add("0xb", 1, 101, 111, 112, 113, 114, 115, 116)
	add("0xb", 2, 202)
	add("0xb", 4, 401, 402, 403)

	res, err := d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.ExactMatches != 2 || res.SharedGroups != 2 {
		t.Fatalf("breakdown wrong: %+v", res)
	}
	if res.Points != 28.57 {
		t.Fatalf("mutual-groups-only: want 28.57 (100 * 2 * 2 / (13 + 1)), got %v", res.Points)
	}

	// No mutual group at all grades 0, even with interests on both sides.
	if _, _, err := d.DeleteUserInterests(ctx, "0xroom", "0xb", []int{101, 111, 112, 113, 114, 115, 116, 202}); err != nil {
		t.Fatalf("delete interests: %v", err)
	}
	add("0xb", 4, 404)
	res, err = d.MatchPointsV2(ctx, "0xroom", "0xa", "0xb")
	if err != nil {
		t.Fatalf("match v2: %v", err)
	}
	if res.Points != 0 || res.SharedGroups != 0 {
		t.Fatalf("no mutual group: want 0, got %v (%+v)", res.Points, res)
	}
}
