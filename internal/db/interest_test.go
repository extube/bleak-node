package db

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"

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
	if _, err := db.MatchPoints(ctx, room, alice, alice, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same addresses must be ErrInvalid, got %v", err)
	}
	if _, err := db.MatchPoints(ctx, room, alice, bob, -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative time must be ErrInvalid, got %v", err)
	}
	if _, err := db.MatchPoints(ctx, room, alice, "0xunknown", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown match user must be ErrNotFound, got %v", err)
	}
}

func TestMatchPointsFormula(t *testing.T) {
	db := interestTestDB(t)
	ctx := context.Background()
	room, alice, bob := seedInterestUsers(t, db)

	// t = 1h, no handshake, no interests: 50 + 50 * 0.5 = 75.
	res, err := db.MatchPoints(ctx, room, alice, bob, 3_600_000)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points != 75 {
		t.Fatalf("want 75, got %v (%+v)", res.Points, res)
	}
	if res.MutualHandshake || res.Handshake || res.ExactMatches != 0 || res.SharedGroups != 0 {
		t.Fatalf("unexpected breakdown: %+v", res)
	}

	// Shared group only: 75 + 2 = 77.
	if _, err := db.AddUserInterests(ctx, room, alice, []model.Interest{{Title: "Chess", Group: 1, Key: 7}}); err != nil {
		t.Fatalf("add A: %v", err)
	}
	if _, err := db.AddUserInterests(ctx, room, bob, []model.Interest{{Title: "Go", Group: 1, Key: 8}}); err != nil {
		t.Fatalf("add B: %v", err)
	}
	res, err = db.MatchPoints(ctx, room, alice, bob, 3_600_000)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points != 77 || res.SharedGroups != 1 || res.ExactMatches != 0 {
		t.Fatalf("shared group: want 77, got %v (%+v)", res.Points, res)
	}

	// Exact match (same group AND key): 75 + 2 + 5 = 82.
	if _, err := db.AddUserInterests(ctx, room, bob, []model.Interest{{Title: "Chess", Group: 1, Key: 7}}); err != nil {
		t.Fatalf("add B exact: %v", err)
	}
	res, err = db.MatchPoints(ctx, room, alice, bob, 3_600_000)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points != 82 || res.ExactMatches != 1 || res.SharedGroups != 1 {
		t.Fatalf("exact match: want 82, got %v (%+v)", res.Points, res)
	}

	// Zero time decays to the full base (100): the clamp holds at 100 even
	// with the interest coefficient on top.
	res, err = db.MatchPoints(ctx, room, alice, bob, 0)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points != 100 {
		t.Fatalf("t=0 must clamp to 100, got %v", res.Points)
	}

	// Very long time: the base decays to 50, the interest coefficient
	// keeps 50 + 2 + 5 = 57.
	res, err = db.MatchPoints(ctx, room, alice, bob, 360_000_000_000_000)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points != 57 {
		t.Fatalf("long time: want 57, got %v", res.Points)
	}

	// One-sided handshake: +4 (the directions are in the room).
	if _, err := db.CreateHandshake(ctx, &model.Handshake{
		FromAddress: alice, ToAddress: bob, Status: model.DefaultStatus,
	}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	res, err = db.MatchPoints(ctx, room, alice, bob, 3_600_000)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if !res.Handshake || res.MutualHandshake {
		t.Fatalf("unexpected handshake flags: %+v", res)
	}
	if res.Points != 86 {
		t.Fatalf("one-sided handshake: want 86, got %v", res.Points)
	}

	// Mutual handshake: instant 100, whatever the time.
	if _, err := db.CreateHandshake(ctx, &model.Handshake{
		FromAddress: bob, ToAddress: alice, Status: model.DefaultStatus,
	}); err != nil {
		t.Fatalf("handshake back: %v", err)
	}
	res, err = db.MatchPoints(ctx, room, alice, bob, 360_000_000_000) // 100h
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Points != 100 || !res.MutualHandshake {
		t.Fatalf("mutual handshake: want instant 100, got %v (%+v)", res.Points, res)
	}
}

func TestMatchPointsClamp(t *testing.T) {
	db := interestTestDB(t)
	ctx := context.Background()
	room, alice, bob := seedInterestUsers(t, db)

	// Many exact matches + shared groups + handshake push the raw value far
	// above 100: the clamp holds it at 100.
	for k := 0; k < 30; k++ {
		if _, err := db.AddUserInterests(ctx, room, alice, []model.Interest{{Title: "A", Group: k, Key: k}}); err != nil {
			t.Fatalf("add A: %v", err)
		}
		if _, err := db.AddUserInterests(ctx, room, bob, []model.Interest{{Title: "B", Group: k, Key: k}}); err != nil {
			t.Fatalf("add B: %v", err)
		}
	}
	res, err := db.MatchPoints(ctx, room, alice, bob, 3_600_000)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if math.Round(res.Points) != 100 {
		t.Fatalf("clamp: want 100, got %v", res.Points)
	}
	if res.ExactMatches != 30 || res.SharedGroups != 30 {
		t.Fatalf("breakdown: %+v", res)
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
