package matcher

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/extube/bleak-node/internal/db"
	"github.com/extube/bleak-node/internal/model"
)

// matchTestDB opens a database and returns it with a fresh context.
func matchTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "matcher.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// outgoing returns the addresses user sent a handshake to.
func outgoing(t *testing.T, database *db.DB, user string) map[string]bool {
	t.Helper()
	rows, err := database.ListUserHandshakes(context.Background(), user)
	if err != nil {
		t.Fatalf("list handshakes: %v", err)
	}
	out := map[string]bool{}
	for _, hs := range rows {
		if hs.FromAddress == user {
			out[hs.ToAddress] = true
		}
	}
	return out
}

func TestWorkerMatchesTopThree(t *testing.T) {
	database := matchTestDB(t)
	ctx := context.Background()

	room := model.Room{Name: "Event"}
	if err := database.CreateRoom(ctx, &room, ""); err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := database.CreateRoomInvite(ctx, room.Address, time.Now().Add(db.DefaultInviteExpiry)); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	// Five personas; Alice's exact-interest overlap ranks the others:
	// Bob (1 of 1), Carol (1 of 2), Dave (1 of 3), Erin (nothing).
	users := map[string][]int{
		"alice": {1, 2, 3},
		"bob":   {1, 2, 3},
		"carol": {1, 2},
		"dave":  {1},
		"erin":  {},
	}
	addr := map[string]string{}
	for _, name := range []string{"alice", "bob", "carol", "dave", "erin"} {
		u := model.User{Name: name}
		if err := database.CreateUser(ctx, room.Address, &u, ""); err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		addr[name] = u.Address
		for _, key := range users[name] {
			if _, err := database.AddUserInterests(ctx, room.Address, u.Address,
				[]model.Interest{{Title: "x", Group: 1, Key: key}}); err != nil {
				t.Fatalf("add interest: %v", err)
			}
		}
	}

	w := New(database, slog.New(slog.NewTextHandler(&testSink{}, nil)))
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("run once: %v", err)
	}

	// Job 1: exactly the top-3 get handshakes. The exact trio is
	// deliberately not pinned: every handshake direction (an incoming one
	// included) adds +1 to the score's C, so the ranking of a row depends
	// on who was processed (and shook hands) before — e.g. erin, sharing
	// nothing, can tie bob at 50 points by sending first. The stable
	// contract is the COUNT here and FULL connectivity after job 2.
	sent := outgoing(t, database, addr["alice"])
	if len(sent) != 3 {
		t.Fatalf("job 1 must send exactly 3 handshakes for alice, got %v", sent)
	}
	if sent[addr["alice"]] {
		t.Fatalf("alice must not handshake herself: %v", sent)
	}

	// Job 2: the already-shaken pairs are skipped in favor of new ones, so
	// alice ends up connected to every other active user.
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("run twice: %v", err)
	}
	sent = outgoing(t, database, addr["alice"])
	if len(sent) != 4 {
		t.Fatalf("job 2 must connect alice to everyone, got %v", sent)
	}
	for _, name := range []string{"bob", "carol", "dave", "erin"} {
		if !sent[addr[name]] {
			t.Fatalf("alice must handshake %s after job 2, sent %v", name, sent)
		}
	}
}

func TestWorkerSkipsInactiveUsersAndIdleRooms(t *testing.T) {
	database := matchTestDB(t)
	ctx := context.Background()

	live := model.Room{Name: "Live"}
	if err := database.CreateRoom(ctx, &live, ""); err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := database.CreateRoomInvite(ctx, live.Address, time.Now().Add(db.DefaultInviteExpiry)); err != nil {
		t.Fatalf("create invite: %v", err)
	}
	// An expired and a revoked code do not keep a room in the pool.
	if _, err := database.CreateRoomInvite(ctx, live.Address, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if _, err := database.CreateRoomInvite(ctx, live.Address, time.Now().Add(db.DefaultInviteExpiry)); err != nil {
		t.Fatalf("create invite: %v", err)
	}
	rows, err := database.ListRoomInvites(ctx, live.Address)
	if err != nil || len(rows) < 2 {
		t.Fatalf("seed invites: %v (%d)", err, len(rows))
	}
	if err := database.RevokeRoomInvite(ctx, live.Address, rows[len(rows)-1].Code); err != nil {
		t.Fatalf("revoke invite: %v", err)
	}

	idle := model.Room{Name: "Idle"}
	if err := database.CreateRoom(ctx, &idle, ""); err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := database.CreateRoomInvite(ctx, idle.Address, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	rooms, err := database.ListRoomsWithActiveInvites(ctx)
	if err != nil {
		t.Fatalf("list rooms: %v", err)
	}
	if len(rooms) != 1 || rooms[0].Address != live.Address {
		t.Fatalf("only the room with a live invite must be listed, got %+v", rooms)
	}

	var pool []model.User
	for _, name := range []string{"alice", "bob", "carol"} {
		u := model.User{Name: name}
		if err := database.CreateUser(ctx, live.Address, &u, ""); err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		pool = append(pool, u)
	}
	// alice opts out: the worker must not send her handshakes nor rank her.
	if _, err := database.UpdateUser(ctx, live.Address, pool[0].Address, nil, nil, nil, boolPtr(false)); err != nil {
		t.Fatalf("deactivate alice: %v", err)
	}
	active, err := database.ListActiveRoomUsers(ctx, live.Address)
	if err != nil {
		t.Fatalf("list active users: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("want 2 active users, got %d", len(active))
	}

	w := New(database, slog.New(slog.NewTextHandler(testSink{}, nil)))
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if out := outgoing(t, database, pool[0].Address); len(out) != 0 {
		t.Fatalf("inactive user must receive no handshakes, sent %v", out)
	}
	// bob and carol rank each other (no interests anywhere): both directions.
	if out := outgoing(t, database, pool[1].Address); len(out) != 1 || !out[pool[2].Address] {
		t.Fatalf("bob must handshake carol, got %v", out)
	}
	if out := outgoing(t, database, pool[2].Address); len(out) != 1 || !out[pool[1].Address] {
		t.Fatalf("carol must handshake bob, got %v", out)
	}
	// The idle room is untouched: its (nonexistent) users were never read.
	var count int
	if err := database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM handshakes WHERE room = ?`, idle.Address).Scan(&count); err != nil {
		t.Fatalf("count idle handshakes: %v", err)
	}
	if count != 0 {
		t.Fatalf("idle room must stay untouched, got %d handshakes", count)
	}
}

func boolPtr(b bool) *bool { return &b }

// testSink is a log sink passing through to stderr (verbose test runs).
type testSink struct{}

func (testSink) Write(p []byte) (int, error) { return os.Stderr.Write(p) }
