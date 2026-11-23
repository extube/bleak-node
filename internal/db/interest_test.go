package db

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// interestTestDB opens a database and seeds one room (owner token ignored)
// with two users through the production insert paths.
func interestTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "interest.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	room := model.Room{Name: "Test Room"}
	if err := database.CreateRoom(ctx, &room, ""); err != nil {
		t.Fatalf("create room: %v", err)
	}
	for _, name := range []string{"Alice", "Bob"} {
		u := model.User{Name: name}
		if err := database.CreateUser(ctx, room.Address, &u, ""); err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
	}
	return database
}

func TestAddAndDeleteUserInterests(t *testing.T) {
	db := interestTestDB(t)
	ctx := context.Background()

	room, alice, bob := seedInterestUsers(t, db)
	_ = bob

	added, err := db.AddUserInterests(ctx, room, alice, []model.Interest{
		{Title: "Chess", Group: 1, Key: 7},
		{Title: "Go", Group: 1, Key: 8},
		{Title: "Guitar", Group: 2, Key: 3},
	})
	if err != nil {
		t.Fatalf("add interests: %v", err)
	}
	if len(added) != 3 {
		t.Fatalf("want 3 interests, got %d", len(added))
	}

	// Upsert by key: re-adding key 7 replaces the title, order stays.
	added, err = db.AddUserInterests(ctx, room, alice, []model.Interest{
		{Title: "Blitz chess", Group: 1, Key: 7},
	})
	if err != nil {
		t.Fatalf("re-add interests: %v", err)
	}
	if len(added) != 3 {
		t.Fatalf("upsert must not duplicate: got %d rows", len(added))
	}
	if added[0].Title != "Blitz chess" || added[0].Key != 7 {
		t.Fatalf("upsert did not replace title: %+v", added[0])
	}

	removed, remaining, err := db.DeleteUserInterests(ctx, room, alice, []int{7, 99})
	if err != nil {
		t.Fatalf("delete interests: %v", err)
	}
	if removed != 1 {
		t.Fatalf("want 1 removed, got %d", removed)
	}
	if len(remaining) != 2 {
		t.Fatalf("want 2 remaining, got %d", len(remaining))
	}

	// Unknown keys only: nothing removed, no error.
	removed, remaining, err = db.DeleteUserInterests(ctx, room, alice, []int{99})
	if err != nil || removed != 0 || len(remaining) != 2 {
		t.Fatalf("idempotent delete: %v removed=%d remaining=%d", err, removed, len(remaining))
	}
}

