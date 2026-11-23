// Ownership credentials: persona/room tokens, one-time creation tokens,
// room invite codes and one-time mutual codes (feature #33).
//
// Rules (CONCEPT.md §7.1–§7.3): raw secrets are never stored, returned or
// logged; the server keeps SHA-256 hex hashes only, except the 6-digit
// invite codes which are stored plain (a 10^6 space gains nothing from
// hashing — join attempts are rate-limited instead).
package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// Sentinel errors for the credential layer, mapped to HTTP statuses by the
// server: ErrGone → 410 (a one-time code was already consumed), ErrForbidden
// → 403 (valid format, wrong value/rule).
var (
	ErrGone      = errors.New("gone")
	ErrForbidden = errors.New("forbidden")
)

// HashToken hashes a raw secret (persona/room token, creation token,
// mutual code) into the SHA-256 hex form stored on the records.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// hashMatches reports whether hash(raw) equals stored in constant time.
// Both are SHA-256 hex strings; a length difference resolves to false.
func hashMatches(raw, stored string) bool {
	return subtle.ConstantTimeCompare([]byte(HashToken(raw)), []byte(stored)) == 1
}

// hashEquals compares two stored hashes in constant time.
func hashEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---- persona tokens ----

// UserTokenHash returns the token hash of a non-deleted user inside the
// given room ("" when the user is unknown or has no token registered).
func (d *DB) UserTokenHash(ctx context.Context, roomAddress, address string) (string, error) {
	var h sql.NullString
	err := d.QueryRowContext(ctx,
		`SELECT token_hash FROM users WHERE address = ? AND room = ? AND deleted_at IS NULL`,
		address, roomAddress).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return h.String, nil
}

// UserTokenHashByAddress is UserTokenHash without the room scoping (for
// routes that carry no room in their path).
func (d *DB) UserTokenHashByAddress(ctx context.Context, address string) (string, error) {
	var h sql.NullString
	err := d.QueryRowContext(ctx,
		`SELECT token_hash FROM users WHERE address = ? AND deleted_at IS NULL`,
		address).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return h.String, nil
}

