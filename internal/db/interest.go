// User interests and the matching score between two personas.
//
// A persona declares a list of interests: each carries a title classified
// by a numeric group (the category) and a numeric key (the concrete
// interest). The matching score (feature #36 formula) grades two users of
// one room on [0, 100]:
//
//	points = 100 * (1 - 1 / (C * 2^t))
//
// points = 100 * (1 - 1 / 2^sqrt(t + C))
//
// with t the hours (float) since the FIRST persona's last SENT handshake —
// the user the score is "for" (the auto-match matrix row); a persona that
// has never sent a handshake counts from their creation instead — and C
// the relation coefficient (see MatchPoints).
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/extube/bleak-node/internal/model"
)

// Matching-score coefficients. The legacy formula (feature #34,
// clamp(50 + 50 * (1/2^t) + coef)) is retired; the coefficients survive
// only in the HARD match's calibrated soft score (db/match.go,
// matchSoftOn) and may be retuned without contract changes.
const (
	CoefHandshake = 4
	CoefSameGroup = 2
	CoefSameKey   = 5
)

// MatchResult is the response of the matching-score computation.
type MatchResult struct {
	// Points is the final score, always within [0, 100].
	Points float64 `json:"points"`
	// MutualHandshake reports that both directed handshakes exist (the
	// instant-100 case).
	MutualHandshake bool `json:"mutual_handshake"`
	// Handshake reports that at least one directed handshake exists.
	Handshake bool `json:"handshake"`
	// ExactMatches is the number of exact interest matches (same group
	// and same key on both sides).
	ExactMatches int `json:"exact_interest_matches"`
	// SharedGroups is the number of interest groups present on both sides.
	SharedGroups int `json:"shared_groups"`
}

// validateInterest checks one client-supplied interest row.
func validateInterest(in model.Interest) error {
	if n := utf8.RuneCountInString(in.Title); n == 0 || n > model.MaxInterestTitleLen {
		return errors.Join(ErrInvalid, fmt.Errorf(
			"interest title must be 1..%d symbols (got %d)", model.MaxInterestTitleLen, n))
	}
	if in.Group < 0 {
		return errors.Join(ErrInvalid, fmt.Errorf("interest group must be >= 0 (got %d)", in.Group))
	}
	if in.Key < 0 {
		return errors.Join(ErrInvalid, fmt.Errorf("interest key must be >= 0 (got %d)", in.Key))
	}
	return nil
}

// userInRoomOn reports whether a non-deleted user lives in the room, on
// the given executor.
func (d *DB) userInRoomOn(ctx context.Context, q rowQuerier, room, address string) (bool, error) {
	return d.existsOn(ctx, q,
		`SELECT EXISTS (SELECT 1 FROM users WHERE address = ? AND room = ? AND deleted_at IS NULL)`,
		address, room)
}

