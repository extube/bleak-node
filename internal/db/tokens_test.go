package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// seedCredWorld creates a room plus n users with persona tokens; returns
// the room and the users in insertion order.
func seedCredWorld(t *testing.T, ctx context.Context, d *DB, n int) (model.Room, []model.User) {
	t.Helper()
	room := model.Room{Name: "Cred Room"}
	if err := d.CreateRoom(ctx, &room, HashToken("room-secret")); err != nil {
		t.Fatalf("create room: %v", err)
	}
	users := make([]model.User, 0, n)
	for i := 0; i < n; i++ {
		u := model.User{Name: "User" + string(rune('A'+i))}
		if err := d.CreateUser(ctx, room.Address, &u, HashToken("secret-"+u.Name)); err != nil {
			t.Fatalf("create user %s: %v", u.Name, err)
		}
		users = append(users, u)
	}
	return room, users
}

// chatCount counts the chats of a user.
func chatCount(t *testing.T, ctx context.Context, d *DB, address string) int {
	t.Helper()
	chats, err := d.ListUserChats(ctx, address)
	if err != nil {
		t.Fatalf("list chats of %s: %v", address, err)
	}
	return len(chats)
}

func TestConsumeMutualCodeLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	_, users := seedCredWorld(t, ctx, d, 3)
	a, b := users[0], users[1] // a = badge owner, b = scanner

	// Register a live one-time code for a.
	if err := d.SetUserMutualCodeHash(ctx, a.Room, a.Address, HashToken("code-1")); err != nil {
		t.Fatalf("register code: %v", err)
	}
	live, err := d.UserMutualCodeLive(ctx, a.Room, a.Address)
	if err != nil || !live {
		t.Fatalf("code should be live: live=%t err=%v", live, err)
	}

	// First scan: both handshakes + the pair chat, atomically.
	res, err := d.ConsumeMutualCode(ctx, b.Address, a.Address, "code-1")
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if !res.ABNew || !res.BANew || !res.ChatFound {
		t.Fatalf("first scan: ABNew=%t BANew=%t ChatFound=%t", res.ABNew, res.BANew, res.ChatFound)
	}
	if res.AB.FromAddress != b.Address || res.AB.ToAddress != a.Address {
		t.Fatalf("AB handshake = %v", res.AB)
	}
	if res.BA.FromAddress != a.Address || res.BA.ToAddress != b.Address {
		t.Fatalf("BA handshake = %v", res.BA)
	}
	if got := chatCount(t, ctx, d, b.Address); got != 1 {
		t.Fatalf("chats of scanner = %d, want 1", got)
	}

	// Burned: a stored hash gone.
	live, err = d.UserMutualCodeLive(ctx, a.Room, a.Address)
	if err != nil || live {
		t.Fatalf("code must be consumed after use: live=%t err=%v", live, err)
	}

	// Replay of the burned code: explicit "already used" (410).
	if _, err := d.ConsumeMutualCode(ctx, b.Address, a.Address, "code-1"); !errors.Is(err, ErrGone) {
		t.Fatalf("replay error = %v, want ErrGone", err)
	}

	// Wrong code while a live one exists: 403, the code stays live.
	if err := d.SetUserMutualCodeHash(ctx, a.Room, a.Address, HashToken("code-2")); err != nil {
		t.Fatalf("register code-2: %v", err)
	}
	if _, err := d.ConsumeMutualCode(ctx, b.Address, a.Address, "nope"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong code error = %v, want ErrForbidden", err)
	}
	live, _ = d.UserMutualCodeLive(ctx, a.Room, a.Address)
	if !live {
		t.Fatal("wrong attempt must not burn the code")
	}

	// Pair already mutual: a successful scan burns the code but never
	// creates a second chat.
	res, err = d.ConsumeMutualCode(ctx, b.Address, a.Address, "code-2")
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if res.ABNew || res.BANew {
		t.Fatal("an existing pair must not create new handshakes")
	}
	if got := chatCount(t, ctx, d, b.Address); got != 1 {
		t.Fatalf("chats after re-scan = %d, want 1 (one chat per pair)", got)
	}
	live, _ = d.UserMutualCodeLive(ctx, a.Room, a.Address)
	if live {
		t.Fatal("the code must burn even when the pair was already mutual")
	}

	// Unknown target user: 404.
	if _, err := d.ConsumeMutualCode(ctx, b.Address, "0xmissing", "code-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown target error = %v, want ErrNotFound", err)
	}
}