func TestInterestValidation(t *testing.T) {
	db := interestTestDB(t)
	ctx := context.Background()
	room, alice, bob := seedInterestUsers(t, db)

	if _, err := db.AddUserInterests(ctx, room, alice, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty list must be ErrInvalid, got %v", err)
	}
	if _, err := db.AddUserInterests(ctx, room, alice, []model.Interest{{Title: "", Group: 1, Key: 1}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty title must be ErrInvalid, got %v", err)
	}
	if _, err := db.AddUserInterests(ctx, room, alice, []model.Interest{{Title: "x", Group: -1, Key: 1}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative group must be ErrInvalid, got %v", err)
	}
	// Unknown user / wrong room are ErrNotFound.
	if _, err := db.AddUserInterests(ctx, room, "0xunknown", []model.Interest{{Title: "x", Group: 1, Key: 1}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user must be ErrNotFound, got %v", err)
	}
	if _, _, err := db.DeleteUserInterests(ctx, room, bob, []int{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty keys must be ErrInvalid, got %v", err)
	}
	if _, err := db.MatchPoints(ctx, room, alice, alice); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same addresses must be ErrInvalid, got %v", err)
	}
	if _, err := db.MatchPoints(ctx, room, alice, "0xunknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown match user must be ErrNotFound, got %v", err)
	}
	if _, err := db.MatchPoints(ctx, room, "0xunknown", bob); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown first match user must be ErrNotFound, got %v", err)
	}
}

// backdateUser sets a user's created_at to now - hours (the formula's t
// falls back to that column when the user never sent a handshake).
func backdateUser(t *testing.T, db *DB, address string, hours float64) {
	t.Helper()
	created := time.Now().UTC().Add(-time.Duration(hours * float64(time.Hour)))
	if _, err := db.ExecContext(context.Background(),
		`UPDATE users SET created_at = ? WHERE address = ?`,
		created.Format(time.RFC3339), address); err != nil {
		t.Fatalf("backdate user: %v", err)
	}
}

// seedHandshake creates a directed handshake and backdates it to now -
// hours (the formula's t reads the sender's newest handshake timestamp).
func seedHandshake(t *testing.T, db *DB, from, to string, hours float64) {
	t.Helper()
	if _, err := db.CreateHandshake(context.Background(), &model.Handshake{
		FromAddress: from, ToAddress: to, Status: model.DefaultStatus,
	}); err != nil {
		t.Fatalf("handshake %s->%s: %v", from, to, err)
	}
	sent := time.Now().UTC().Add(-time.Duration(hours * float64(time.Hour)))
	if _, err := db.ExecContext(context.Background(),
		`UPDATE handshakes SET created_at = ? WHERE from_address = ? AND to_address = ?`,
		sent.Format(time.RFC3339), from, to); err != nil {
		t.Fatalf("backdate handshake: %v", err)
	}
}

// thirdUser creates a third persona in the room (a handshake target that
// leaves the alice-bob relation coefficient untouched).
func thirdUser(t *testing.T, db *DB, room, name string) string {
	t.Helper()
	u := model.User{Name: name}
	if err := db.CreateUser(context.Background(), room, &u, ""); err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	return u.Address
}

func TestMatchPointsFormula(t *testing.T) {
	db := interestTestDB(t)
	ctx := context.Background()
	room, alice, bob := seedInterestUsers(t, db)
	carol := thirdUser(t, db, room, "Carol")

	// Brand-new personas, no handshake, no interests: C = 0 and t ~ 0 ->
	// sqrt(0) = 0 -> 2^0 = 1 -> points ~ 0 (sqrt-amplified tiny clocks
	// read under one point).
	res, err := db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points < 0 || res.Points > 2 {
		t.Fatalf("fresh pair must score ~0, got %v (%+v)", res.Points, res)
	}
	if res.MutualHandshake || res.Handshake || res.ExactMatches != 0 || res.SharedGroups != 0 {
		t.Fatalf("unexpected breakdown: %+v", res)
	}

	// Still nothing shared an hour later: C = 0, so the pair grows on age
	// alone — t = 1 -> sqrt(1) = 1 -> 100 * (1 - 1/2) = 50.
	backdateUser(t, db, alice, 1)
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Abs(res.Points-50) > 0.05 {
		t.Fatalf("age-only pair (C=0): want ~50, got %v (%+v)", res.Points, res)
	}

	// Shared group only: the coefficient counts exact matches alone, so
	// C = 0 and the score stays 50 — the group is reported in the breakdown.
	if _, err := db.AddUserInterests(ctx, room, alice, []model.Interest{{Title: "Chess", Group: 1, Key: 7}}); err != nil {
		t.Fatalf("add A: %v", err)
	}
	if _, err := db.AddUserInterests(ctx, room, bob, []model.Interest{{Title: "Go", Group: 1, Key: 8}}); err != nil {
		t.Fatalf("add B: %v", err)
	}
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Abs(res.Points-50) > 0.05 || res.SharedGroups != 1 || res.ExactMatches != 0 {
		t.Fatalf("shared group only: want ~50 with 1 group, got %v (%+v)", res.Points, res)
	}

	// Exact match (same group AND key): matched/total = 1/3, no handshakes
	// (factor 2) -> C = 2/3; t = 1 (creation fallback) ->
	// sqrt(1 + 2/3) = 1.291 -> 100 * (1 - 1/2^1.291) = 59.13.
	if _, err := db.AddUserInterests(ctx, room, bob, []model.Interest{{Title: "Chess", Group: 1, Key: 7}}); err != nil {
		t.Fatalf("add B exact: %v", err)
	}
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Abs(res.Points-59.13) > 0.05 || res.ExactMatches != 1 || res.SharedGroups != 1 {
		t.Fatalf("exact match: want ~59.13, got %v (%+v)", res.Points, res)
	}

	// One-sided handshake FROM alice (sent 1h ago; the carol handshake
	// keeps bob's pair the only scored one): H_s = 1, H_r = 0 ->
	// C = 3 * 1/3 = 1 -> sqrt(1 + 1) = 1.414 -> 62.48.
	seedHandshake(t, db, alice, carol, 1)
	seedHandshake(t, db, alice, bob, 1)
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Abs(res.Points-62.48) > 0.05 || !res.Handshake || res.MutualHandshake {
		t.Fatalf("one-sided handshake: want ~62.48, got %v (%+v)", res.Points, res)
	}

	// Mutual handshake: H_r = 1 doubles the factor -> C = 5 * 1/3 = 5/3 ->
	// sqrt(1 + 5/3) = 1.633 -> 67.76.
	seedHandshake(t, db, bob, alice, 1)
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Abs(res.Points-67.76) > 0.05 || !res.MutualHandshake {
		t.Fatalf("mutual handshake: want ~67.76, got %v (%+v)", res.Points, res)
	}

	// The sqrt growth: backdating ALL of alice's sent handshakes to 4h
	// gives t = 4 -> sqrt(4 + 5/3) = 2.38 -> 80.8 (the expected value is
	// derived from the spec formula to pin the exponent wiring).
	seedHandshake(t, db, alice, carol, 4)
	seedHandshake(t, db, alice, bob, 4)
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	expected := 100 * (1 - 1/math.Pow(2, math.Sqrt(4+5.0/3.0)))
	if math.Abs(res.Points-expected) > 0.05 {
		t.Fatalf("t=4 inside the sqrt: want ~%.2f, got %v (%+v)", expected, res.Points, res)
	}
}

func TestMatchPointsSaturation(t *testing.T) {
	db := interestTestDB(t)
	ctx := context.Background()
	room, alice, bob := seedInterestUsers(t, db)

	// One identical interest on both sides: C = 2 * 1/2 = 1. A long idle
	// time since the last outreach (creation fallback: none was sent)
	// saturates the sqrt growth at 100.
	backdateUser(t, db, alice, 4000)
	if _, err := db.AddUserInterests(ctx, room, alice, []model.Interest{{Title: "Chess", Group: 1, Key: 7}}); err != nil {
		t.Fatalf("add A: %v", err)
	}
	if _, err := db.AddUserInterests(ctx, room, bob, []model.Interest{{Title: "Chess", Group: 1, Key: 7}}); err != nil {
		t.Fatalf("add B: %v", err)
	}
	res, err := db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points != 100 {
		t.Fatalf("long-idle related pair: want 100, got %v", res.Points)
	}

	// The other side's clock does not matter: t comes from the FIRST user
	// only. The mirrored call with a fresh first user reads t ~ 0, but the
	// relation C = 1 floors the summand: sqrt(0 + 1) = 1 -> 50.
	res, err = db.MatchPoints(ctx, room, bob, alice)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Abs(res.Points-50) > 0.05 {
		t.Fatalf("mirrored call must use the first user's clock (C floors at 50): got %v", res.Points)
	}

	// A fresh handshake RESETS the sender's t: right after alice reaches
	// out, her row drops from the saturated 100 back to her relation floor
	// (t ~ 0, C = 1.5 -> sqrt(1.5) = 1.225 -> ~57.2; the "just matched,
	// look elsewhere for now" effect).
	seedHandshake(t, db, alice, bob, 0)
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points < 55 || res.Points > 59 {
		t.Fatalf("fresh outreach must reset t to ~0, got %v", res.Points)
	}
}

// seedInterestUsers returns the room and the two user addresses seeded by
// interestTestDB.
func seedInterestUsers(t *testing.T, db *DB) (room, alice, bob string) {
	t.Helper()
	ctx := context.Background()
	if err := db.QueryRowContext(ctx,
		`SELECT address FROM rooms LIMIT 1`).Scan(&room); err != nil {
		t.Fatalf("seeded room: %v", err)
	}
	users, err := db.ListRoomUsers(ctx, room)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("want 2 seeded users, got %d", len(users))
	}
	return room, users[0].Address, users[1].Address
}