// AddUserInterests upserts the interests of a non-deleted user inside the
// given room (each key is unique per user; re-adding a key replaces the
// title/group) and returns the user's full resulting list. An empty list
// is ErrInvalid.
func (d *DB) AddUserInterests(ctx context.Context, roomAddress, address string, list []model.Interest) ([]model.Interest, error) {
	if len(list) == 0 {
		return nil, errors.Join(ErrInvalid, errors.New("interests list is empty"))
	}
	for _, in := range list {
		if err := validateInterest(in); err != nil {
			return nil, err
		}
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if ok, err := d.userInRoomOn(ctx, tx, roomAddress, address); err != nil {
		return nil, err
	} else if !ok {
		return nil, errors.Join(ErrNotFound,
			fmt.Errorf("user %q not found in room %q", address, roomAddress))
	}

	for _, in := range list {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO interests (user_address, title, "group", "key")
			VALUES (?, ?, ?, ?)
			ON CONFLICT (user_address, "key")
			DO UPDATE SET title = excluded.title, "group" = excluded."group"`,
			address, in.Title, in.Group, in.Key); err != nil {
			return nil, err
		}
	}
	res, err := listInterestsOn(ctx, tx, address)
	if err != nil {
		return nil, err
	}
	return res, tx.Commit()
}

// DeleteUserInterests removes the interests with the given keys of a
// non-deleted user inside the given room and returns the remaining list.
// Unknown keys are not an error (the delete is idempotent); the int result
// reports how many rows were removed.
func (d *DB) DeleteUserInterests(ctx context.Context, roomAddress, address string, keys []int) (int, []model.Interest, error) {
	if len(keys) == 0 {
		return 0, nil, errors.Join(ErrInvalid, errors.New("keys list is empty"))
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if ok, err := d.userInRoomOn(ctx, tx, roomAddress, address); err != nil {
		return 0, nil, err
	} else if !ok {
		return 0, nil, errors.Join(ErrNotFound,
			fmt.Errorf("user %q not found in room %q", address, roomAddress))
	}

	// DELETE ... IN with per-key placeholders (the list is bounded by the
	// request cap, so the dynamic placeholder count stays small).
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys)+1)
	args = append(args, address)
	for _, k := range keys {
		args = append(args, k)
	}
	res, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM interests WHERE user_address = ? AND "key" IN (%s)`, placeholders),
		args...)
	if err != nil {
		return 0, nil, err
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return 0, nil, err
	}
	remaining, err := listInterestsOn(ctx, tx, address)
	if err != nil {
		return 0, nil, err
	}
	return int(removed), remaining, tx.Commit()
}

// ListUserInterests returns all interests of a non-deleted user inside the
// given room, ordered by group, then key.
func (d *DB) ListUserInterests(ctx context.Context, roomAddress, address string) ([]model.Interest, error) {
	if ok, err := d.userInRoomOn(ctx, d, roomAddress, address); err != nil {
		return nil, err
	} else if !ok {
		return nil, errors.Join(ErrNotFound,
			fmt.Errorf("user %q not found in room %q", address, roomAddress))
	}
	return listInterestsOn(ctx, d, address)
}

