package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/extube/bleak-node/internal/model"
)

// rowQuerier is the subset of *sql.DB / *sql.Tx used for chat helpers.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// findChat returns the chat linking exactly two users, if any.
// from == to never matches anything by construction.
func findChat(ctx context.Context, q rowQuerier, a, b string) (model.Chat, bool, error) {
	row := q.QueryRowContext(ctx, `
SELECT c.address, c.room, c.scenario, c.language, c.phase, c.phase_started_at, c.created_at
FROM chats c
JOIN participants p1 ON p1.chat = c.address AND p1.user = ?
JOIN participants p2 ON p2.chat = c.address AND p2.user = ?
WHERE p1.user <> p2.user
ORDER BY c.created_at ASC
LIMIT 1`, a, b)

	var (
		c         model.Chat
		createdAt string
		startedAt string
	)
	if err := row.Scan(&c.Address, &c.Room, &c.Scenario, &c.Language, &c.Phase, &startedAt, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Chat{}, false, nil
		}
		return model.Chat{}, false, err
	}
	c.PhaseStartedAt = parseNullTS(sql.NullString{String: startedAt, Valid: startedAt != ""})
	c.CreatedAt = parseTS(createdAt)
	return c, true, nil
}

// ensureChat creates the chat plus its two participants when the pair does
// not have one yet; idempotent. Runs on the given executor (tx or db).
// The bool reports whether a new chat was created.
func ensureChat(ctx context.Context, q rowQuerier, room, a, b string) (model.Chat, bool, error) {
	c, found, err := findChat(ctx, q, a, b)
	if err != nil || found {
		return c, false, err
	}

	c = model.Chat{Room: room, Language: "en", CreatedAt: time.Now().UTC()}
	for attempt := 0; attempt < 3; attempt++ {
		c.Address = newAddress(16)
		if _, err := q.ExecContext(ctx,
			`INSERT INTO chats (address, room, scenario, language, phase, phase_started_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			c.Address, c.Room, "", c.Language, "", "", c.CreatedAt.Format(time.RFC3339)); !isUnique(err) {
			if err != nil {
				return model.Chat{}, false, err
			}
			break
		}
	}
	if c.Address == "" {
		return model.Chat{}, false, errors.Join(ErrConflict, errors.New("could not generate unique chat address"))
	}

	// Participants are created automatically with the chat.
	for _, u := range []string{a, b} {
		if _, err := q.ExecContext(ctx,
			`INSERT INTO participants (chat, user) VALUES (?, ?)`, c.Address, u); err != nil && !isUnique(err) {
			return model.Chat{}, false, err
		}
	}
	return c, true, nil
}

// bothHandshakeDirections reports whether the handshakes a -> b and b -> a
// both exist. It runs on the given executor so a just-inserted row counts.
func bothHandshakeDirections(ctx context.Context, q rowQuerier, a, b string) (bool, error) {
	var both bool
	err := q.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1 FROM handshakes WHERE from_address = ? AND to_address = ?
) AND EXISTS (
	SELECT 1 FROM handshakes WHERE from_address = ? AND to_address = ?
)`, a, b, b, a).Scan(&both)
	return both, err
}

// GetChat returns a chat with its participants.
func (d *DB) GetChat(ctx context.Context, address string) (model.Chat, []model.Participant, error) {
	c, found, err := d.ChatByAddress(ctx, address)
	if err != nil || !found {
		return model.Chat{}, nil, errors.Join(ErrNotFound, sql.ErrNoRows)
	}
	parts, err := d.ChatParticipants(ctx, address)
	if err != nil {
		return model.Chat{}, nil, err
	}
	return c, parts, nil
}

// ChatByAddress returns a chat by its address.
func (d *DB) ChatByAddress(ctx context.Context, address string) (model.Chat, bool, error) {
	row := d.QueryRowContext(ctx,
		`SELECT address, room, scenario, language, phase, phase_started_at, created_at FROM chats WHERE address = ?`, address)
	var (
		c         model.Chat
		createdAt string
		startedAt string
	)
	if err := row.Scan(&c.Address, &c.Room, &c.Scenario, &c.Language, &c.Phase, &startedAt, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Chat{}, false, nil
		}
		return model.Chat{}, false, err
	}
	c.PhaseStartedAt = parseNullTS(sql.NullString{String: startedAt, Valid: startedAt != ""})
	c.CreatedAt = parseTS(createdAt)
	return c, true, nil
}