func TestMutualIncomeRuleRejectionDoesNotBurn(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	_, users := seedCredWorld(t, ctx, d, 12)
	a, b := users[0], users[1] // a = scanner (will run out of balance), b = target

	if err := d.SetUserMutualCodeHash(ctx, b.Room, b.Address, HashToken("live-code")); err != nil {
		t.Fatalf("register code: %v", err)
	}

	// a spends its welcome balance (+10) on handshakes to 10 others.
	for _, target := range users[2:12] {
		if _, err := d.CreateHandshake(ctx, &model.Handshake{
			FromAddress: a.Address, ToAddress: target.Address, Status: model.DefaultStatus,
		}); err != nil {
			t.Fatalf("handshake a->%s: %v", target.Address, err)
		}
	}
	// Balance is now 0; the mutual A->B direction violates income>outcome.
	if _, err := d.ConsumeMutualCode(ctx, a.Address, b.Address, "live-code"); !errors.Is(err, ErrBalance) {
		t.Fatalf("rejected mutual error = %v, want ErrBalance", err)
	}
	// The code is NOT consumed by a rule rejection.
	live, err := d.UserMutualCodeLive(ctx, b.Room, b.Address)
	if err != nil || !live {
		t.Fatalf("rejection must not burn the code: live=%t err=%v", live, err)
	}
	if got := chatCount(t, ctx, d, a.Address); got != 0 {
		t.Fatalf("chats of scanner = %d, want 0 (nothing was created)", got)
	}

	// Incoming handshakes lift the balance above zero; the scan succeeds.
	if _, err := d.CreateHandshake(ctx, &model.Handshake{
		FromAddress: users[2].Address, ToAddress: a.Address, Status: model.DefaultStatus,
	}); err != nil {
		t.Fatalf("handshake back to a: %v", err)
	}
	res, err := d.ConsumeMutualCode(ctx, a.Address, b.Address, "live-code")
	if err != nil {
		t.Fatalf("mutual after income: %v", err)
	}
	if !res.ABNew || !res.BANew {
		t.Fatalf("expected a fresh pair: ABNew=%t BANew=%t", res.ABNew, res.BANew)
	}
	live, _ = d.UserMutualCodeLive(ctx, b.Room, b.Address)
	if live {
		t.Fatal("successful scan must burn the code")
	}
}

func TestCreateTokenSingleUse(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	raw := "create-token-raw"
	if err := d.MintCreateToken(ctx, HashToken(raw)); err != nil {
		t.Fatalf("mint: %v", err)
	}
	exists, used, err := d.CreateTokenState(ctx, HashToken(raw))
	if err != nil || !exists || used {
		t.Fatalf("state after mint: exists=%t used=%t err=%v", exists, used, err)
	}

	ok, err := d.ConsumeCreateToken(ctx, HashToken(raw))
	if err != nil || !ok {
		t.Fatalf("first use: ok=%t err=%v", ok, err)
	}
	ok, err = d.ConsumeCreateToken(ctx, HashToken(raw))
	if err != nil || ok {
		t.Fatalf("second use: ok=%t err=%v (must be single-use)", ok, err)
	}
	exists, used, err = d.CreateTokenState(ctx, HashToken(raw))
	if err != nil || !exists || !used {
		t.Fatalf("state after use: exists=%t used=%t err=%v", exists, used, err)
	}

	exists, _, err = d.CreateTokenState(ctx, HashToken("unknown-raw"))
	if err != nil || exists {
		t.Fatalf("unknown token: exists=%t err=%v", exists, err)
	}
}

func TestInviteLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	room, _ := seedCredWorld(t, ctx, d, 1)

	// Default mint: multi-use until expiry (24 h).
	inv, err := d.CreateRoomInvite(ctx, room.Address, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("mint invite: %v", err)
	}
	if len(inv.Code) != 6 {
		t.Fatalf("invite code %q is not 6 digits", inv.Code)
	}
	valid, err := d.ValidRoomInvite(ctx, room.Address, inv.Code)
	if err != nil || !valid {
		t.Fatalf("invite should be valid: valid=%t err=%v", valid, err)
	}
	// Multi-use: validation is not consumption.
	valid, _ = d.ValidRoomInvite(ctx, room.Address, inv.Code)
	if !valid {
		t.Fatal("invite codes are multi-use until expiry")
	}

	// List shows only active codes.
	inv2, err := d.CreateRoomInvite(ctx, room.Address, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("mint invite 2: %v", err)
	}
	list, err := d.ListRoomInvites(ctx, room.Address)
	if err != nil || len(list) != 2 {
		t.Fatalf("active invites = %d (err=%v), want 2", len(list), err)
	}

	// Revocation is immediate.
	if err := d.RevokeRoomInvite(ctx, room.Address, inv.Code); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	valid, _ = d.ValidRoomInvite(ctx, room.Address, inv.Code)
	if valid {
		t.Fatal("revoked invite must be invalid")
	}
	list, _ = d.ListRoomInvites(ctx, room.Address)
	if len(list) != 1 || list[0].Code != inv2.Code {
		t.Fatalf("list after revoke = %v, want only %q", list, inv2.Code)
	}
	// Revoking again (or an unknown code of the room) is a 404.
	if err := d.RevokeRoomInvite(ctx, room.Address, inv.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double revoke error = %v, want ErrNotFound", err)
	}

	// Expired invites are invalid.
	expired, err := d.CreateRoomInvite(ctx, room.Address, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("mint expired invite: %v", err)
	}
	valid, _ = d.ValidRoomInvite(ctx, room.Address, expired.Code)
	if valid {
		t.Fatal("expired invite must be invalid")
	}

	// Invite codes of another room do not match.
	other := model.Room{Name: "Other"}
	if err := d.CreateRoom(ctx, &other, ""); err != nil {
		t.Fatalf("create other room: %v", err)
	}
	valid, _ = d.ValidRoomInvite(ctx, other.Address, inv2.Code)
	if valid {
		t.Fatal("a room's invite must not be valid for another room")
	}

	// Global resolution maps the code to its room while valid.
	got, ok, err := d.ResolveRoomInvite(ctx, inv2.Code)
	if err != nil || !ok || got != room.Address {
		t.Fatalf("resolve = %q ok=%t err=%v, want %q", got, ok, err, room.Address)
	}
	if err := d.RevokeRoomInvite(ctx, room.Address, inv2.Code); err != nil {
		t.Fatalf("revoke inv2: %v", err)
	}
	if _, ok, _ := d.ResolveRoomInvite(ctx, inv2.Code); ok {
		t.Fatal("a revoked code must not resolve")
	}
}

func TestTokenRotationKillsOldToken(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	room, users := seedCredWorld(t, ctx, d, 2)
	a, b := users[0], users[1]

	stored, err := d.UserTokenHash(ctx, room.Address, a.Address)
	if err != nil || !hashMatches("secret-UserA", stored) {
		t.Fatalf("seeded token hash: %q err=%v", stored, err)
	}

	// Rotation: presenting the current token (done by the caller) stores
	// the new hash; the old one no longer matches.
	if err := d.SetUserTokenHash(ctx, room.Address, a.Address, HashToken("rotated")); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	stored, _ = d.UserTokenHash(ctx, room.Address, a.Address)
	if hashMatches("secret-UserA", stored) {
		t.Fatal("the old token must be invalid after rotation")
	}
	if !hashMatches("rotated", stored) {
		t.Fatal("the new token must match after rotation")
	}

	// Room-scoped lookups do not leak across rooms.
	if err := d.SetRoomTokenHash(ctx, room.Address, HashToken("room-rotated")); err != nil {
		t.Fatalf("rotate room token: %v", err)
	}
	roomHash, err := d.RoomTokenHash(ctx, room.Address)
	if err != nil || !hashMatches("room-rotated", roomHash) {
		t.Fatalf("room hash = %q err=%v", roomHash, err)
	}

	// Scanner identification by token hash finds exactly the right persona.
	found, ok, err := d.UserByTokenHash(ctx, HashToken("secret-UserB"))
	if err != nil || !ok || found.Address != b.Address {
		t.Fatalf("UserByTokenHash = %v ok=%t err=%v", found, ok, err)
	}
}
