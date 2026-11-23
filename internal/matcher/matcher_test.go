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

func TestWorkerSendsBestPairs(t *testing.T) {
	database := matchTestDB(t)
	ctx := context.Background()

	room := model.Room{Name: "Event"}
	if err := database.CreateRoom(ctx, &room, ""); err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := database.CreateRoomInvite(ctx, room.Address, time.Now().Add(db.DefaultInviteExpiry)); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	// Five personas with a clean interest ladder, all backdated to the same
	// creation moment so every sender's t is equal and the pair ranking is
	// driven by C alone: alice-bob grade C=1 (identical sets), the
	// alice/bob-carol pairs C=0.8, carol-dave C=0.667, alice/bob-dave 0.5,
	// everything with erin 0. The budget for 5 users is 4 pairs
	// (2 * floor(sqrt(5))).
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
	backdateRoomUsers(t, database, room.Address, 10)
	// Carol's clock runs an hour behind: without it the C=0.8 pairs of
	// alice/bob/carol tie exactly and the address tiebreak would pick the
	// senders randomly. With it, job 1 is fully score-determined.
	stamp9 := time.Now().UTC().Add(-9 * time.Hour)
	if _, err := database.ExecContext(ctx,
		`UPDATE users SET created_at = ? WHERE room = ? AND name = 'carol'`,
		stamp9.Format(time.RFC3339), room.Address); err != nil {
		t.Fatalf("backdate carol: %v", err)
	}

	w := New(database, slog.New(slog.NewTextHandler(&testSink{}, nil)))
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("run once: %v", err)
	}

	// Job 1, budget 4 (2 * floor(sqrt(5))): the C=1 pair both ways
	// (a->b, b->a) and the two C=0.8 pairs sent by the 10h-old senders
	// (a->c, b->c) — carol's own pairs grade lower on her younger clock.
	all := roomPairs(t, database, room.Address)
	if len(all) != 4 {
		t.Fatalf("job 1 must send exactly 4 handshakes, got %d: %v", len(all), all)
	}
	ab := map[string]bool{
		addr["alice"] + "->" + addr["bob"]:   true,
		addr["bob"] + "->" + addr["alice"]:   true,
		addr["alice"] + "->" + addr["carol"]: true,
		addr["bob"] + "->" + addr["carol"]:   true,
	}
	for _, p := range all {
		key := p.FromAddress + "->" + p.ToAddress
		if !ab[key] {
			t.Fatalf("job 1 sent an unexpected pair %s (all: %v)", key, all)
		}
	}
	if out := outgoing(t, database, addr["erin"]); len(out) != 0 {
		t.Fatalf("erin shares nothing and must stay out of job 1, got %v", out)
	}

	// Job 2 runs immediately: alice's and bob's clocks were reset by their
	// job-1 outreach (t ~ 0 zeroes the exponent), while carol and dave
	// never sent — so the pool re-ranks around them and the budget of 4
	// goes to carol -> {alice, bob, dave} and dave -> carol.
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("run twice: %v", err)
	}
	if out := outgoing(t, database, addr["carol"]); len(out) != 3 ||
		!out[addr["alice"]] || !out[addr["bob"]] || !out[addr["dave"]] {
		t.Fatalf("job 2 must connect carol to alice, bob and dave, got %v", out)
	}
	if out := outgoing(t, database, addr["dave"]); len(out) != 1 || !out[addr["carol"]] {
		t.Fatalf("job 2 must send dave -> carol, got %v", out)
	}
	if out := outgoing(t, database, addr["alice"]); len(out) != 2 {
		t.Fatalf("alice must not send again right after job 1, got %v", out)
	}
	if out := outgoing(t, database, addr["bob"]); len(out) != 2 {
		t.Fatalf("bob must not send again right after job 1, got %v", out)
	}

	// Simulate an hour passing (every sent handshake backdated), then job
	// 3: the existing pairs are skipped, so the budget goes to the best
	// NEW pairs — erin (never sent, t = 10h from creation, C = 0 grades
	// 88.8) outranks the C=0.5 pairs of the 1h-old senders (85.2) and
	// connects to everyone.
	backdateRoomHandshakes(t, database, room.Address, 1)
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("run thrice: %v", err)
	}
	out := outgoing(t, database, addr["erin"])
	if len(out) != 4 {
		t.Fatalf("job 3 must send erin -> everyone, got %v", out)
	}
	for _, name := range []string{"alice", "bob", "carol", "dave"} {
		if !out[addr[name]] {
			t.Fatalf("erin must handshake %s in job 3, sent %v", name, out)
		}
	}
}

// countOutgoing counts the handshakes one user sent inside one room.
func countOutgoing(t *testing.T, database *db.DB, room, user string) int {
	t.Helper()
	rows, err := database.ListUserHandshakes(context.Background(), user)
	if err != nil {
		t.Fatalf("list handshakes: %v", err)
	}
	n := 0
	for _, hs := range rows {
		if hs.FromAddress == user && hs.Room == room {
			n++
		}
	}
	return n
}

// roomPairs lists every handshake of one room.
func roomPairs(t *testing.T, database *db.DB, room string) []model.Handshake {
	t.Helper()
	var out []model.Handshake
	rows, err := database.QueryContext(context.Background(),
		`SELECT tx_id, from_address, to_address FROM handshakes
		 WHERE room = ? AND from_address != ?`, room, room)
	if err != nil {
		t.Fatalf("list room handshakes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hs model.Handshake
		if err := rows.Scan(&hs.TxID, &hs.FromAddress, &hs.ToAddress); err != nil {
			t.Fatalf("scan handshake: %v", err)
		}
		out = append(out, hs)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list room handshakes: %v", err)
	}
	return out
}

// backdateRoomUsers sets every user's created_at of the room to now -
// hours (the match formula's t falls back to that column when the user
// never sent a handshake).
func backdateRoomUsers(t *testing.T, database *db.DB, room string, hours float64) {
	t.Helper()
	stamp := time.Now().UTC().Add(-time.Duration(hours * float64(time.Hour)))
	if _, err := database.ExecContext(context.Background(),
		`UPDATE users SET created_at = ? WHERE room = ?`,
		stamp.Format(time.RFC3339), room); err != nil {
		t.Fatalf("backdate users: %v", err)
	}
}

// backdateRoomHandshakes sets every handshake's created_at of the room to
// now - hours (the match formula's t reads the sender's newest one).
func backdateRoomHandshakes(t *testing.T, database *db.DB, room string, hours float64) {
	t.Helper()
	stamp := time.Now().UTC().Add(-time.Duration(hours * float64(time.Hour)))
	if _, err := database.ExecContext(context.Background(),
		`UPDATE handshakes SET created_at = ? WHERE room = ?`,
		stamp.Format(time.RFC3339), room); err != nil {
		t.Fatalf("backdate handshakes: %v", err)
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