func listInterestsOn(ctx context.Context, q listQuerier, address string) ([]model.Interest, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT user_address, title, "group", "key" FROM interests
		 WHERE user_address = ?
		 ORDER BY "group" ASC, "key" ASC`, address)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Interest{}
	for rows.Next() {
		var in model.Interest
		if err := rows.Scan(&in.UserAddress, &in.Title, &in.Group, &in.Key); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// existsOn is exists on the given executor.
func (d *DB) existsOn(ctx context.Context, q rowQuerier, query string, args ...any) (bool, error) {
	var one bool
	if err := q.QueryRowContext(ctx, query, args...).Scan(&one); err != nil {
		return false, err
	}
	return one, nil
}

// listQuerier is the QueryContext subset of *sql.DB / *sql.Tx used by the
// interest list helper.
type listQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// MatchPoints computes the matching score of two personas inside one room
// (feature #39 formula). Both users must live in the room (ErrNotFound
// otherwise) and differ (ErrInvalid). The score:
//
//	points = 100 * (1 - 1 / 2^sqrt(t + C))
//
// with t the hours (float) since the FIRST persona's last SENT handshake in
// the room — the user the score is "for" (the auto-match matrix row); a
// persona that has never sent a handshake counts from their creation
// instead. Never negative — a future timestamp counts as brand new. C is
// the relation coefficient:
//
//		C = (2 + 2*H_r + 1*H_s) * matched / total
//
//	  - H_r: 1 when the OTHER persona's directed handshake to the first
//	    exists in the room (a received handshake counts double — reciprocity
//	    is the stronger signal), else 0;
//	  - H_s: 1 when the first persona's directed handshake to the other
//	    exists, else 0;
//	  - matched / total: exact interest matches (same group AND key) over
//	    the interests total of both personas; a pair with no interests at
//	    all grades C = 0.
//
// The coefficient lives INSIDE the square root as a SUMMAND: sqrt(t + C)
// treats a unit of relation like an extra hour of idle time. A pair with
// no relation (C = 0) grows on age alone — 50 after an hour, 75 after
// four — while every shared interest and handshake direction pulls the
// saturation (100) closer. The result is clamped into [0, 100] and
// rounded to 2 decimals.
func (d *DB) MatchPoints(ctx context.Context, roomAddress, first, second string) (MatchResult, error) {
	var res MatchResult
	if first == second {
		return res, errors.Join(ErrInvalid, errors.New("first_user_address and second_user_address must differ"))
	}

	// One consistent snapshot: handshakes, interests and users are read on
	// the same implicit transaction window (single-writer SQLite keeps the
	// writes serialized; a read tx guarantees a coherent view).
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()

	// The FIRST persona's creation stamps the formula's t.
	var createdFirst string
	err = tx.QueryRowContext(ctx,
		`SELECT created_at FROM users WHERE address = ? AND room = ? AND deleted_at IS NULL`,
		first, roomAddress).Scan(&createdFirst)
	if errors.Is(err, sql.ErrNoRows) {
		return res, errors.Join(ErrNotFound,
			fmt.Errorf("user %q not found in room %q", first, roomAddress))
	}
	if err != nil {
		return res, err
	}
	if ok, err := d.userInRoomOn(ctx, tx, roomAddress, second); err != nil {
		return res, err
	} else if !ok {
		return res, errors.Join(ErrNotFound,
			fmt.Errorf("user %q not found in room %q", second, roomAddress))
	}

	// Handshake directions between the two personas within the room:
	// H_s = the first persona's handshake TO the other, H_r = the other's
	// handshake received BY the first.
	var hs, hr int
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM handshakes
			WHERE room = ? AND from_address = ? AND to_address = ?)`,
		roomAddress, first, second).Scan(&hs); err != nil {
		return res, err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM handshakes
			WHERE room = ? AND from_address = ? AND to_address = ?)`,
		roomAddress, second, first).Scan(&hr); err != nil {
		return res, err
	}
	res.Handshake = hs+hr > 0
	res.MutualHandshake = hs+hr == 2

	// Interests of both personas: exact matches (same group AND key) and
	// shared groups (the group appears on both sides).
	var interestsA, interestsB []model.Interest
	interestsA, interestsB, res, err = interestOverlapOn(ctx, tx, res, first, second)
	if err != nil {
		return res, err
	}

	// C = (2 + 2*H_r + 1*H_s) * matched / total — a received handshake
	// counts double, a sent one single; the interest part is the exact
	// matches over the interests total of BOTH sides. No interests grades
	// C = 0 (the pair stays at 0 points whatever its age).
	totalInterests := len(interestsA) + len(interestsB)
	c := 0.0
	if totalInterests > 0 {
		c = float64(2+2*hr+hs) * float64(res.ExactMatches) / float64(totalInterests)
	}

	// t: hours since the FIRST persona's last SENT handshake in the room
	// (the recency of their last outreach); a persona that never sent one
	// falls back to their creation age. Never negative — a future
	// timestamp counts as brand new. C lives INSIDE the sqrt as a
	// summand — a unit of relation acts like an extra hour:
	// points = 100 * (1 - 1 / 2^sqrt(t + C)).
	var lastSent sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(created_at) FROM handshakes WHERE room = ? AND from_address = ?`,
		roomAddress, first).Scan(&lastSent); err != nil {
		return res, err
	}
	t := time.Since(parseTS(createdFirst)).Hours()
	if lastSent.Valid {
		t = time.Since(parseTS(lastSent.String)).Hours()
	}
	if t < 0 {
		t = 0
	}

	res.Points = 100 * (1 - 1/math.Pow(2, math.Sqrt(t+c)))
	// Clamp into [0, 100]; round to 2 decimals so clients see clean numbers.
	res.Points = math.Max(0, math.Min(100, res.Points))
	res.Points = math.Round(res.Points*100) / 100
	return res, tx.Commit()
}