// SetChatLanguage stores the response language for a chat. Empty language
// values default to English; only supported language codes are accepted.
func (d *DB) SetChatLanguage(ctx context.Context, address, language string) error {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" {
		language = "en"
	}
	if language != "en" && language != "ru" {
		return errors.Join(ErrInvalid, fmt.Errorf("unsupported chat language %q (use en or ru)", language))
	}
	res, err := d.ExecContext(ctx,
		`UPDATE chats SET language = ? WHERE address = ?`, language, address)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return errors.Join(ErrNotFound, fmt.Errorf("chat %q not found", address))
	}
	return nil
}

// ChatScenario returns the scenario key stored on the chat. The bool
// reports that the chat exists and has a non-empty scenario.
func (d *DB) ChatScenario(ctx context.Context, address string) (string, bool, error) {
	var scenario sql.NullString
	err := d.QueryRowContext(ctx,
		`SELECT scenario FROM chats WHERE address = ?`, address).Scan(&scenario)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !scenario.Valid || scenario.String == "" {
		return "", false, nil
	}
	return scenario.String, true, nil
}

// SetChatScenario stores the scenario key on the chat.
func (d *DB) SetChatScenario(ctx context.Context, address, scenario string) error {
	res, err := d.ExecContext(ctx,
		`UPDATE chats SET scenario = ? WHERE address = ?`, scenario, address)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return errors.Join(ErrNotFound, fmt.Errorf("chat %q not found", address))
	}
	return nil
}

// ChatPhase returns the conversation phase stored on the chat. The bool
// reports that the chat exists and carries a non-empty phase.
func (d *DB) ChatPhase(ctx context.Context, address string) (string, bool, error) {
	var phase string
	err := d.QueryRowContext(ctx,
		`SELECT phase FROM chats WHERE address = ?`, address).Scan(&phase)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if phase == "" {
		return "", false, nil
	}
	return phase, true, nil
}

// SetChatPhase overwrites the conversation phase of the chat ("", networking,
// debate) together with the moment the current phase began (zero for the
// networking phase). The stamp lets the worker replay only the messages of the
// current phase while all earlier messages stay saved in the table.
// created_at strings carry whole-second RFC3339 precision, so the stamp is
// floored to the NEXT second: every message of the finished part sits
// strictly before the boundary, boundary-adjacent ones included.
func (d *DB) SetChatPhase(ctx context.Context, address, phase string, startedAt time.Time) error {
	stamp := ""
	if !startedAt.IsZero() {
		stamp = startedAt.UTC().Truncate(time.Second).Add(time.Second).Format(time.RFC3339)
	}
	res, err := d.ExecContext(ctx,
		`UPDATE chats SET phase = ?, phase_started_at = ? WHERE address = ?`, phase, stamp, address)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return errors.Join(ErrNotFound, fmt.Errorf("chat %q not found", address))
	}
	return nil
}

