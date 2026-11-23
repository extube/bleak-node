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

	// Job 1, budget 2 (floor(sqrt(5))): the C=1 pair both ways (a->b,
	// b->a) — the two best pairs of the pool, scored on the 10h-old
	// sender clocks.
	all := roomPairs(t, database, room.Address)
	if len(all) != 2 {
		t.Fatalf("job 1 must send exactly 2 handshakes, got %d: %v", len(all), all)
	}
	ab := map[string]bool{
		addr["alice"] + "->" + addr["bob"]: true,
		addr["bob"] + "->" + addr["alice"]: true,
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

	// Job 2 runs immediately: the budget of 2 (floor(sqrt(5))) was spent
	// on the C=1 pair AND the room cap (5^2 / 10 = 2) is now reached —
	// the auto-matcher skips the room and sends nothing more.
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("run twice: %v", err)
	}
	if out := outgoing(t, database, addr["carol"]); len(out) != 0 {
		t.Fatalf("the capped room must be skipped in job 2, carol sent %v", out)
	}
	if out := outgoing(t, database, addr["alice"]); len(out) != 1 {
		t.Fatalf("alice must hold only her job-1 send, got %v", out)
	}
	if out := outgoing(t, database, addr["bob"]); len(out) != 1 {
		t.Fatalf("bob must hold only his job-1 send, got %v", out)
	}
	if out := outgoing(t, database, addr["erin"]); len(out) != 0 {
		t.Fatalf("erin must stay out, got %v", out)
	}

	// Even with simulated time passing, the capped room stays skipped.
	backdateRoomHandshakes(t, database, room.Address, 1)
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("run thrice: %v", err)
	}
	if out := outgoing(t, database, addr["dave"]); len(out) != 0 {
		t.Fatalf("the capped room must be skipped in job 3, dave sent %v", out)
	}
	if out := outgoing(t, database, addr["erin"]); len(out) != 0 {
		t.Fatalf("the capped room must be skipped in job 3, erin sent %v", out)
	}
	if all := roomPairs(t, database, room.Address); len(all) != 2 {
		t.Fatalf("the capped room must hold exactly its 2 handshakes, got %v", all)
	}
}

// TestWorkerStopsAtRoomCap verifies the cap check directly: a room whose
// handshakes already reached N^2/10 of its active users is skipped on the
// first job (three users cap at 0, so any handshake count saturates it).
func TestWorkerStopsAtRoomCap(t *testing.T) {
	database := matchTestDB(t)
	ctx := context.Background()

	room := model.Room{Name: "Capped"}
	if err := database.CreateRoom(ctx, &room, ""); err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := database.CreateRoomInvite(ctx, room.Address, time.Now().Add(db.DefaultInviteExpiry)); err != nil {
		t.Fatalf("create invite: %v", err)
	}
	// Three active users cap the room at 0 (3^2/10); the pair
	// alice <-> bob already carries both directions.
	addr := map[string]string{}
	for _, name := range []string{"alice", "bob", "carol"} {
		u := model.User{Name: name}
		if err := database.CreateUser(ctx, room.Address, &u, ""); err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		addr[name] = u.Address
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i, dir := range []struct{ from, to string }{
		{addr["alice"], addr["bob"]}, {addr["bob"], addr["alice"]},
	} {
		if _, err := database.ExecContext(ctx,
			`INSERT INTO handshakes (tx_id, from_address, to_address, room, status, value, created_at)
			 VALUES (?, ?, ?, ?, 1, 1, ?)`,
			"0xtx"+string(rune('a'+i)), dir.from, dir.to, room.Address, now); err != nil {
			t.Fatalf("seed handshake: %v", err)
		}
	}

	w := New(database, slog.New(slog.NewTextHandler(testSink{}, nil)))
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("run once: %v", err)
	}
	// The cap (2) is already reached: no auto sends at all — the remaining
	// pairs (alice->carol, carol->alice, bob->carol, carol->bob) stay
	// untouched by the worker.
	if out := outgoing(t, database, addr["alice"]); len(out) != 1 || !out[addr["bob"]] {
		t.Fatalf("capped room must not auto-send, alice sent %v", out)
	}
	if out := outgoing(t, database, addr["bob"]); len(out) != 1 || !out[addr["alice"]] {
		t.Fatalf("capped room must not auto-send, bob sent %v", out)
	}
	if out := outgoing(t, database, addr["carol"]); len(out) != 0 {
		t.Fatalf("capped room must not auto-send, carol sent %v", out)
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

	// Two active users cap the room at 0 (2^2 / 10) — the auto-matcher
	// skips it entirely, even with an unused pair budget (floor(sqrt(2))).
	// Manual sends through the API stay open, but the test does not touch
	// them.
	w := New(database, slog.New(slog.NewTextHandler(testSink{}, nil)))
	if err := w.RunOnce(ctx); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if out := outgoing(t, database, pool[0].Address); len(out) != 0 {
		t.Fatalf("inactive user must receive no handshakes, sent %v", out)
	}
	if out := outgoing(t, database, pool[1].Address); len(out) != 0 {
		t.Fatalf("the cap-0 room must be skipped, bob sent %v", out)
	}
	if out := outgoing(t, database, pool[2].Address); len(out) != 0 {
		t.Fatalf("the cap-0 room must be skipped, carol sent %v", out)
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