// UserByTokenHash finds a non-deleted user by the hash of their presented
// token (constant-time compare per row with a non-null token_hash). Used by
// the mutual-QR handshake, where the scanner is identified only by their
// Bearer token.
func (d *DB) UserByTokenHash(ctx context.Context, tokenHash string) (model.User, bool, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT address, name, room, public_bio, private_bio, greeting, language, active, created_at, updated_at, deleted_at, token_hash
		 FROM users WHERE token_hash IS NOT NULL AND deleted_at IS NULL`)
	if err != nil {
		return model.User{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			u                    model.User
			createdAt, updatedAt string
			deletedAt            sql.NullString
			tokenHashRow         sql.NullString
			active               int
		)
		if err := rows.Scan(&u.Address, &u.Name, &u.Room, &u.PublicBio, &u.PrivateBio,
			&u.Greeting, &u.Language, &active, &createdAt, &updatedAt, &deletedAt, &tokenHashRow); err != nil {
			return model.User{}, false, err
		}
		if hashEquals(tokenHashRow.String, tokenHash) {
			u.CreatedAt = parseTS(createdAt)
			u.UpdatedAt = parseTS(updatedAt)
			u.DeletedAt = parseNullTS(deletedAt)
			u.Active = active == 1
			return u, true, nil
		}
	}
	return model.User{}, false, rows.Err()
}

// SetUserTokenHash stores the token hash of a non-deleted user inside the
// given room (rotation: presenting the current token allows registering a
// new one; the old token dies).
func (d *DB) SetUserTokenHash(ctx context.Context, roomAddress, address, tokenHash string) error {
	res, err := d.ExecContext(ctx,
		`UPDATE users SET token_hash = ?, updated_at = ?
		 WHERE address = ? AND room = ? AND deleted_at IS NULL`,
		tokenHash, time.Now().UTC().Format(time.RFC3339), address, roomAddress)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.Join(ErrNotFound, fmt.Errorf("user %q not found in room %q", address, roomAddress))
	}
	return nil
}

// SetUserTokenHashByAddress stores the token hash regardless of the room
// (legacy migration only; the API routes are room-scoped).
func (d *DB) SetUserTokenHashByAddress(ctx context.Context, address, tokenHash string) error {
	res, err := d.ExecContext(ctx,
		`UPDATE users SET token_hash = ?, updated_at = ? WHERE address = ? AND deleted_at IS NULL`,
		tokenHash, time.Now().UTC().Format(time.RFC3339), address)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.Join(ErrNotFound, fmt.Errorf("user %q not found", address))
	}
	return nil
}

// ---- mutual one-time codes ----

// SetUserMutualCodeHash registers the hash of a fresh one-time mutual code
// for a non-deleted user inside the given room. Storing the hash makes the
// code live; there is no separate availability flag.
func (d *DB) SetUserMutualCodeHash(ctx context.Context, roomAddress, address, mutualCodeHash string) error {
	return d.setUserMutualCodeHashOn(ctx, d, roomAddress, address, mutualCodeHash)
}

func (d *DB) setUserMutualCodeHashOn(ctx context.Context, q execContext, roomAddress, address, mutualCodeHash string) error {
	res, err := q.ExecContext(ctx,
		`UPDATE users SET mutual_code_hash = ?, updated_at = ?
		 WHERE address = ? AND room = ? AND deleted_at IS NULL`,
		mutualCodeHash, time.Now().UTC().Format(time.RFC3339), address, roomAddress)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.Join(ErrNotFound, fmt.Errorf("user %q not found in room %q", address, roomAddress))
	}
	return nil
}

// SetUserMutualCodeHashByAddress registers the hash regardless of the room
// (legacy migration only).
func (d *DB) SetUserMutualCodeHashByAddress(ctx context.Context, address, mutualCodeHash string) error {
	res, err := d.ExecContext(ctx,
		`UPDATE users SET mutual_code_hash = ?, updated_at = ? WHERE address = ? AND deleted_at IS NULL`,
		mutualCodeHash, time.Now().UTC().Format(time.RFC3339), address)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.Join(ErrNotFound, fmt.Errorf("user %q not found", address))
	}
	return nil
}

// UserMutualCodeLive reports whether a one-time mutual code is registered
// (derived: a stored hash = live, NULL = consumed/none).
func (d *DB) UserMutualCodeLive(ctx context.Context, roomAddress, address string) (bool, error) {
	var h sql.NullString
	err := d.QueryRowContext(ctx,
		`SELECT mutual_code_hash FROM users WHERE address = ? AND room = ? AND deleted_at IS NULL`,
		address, roomAddress).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return h.Valid && h.String != "", nil
}

// MutualHandshakeResult reports what POST /api/handshake/mutual did.
type MutualHandshakeResult struct {
	// AB is the scanner -> target handshake, BA the target -> scanner one.
	AB, BA       model.Handshake
	ABNew, BANew bool
	Chat         model.Chat
	ChatFound    bool
}

// ConsumeMutualCode verifies the scanned raw one-time code against the
// target's stored hash and, on a match, creates both directed handshakes
// (idempotent, auto-chat) and clears the stored hash — everything in ONE
// transaction, so the burn is atomic with the pair creation.
//
// Rules (CONCEPT.md §7.3): the code is consumed even when the pair was
// already mutual (one chat per pair, no second chat); the income>outcome
// rule applies only to the caller's A→B direction and a rejection does NOT
// consume the code (the transaction rolls back); wrong code → ErrForbidden;
// missing hash → ErrGone ("code already used").
func (d *DB) ConsumeMutualCode(ctx context.Context, scannerAddress, targetAddress, rawCode string) (MutualHandshakeResult, error) {
	var res MutualHandshakeResult

	// The target must exist (404) before anything else.
	target, ok, err := d.UserByAddress(ctx, targetAddress)
	if err != nil {
		return res, err
	}
	if !ok || target.DeletedAt != nil {
		return res, errors.Join(ErrNotFound, fmt.Errorf("user %q not found", targetAddress))
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()

	// Read the stored hash on the same transaction that will clear it.
	var stored sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT mutual_code_hash FROM users WHERE address = ?`, targetAddress).Scan(&stored); err != nil {
		return res, err
	}
	if !stored.Valid || stored.String == "" {
		// No live code: explicit "code already used" (410).
		return res, errors.Join(ErrGone, fmt.Errorf(
			"no live mutual code for user %q: the code was already used", targetAddress))
	}
	if !hashMatches(rawCode, stored.String) {
		return res, errors.Join(ErrForbidden, errors.New("mutual code does not match"))
	}

	// Both directed handshakes; the caller's A→B carries the balance rule.
	hsAB := model.Handshake{
		FromAddress: scannerAddress,
		ToAddress:   targetAddress,
		Status:      model.DefaultStatus,
	}
	if res.ABNew, err = createHandshakeOn(ctx, tx, &hsAB, false); err != nil {
		return res, err
	}
	res.AB = hsAB

	hsBA := model.Handshake{
		FromAddress: targetAddress,
		ToAddress:   scannerAddress,
		Status:      model.DefaultStatus,
	}
	if res.BANew, err = createHandshakeOn(ctx, tx, &hsBA, true); err != nil {
		return res, err
	}
	res.BA = hsBA

	// Burn: only reachable when both handshakes succeeded (any error above
	// rolls the transaction back, leaving the code live).
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET mutual_code_hash = NULL, updated_at = ? WHERE address = ?`,
		time.Now().UTC().Format(time.RFC3339), targetAddress); err != nil {
		return res, err
	}

	res.Chat, res.ChatFound, err = findChat(ctx, tx, scannerAddress, targetAddress)
	if err != nil {
		return res, err
	}
	if !res.ChatFound {
		return res, errors.Join(ErrConflict, errors.New("pair chat missing after mutual handshake"))
	}
	return res, tx.Commit()
}

// ---- room tokens ----

// RoomTokenHash returns the token hash of a non-deleted room ("" when
// unknown or no token registered).
func (d *DB) RoomTokenHash(ctx context.Context, address string) (string, error) {
	var h sql.NullString
	err := d.QueryRowContext(ctx,
		`SELECT token_hash FROM rooms WHERE address = ? AND deleted_at IS NULL`, address).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return h.String, nil
}

// SetRoomTokenHash stores the token hash of a non-deleted room (rotation).
func (d *DB) SetRoomTokenHash(ctx context.Context, address, tokenHash string) error {
	res, err := d.ExecContext(ctx,
		`UPDATE rooms SET token_hash = ?, updated_at = ? WHERE address = ? AND deleted_at IS NULL`,
		tokenHash, time.Now().UTC().Format(time.RFC3339), address)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.Join(ErrNotFound, fmt.Errorf("room %q not found", address))
	}
	return nil
}

// ---- one-time creation tokens ----

// MintCreateToken stores the hash of a freshly minted one-time room-creation
// token (the raw token is printed once by the operator CLI and never stored).
func (d *DB) MintCreateToken(ctx context.Context, tokenHash string) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO create_tokens (hash, created_at, used_at) VALUES (?, ?, NULL)`,
		tokenHash, time.Now().UTC().Format(time.RFC3339))
	if isUnique(err) {
		return errors.Join(ErrConflict, errors.New("this creation token hash already exists"))
	}
	return err
}

