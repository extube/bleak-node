// The time-independent matching score between two personas (feature #35).
//
// The legacy score (db.interest.go, POST /api/match) decays with a
// requested time; this method (POST /api/match/v2) deliberately does not:
// the same pair of personas always grades to the same points. Its result is
// confined to the OPEN interval (0, 100): 0 and 100 are impossible, so the
// value stays strictly inside and can never read as a perfect match.
//
// points = 50 + 50 * tanh(coef / SoftTanhScale)
//
// tanh maps the coefficient domain (-inf, +inf) onto (-1, 1), so the result
// approaches 0 and 100 asymptotically but never reaches either. The
// coefficient is the same relation-based sum the legacy method uses:
//
//   - one handshake direction: +CoefHandshake (a mutual pair counts both
//     directions; unlike the legacy method there is NO instant 100);
//   - every shared interest group: +CoefSameGroup;
//   - every exact interest match: +CoefSameKey.
//
// Difficulty calibration (owner directive): a FULL match (> 80 points)
// must stay out of reach for ~80% of random user pairs. Simulated random
// populations put the old scale (10) at ~29% over 80; SoftTanhScale = 12
// moves a plain mutual handshake (coef 8) to 79.14 - just below the line -
// so a handshake alone never grants a full match, and the ~20% of pairs
// that cross 80 need real shared interests on top.
package db

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/extube/bleak-node/internal/model"
)

// SoftTanhScale stretches the coefficient so ordinary relation values land
// in a readable range AND keeps the > 80 full-match zone rare: at scale 12
// one handshake (coef 4) scores ~66, mutual handshake + one shared group
// (coef 10) ~84, a plain mutual handshake (coef 8) 79.14 - the calibration
// is documented in the package comment.
const SoftTanhScale = 12.0

// soft score bounds after rounding: rounding a value asymptotically close
// to the endpoints could still print 100.00 or 0.00, so the final result is
// pinned into the open interval.
const (
	softScoreMin = 0.01
	softScoreMax = 99.99
)

// MatchPointsV2 computes the time-independent matching score of two
// personas inside one room, always strictly inside (0, 100). Both users
// must live in the room (ErrNotFound otherwise) and differ (ErrInvalid).
func (d *DB) MatchPointsV2(ctx context.Context, roomAddress, first, second string) (MatchResult, error) {
	var res MatchResult
	if first == second {
		return res, errors.Join(ErrInvalid, errors.New("first_user_address and second_user_address must differ"))
	}

	// One coherent snapshot of handshakes and interests.
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err = matchPointsV2On(ctx, tx, roomAddress, first, second, true)
	if err != nil {
		return res, err
	}
	return res, tx.Commit()
}

// matchQuerier is the executor subset the V2 score reads on: transaction,
// pool or connection.
type matchQuerier interface {
	rowQuerier
	listQuerier
}

// matchPointsV2On is MatchPointsV2 on the given executor, so the hard match
// can compute the soft score inside its own read transaction. countHandshakes
// selects whether handshake directions weigh in: the standalone v2 score
// counts them, while the HARD MATCH must ignore them entirely (owner
// directive) - every chatting pair is mutual by construction, so handshake
// points would be a constant +8 on every summary and say nothing about the
// pair. The hard match is driven by shared interests and the checkpoint
// quality alone.
func matchPointsV2On(ctx context.Context, q matchQuerier, roomAddress, first, second string, countHandshakes bool) (MatchResult, error) {
	var res MatchResult
	for _, addr := range []string{first, second} {
		var ok bool
		if err := q.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM users WHERE address = ? AND room = ? AND deleted_at IS NULL)`,
			addr, roomAddress).Scan(&ok); err != nil {
			return res, err
		}
		if !ok {
			return res, errors.Join(ErrNotFound,
				fmt.Errorf("user %q not found in room %q", addr, roomAddress))
		}
	}

	directions := 0
	if countHandshakes {
		// Handshake directions between the two personas within the room.
		if err := q.QueryRowContext(ctx, `
		SELECT (SELECT EXISTS (SELECT 1 FROM handshakes
			WHERE room = ? AND from_address = ? AND to_address = ?)) +
		       (SELECT EXISTS (SELECT 1 FROM handshakes
			WHERE room = ? AND from_address = ? AND to_address = ?))`,
			roomAddress, first, second,
			roomAddress, second, first).Scan(&directions); err != nil {
			return res, err
		}
		res.Handshake = directions > 0
		res.MutualHandshake = directions == 2
	}

	// Interests of both personas: exact matches (same group AND key) and
	// shared groups (the group appears on both sides). The matching logic
	// mirrors the legacy method one to one.
	interestsA, err := listInterestsOn(ctx, q, first)
	if err != nil {
		return res, err
	}
	interestsB, err := listInterestsOn(ctx, q, second)
	if err != nil {
		return res, err
	}
	byKeyA := make(map[int]model.Interest, len(interestsA))
	groupsA := make(map[int]struct{}, len(interestsA))
	for _, in := range interestsA {
		byKeyA[in.Key] = in
		groupsA[in.Group] = struct{}{}
	}
	groupsB := make(map[int]struct{}, len(interestsB))
	for _, in := range interestsB {
		groupsB[in.Group] = struct{}{}
		if a, ok := byKeyA[in.Key]; ok && a.Group == in.Group {
			res.ExactMatches++
		}
	}
	for g := range groupsA {
		if _, ok := groupsB[g]; ok {
			res.SharedGroups++
		}
	}

	coef := CoefHandshake*directions +
		CoefSameGroup*res.SharedGroups +
		CoefSameKey*res.ExactMatches

	res.Points = softScore(float64(coef))
	return res, nil
}

// softScore maps a non-negative relation coefficient into the open interval
// (0, 100), rounded to 2 decimals.
func softScore(coef float64) float64 {
	p := 50 + 50*math.Tanh(coef/SoftTanhScale)
	p = math.Round(p*100) / 100
	return clampOpen(p)
}

// clampOpen pins a score inside the open interval (0, 100): the endpoints
// themselves are impossible.
func clampOpen(p float64) float64 {
	return math.Max(softScoreMin, math.Min(softScoreMax, p))
}
