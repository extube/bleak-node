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

// backdateUser sets a user's created_at to now - hours (the formula's t is
// read from that column).
func backdateUser(t *testing.T, db *DB, address string, hours float64) {
	t.Helper()
	created := time.Now().UTC().Add(-time.Duration(hours * float64(time.Hour)))
	if _, err := db.ExecContext(context.Background(),
		`UPDATE users SET created_at = ? WHERE address = ?`,
		created.Format(time.RFC3339), address); err != nil {
		t.Fatalf("backdate user: %v", err)
	}
}

func TestMatchPointsFormula(t *testing.T) {
	db := interestTestDB(t)
	ctx := context.Background()
	room, alice, bob := seedInterestUsers(t, db)

	// Brand-new personas (t ~ 0), no handshake, no interests: C = 1 and
	// points = 100 * (1 - 1/(1 * 2^t)) ~ 0.
	res, err := db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points < 0 || res.Points > 0.1 {
		t.Fatalf("fresh pair must score ~0, got %v (%+v)", res.Points, res)
	}
	if res.MutualHandshake || res.Handshake || res.ExactMatches != 0 || res.SharedGroups != 0 {
		t.Fatalf("unexpected breakdown: %+v", res)
	}

	// t = 1h (the FIRST persona's age), still no relations: C = 1 gives
	// 100 * (1 - 1/2) = 50.
	backdateUser(t, db, alice, 1)
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Abs(res.Points-50) > 0.05 {
		t.Fatalf("t=1h C=1: want ~50, got %v (%+v)", res.Points, res)
	}

	// Shared group only: the percent counts exact matches alone, so the
	// score stays 50 — the group is reported in the breakdown.
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
		t.Fatalf("shared group: want ~50 with 1 group, got %v (%+v)", res.Points, res)
	}

	// Exact match (same group AND key): percent = 2*1/(1+2) = 2/3,
	// C = 1 + 2/3 -> 100 * (1 - 1/(1.6667 * 2)) = 70.
	if _, err := db.AddUserInterests(ctx, room, bob, []model.Interest{{Title: "Chess", Group: 1, Key: 7}}); err != nil {
		t.Fatalf("add B exact: %v", err)
	}
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Abs(res.Points-70) > 0.05 || res.ExactMatches != 1 || res.SharedGroups != 1 {
		t.Fatalf("exact match: want ~70, got %v (%+v)", res.Points, res)
	}

	// One-sided handshake: +1 direction, C = 1 + 1 + 2/3 -> 81.25.
	if _, err := db.CreateHandshake(ctx, &model.Handshake{
		FromAddress: alice, ToAddress: bob, Status: model.DefaultStatus,
	}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Abs(res.Points-81.25) > 0.05 || !res.Handshake || res.MutualHandshake {
		t.Fatalf("one-sided handshake: want ~81.25, got %v (%+v)", res.Points, res)
	}

	// Mutual handshake: no instant 100 anymore — +1 more direction,
	// C = 1 + 2 + 2/3 -> 100 * (1 - 1/(3.6667 * 2)) = 86.36.
	if _, err := db.CreateHandshake(ctx, &model.Handshake{
		FromAddress: bob, ToAddress: alice, Status: model.DefaultStatus,
	}); err != nil {
		t.Fatalf("handshake back: %v", err)
	}
	res, err = db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Abs(res.Points-86.36) > 0.05 || !res.MutualHandshake {
		t.Fatalf("mutual handshake: want ~86.36, got %v (%+v)", res.Points, res)
	}
}

func TestMatchPointsAgeSaturation(t *testing.T) {
	db := interestTestDB(t)
	ctx := context.Background()
	room, alice, bob := seedInterestUsers(t, db)

	// The age term saturates: after a long time even C = 1 saturates the
	// formula at 100 (2^t overflows the denominator away).
	backdateUser(t, db, alice, 4000)
	res, err := db.MatchPoints(ctx, room, alice, bob)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points != 100 {
		t.Fatalf("old persona: want 100, got %v", res.Points)
	}

	// The younger side's age does not matter: t comes from the FIRST user
	// only, so the mirrored call with a fresh first user scores ~0 again.
	res, err = db.MatchPoints(ctx, room, bob, alice)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points > 0.1 {
		t.Fatalf("mirrored call must use the first user's age, got %v", res.Points)
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