// CreateTokenState reports whether the hash exists in the pool and whether
// it was already used ("", false) for unknown hashes.
func (d *DB) CreateTokenState(ctx context.Context, tokenHash string) (exists, used bool, err error) {
	var usedAt sql.NullString
	err = d.QueryRowContext(ctx,
		`SELECT used_at FROM create_tokens WHERE hash = ?`, tokenHash).Scan(&usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, usedAt.Valid && usedAt.String != "", nil
}

// ConsumeCreateToken burns a one-time creation token atomically: the
// conditional UPDATE marks used_at only when still unused. Returns false
// when the token is unknown or already used.
func (d *DB) ConsumeCreateToken(ctx context.Context, tokenHash string) (bool, error) {
	res, err := d.ExecContext(ctx,
		`UPDATE create_tokens SET used_at = ? WHERE hash = ? AND used_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), tokenHash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ---- room invite codes ----

// RoomInvite is a 6-digit join code of a room, stored plain.
type RoomInvite struct {
	Room      string     `json:"room"`
	Code      string     `json:"code"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// DefaultInviteExpiry is the lifetime of a freshly minted invite code.
const DefaultInviteExpiry = 24 * time.Hour

// NewInviteCode generates a random 6-digit code (leading zeros possible).
func NewInviteCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		// crypto/rand failing is a process-level problem; the caller surfaces
		// it as a 500 via the error path of the minting handler.
		panic("crypto/rand failure: " + err.Error())
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// CreateRoomInvite mints an invite code for a non-deleted room. Multi-use
// until expiry; several codes may be active per room. Retries on the
// unlikely 6-digit collision.
func (d *DB) CreateRoomInvite(ctx context.Context, roomAddress string, expiresAt time.Time) (RoomInvite, error) {
	var inv RoomInvite
	if _, err := d.GetRoom(ctx, roomAddress); err != nil {
		return inv, err
	}
	now := time.Now().UTC()
	for attempt := 0; attempt < 10; attempt++ {
		inv = RoomInvite{
			Room:      roomAddress,
			Code:      NewInviteCode(),
			CreatedAt: now,
			ExpiresAt: expiresAt.UTC(),
		}
		_, err := d.ExecContext(ctx,
			`INSERT INTO room_invites (code, room_address, created_at, expires_at, revoked_at)
			 VALUES (?, ?, ?, ?, NULL)`,
			inv.Code, inv.Room, inv.CreatedAt.Format(time.RFC3339), inv.ExpiresAt.Format(time.RFC3339))
		if isUnique(err) {
			continue
		}
		if err != nil {
			return RoomInvite{}, err
		}
		return inv, nil
	}
	return RoomInvite{}, errors.Join(ErrConflict, errors.New("could not generate unique invite code"))
}

// ListRoomInvites returns the active (not revoked, not expired) invite
// codes of a room.
func (d *DB) ListRoomInvites(ctx context.Context, roomAddress string) ([]RoomInvite, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT code, room_address, created_at, expires_at, revoked_at
		 FROM room_invites
		 WHERE room_address = ? AND revoked_at IS NULL AND expires_at > ?
		 ORDER BY created_at ASC, code ASC`,
		roomAddress, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	invites := []RoomInvite{}
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// RevokeRoomInvite revokes an invite code of a room immediately. Unknown
// codes and codes of another room are ErrNotFound.
func (d *DB) RevokeRoomInvite(ctx context.Context, roomAddress, code string) error {
	res, err := d.ExecContext(ctx,
		`UPDATE room_invites SET revoked_at = ?
		 WHERE code = ? AND room_address = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), code, roomAddress)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.Join(ErrNotFound, fmt.Errorf("invite code %q not found in room %q", code, roomAddress))
	}
	return nil
}