// ChatParticipants returns the participants of a chat.
func (d *DB) ChatParticipants(ctx context.Context, chatAddress string) ([]model.Participant, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT chat, user FROM participants WHERE chat = ? ORDER BY user ASC`, chatAddress)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	parts := []model.Participant{}
	for rows.Next() {
		var p model.Participant
		if err := rows.Scan(&p.Chat, &p.User); err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	return parts, rows.Err()
}

// ListUserChats returns all chats the user participates in, newest first.
func (d *DB) ListUserChats(ctx context.Context, user string) ([]model.Chat, error) {
	rows, err := d.QueryContext(ctx, `
SELECT c.address, c.room, c.scenario, c.language, c.phase, c.phase_started_at, c.created_at
FROM chats c
JOIN participants p ON p.chat = c.address
WHERE p.user = ?
ORDER BY c.created_at DESC, c.address ASC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chats := []model.Chat{}
	for rows.Next() {
		var (
			c         model.Chat
			createdAt string
			startedAt string
		)
		if err := rows.Scan(&c.Address, &c.Room, &c.Scenario, &c.Language, &c.Phase, &startedAt, &createdAt); err != nil {
			return nil, err
		}
		c.PhaseStartedAt = parseNullTS(sql.NullString{String: startedAt, Valid: startedAt != ""})
		c.CreatedAt = parseTS(createdAt)
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

// ChatExists reports whether a chat address is live.
func (d *DB) ChatExists(ctx context.Context, address string) (bool, error) {
	return d.exists(ctx, `SELECT EXISTS (SELECT 1 FROM chats WHERE address = ?)`, address)
}

// ListRoomUserChats returns every chat of a user that lives in the room
// (non-deleted), newest first.
func (d *DB) ListRoomUserChats(ctx context.Context, roomAddress, address string) ([]model.Chat, error) {
	ok, err := d.roomHasUser(ctx, roomAddress, address)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.Join(ErrNotFound, fmt.Errorf("user %q not found in room %q", address, roomAddress))
	}
	return d.ListUserChats(ctx, address)
}

// ListRoomUserChatMessages returns every message of a chat, oldest first,
// provided the user lives in the room and participates in that chat.
func (d *DB) ListRoomUserChatMessages(ctx context.Context, roomAddress, address, chatAddress string) ([]model.Message, error) {
	if ok, err := d.roomHasUser(ctx, roomAddress, address); err != nil {
		return nil, err
	} else if !ok {
		return nil, errors.Join(ErrNotFound, fmt.Errorf("user %q not found in room %q", address, roomAddress))
	}
	if ok, err := d.ChatExists(ctx, chatAddress); err != nil {
		return nil, err
	} else if !ok {
		return nil, errors.Join(ErrNotFound, fmt.Errorf("chat %q not found", chatAddress))
	}
	if ok, err := d.exists(ctx,
		`SELECT EXISTS (SELECT 1 FROM participants WHERE chat = ? AND user = ?)`, chatAddress, address); err != nil {
		return nil, err
	} else if !ok {
		return nil, errors.Join(ErrNotFound, fmt.Errorf(
			"user %q does not participate in chat %q", address, chatAddress))
	}
	return d.ListChatMessages(ctx, chatAddress)
}

// ClearChatMessages removes the whole message history of the chat. The chat,
// its participants and its events stay untouched.
func (d *DB) ClearChatMessages(ctx context.Context, chatAddress string) (int64, error) {
	ok, err := d.ChatExists(ctx, chatAddress)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.Join(ErrNotFound, err)
	}
	res, err := d.ExecContext(ctx, `DELETE FROM messages WHERE chat = ?`, chatAddress)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ChatKickoffResult reports what a kickoff did.
type ChatKickoffResult struct {
	Chat    string `json:"chat"`
	Cleared int64  `json:"cleared"`
	Speaker string `json:"speaker"`
}

// ChatKickoff clears the message history of the chat and queues a fresh
// chat.interact event. The first speaker is the sender of the earliest
// handshake between the pair. Everything happens in one transaction, so the
// history wipe and the new turn appear atomically.
func (d *DB) ChatKickoff(ctx context.Context, chatAddress string) (ChatKickoffResult, error) {
	res := ChatKickoffResult{Chat: chatAddress}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()

	// The chat must exist; a kickoff restarts the whole two-part structure
	// from scratch: every earlier message of both phases is wiped and the
	// phase returns to the networking conversation.
	var exists string
	err = tx.QueryRowContext(ctx, `SELECT address FROM chats WHERE address = ?`, chatAddress).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return res, errors.Join(ErrNotFound, err)
	}
	if err != nil {
		return res, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE chats SET phase = '', phase_started_at = '' WHERE address = ?`, chatAddress); err != nil {
		return res, err
	}

	// Wipe the history inside the same transaction.
	cr, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE chat = ?`, chatAddress)
	if err != nil {
		return res, err
	}
	res.Cleared, _ = cr.RowsAffected()

	// The two participants decide the participants; the earliest handshake
	// decides who speaks first.
	rows, err := tx.QueryContext(ctx, `SELECT user FROM participants WHERE chat = ? ORDER BY user ASC`, chatAddress)
	if err != nil {
		return res, err
	}
	parts := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			_ = rows.Close()
			return res, err
		}
		parts = append(parts, u)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return res, err
	}
	_ = rows.Close()
	if len(parts) != 2 || parts[0] == parts[1] {
		return res, errors.Join(ErrInvalid, fmt.Errorf(
			"chat %q does not have exactly two participants", chatAddress))
	}

	var a, b string
	err = tx.QueryRowContext(ctx, `
SELECT from_address, to_address FROM handshakes
WHERE (from_address = ? AND to_address = ?) OR (from_address = ? AND to_address = ?)
ORDER BY created_at ASC, tx_id ASC LIMIT 1`, parts[0], parts[1], parts[1], parts[0]).
		Scan(&a, &b)
	if err != nil {
		return res, err
	}

	if err := enqueueEvent(ctx, tx, model.EventChatInteract, chatAddress, a, b); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.Speaker = a
	return res, nil
}

// roomHasUser reports whether a non-deleted user belongs to the room.
func (d *DB) roomHasUser(ctx context.Context, roomAddress, address string) (bool, error) {
	return d.exists(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE address = ? AND room = ? AND deleted_at IS NULL)`,
		address, roomAddress)
}