// ValidRoomInvite reports whether the code can be used to join the room:
// it exists there, is not revoked and is not expired.
func (d *DB) ValidRoomInvite(ctx context.Context, roomAddress, code string) (bool, error) {
	return d.exists(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM room_invites
			WHERE code = ? AND room_address = ? AND revoked_at IS NULL AND expires_at > ?)`,
		code, roomAddress, time.Now().UTC().Format(time.RFC3339))
}

// ResolveRoomInvite maps a global invite code to its room address (the
// public join-by-code resolution, 6-digit codes are unique across rooms).
func (d *DB) ResolveRoomInvite(ctx context.Context, code string) (string, bool, error) {
	var room string
	err := d.QueryRowContext(ctx,
		`SELECT room_address FROM room_invites
		 WHERE code = ? AND revoked_at IS NULL AND expires_at > ?`,
		code, time.Now().UTC().Format(time.RFC3339)).Scan(&room)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return room, true, nil
}

func scanInvite(s scanner) (RoomInvite, error) {
	var (
		inv                RoomInvite
		createdAt, expires string
		revoked            sql.NullString
	)
	if err := s.Scan(&inv.Code, &inv.Room, &createdAt, &expires, &revoked); err != nil {
		return RoomInvite{}, err
	}
	inv.CreatedAt = parseTS(createdAt)
	inv.ExpiresAt = parseTS(expires)
	inv.RevokedAt = parseNullTS(revoked)
	return inv, nil
}
