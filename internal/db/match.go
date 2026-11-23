// The public match-v2 score (feature #36) and the hard match's calibrated
// soft score.
//
// Public v2 (POST /api/match/v2) is the pure matching-interest percent:
//
//	points = 100 * 2 * exactMatches / (totalInterestsFirst + totalInterestsSecond)
//
// every exact interest match (same group AND key) counts on both sides, so
// two identical interest sets grade 100 and disjoint sets grade 0. No time
// and no handshake input: the same interest sets always grade the same.
//
// The HARD match (feature #35) keeps its own calibrated soft component
// (matchSoftOn below): points = 50 + 50 * tanh(coef / SoftTanhScale) with
// the relation coefficient (+4 per handshake direction, +2 per shared
// group, +5 per exact match), pinned into the OPEN interval (0, 100).
// Its result is confined to the OPEN interval (0, 100): 0 and 100 are
// impossible, so the value stays strictly inside and can never read as a
// perfect match. The difficulty calibration is documented at SoftTanhScale.
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

// MatchPointsV2 computes the matching-interest percent of two personas
// inside one room (feature #36): points = 100 * 2 * exactMatches /
// (totalInterestsFirst + totalInterestsSecond) — every exact interest
// match (same group AND key) counts on both sides, so two identical
// interest sets grade 100 and pairs with nothing shared grade 0. Time and
// handshakes weigh nothing; the same interest sets always grade the same.
// Both users must live in the room (ErrNotFound otherwise) and differ
// (ErrInvalid). The handshake flags of the result stay informational.
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

	for _, addr := range []string{first, second} {
		var ok bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM users WHERE address = ? AND room = ? AND deleted_at IS NULL)`,
			addr, roomAddress).Scan(&ok); err != nil {
			return res, err
		}
		if !ok {
			return res, errors.Join(ErrNotFound,
				fmt.Errorf("user %q not found in room %q", addr, roomAddress))
		}
	}

	// Handshake directions between the two personas within the room:
	// informational flags only — the formula weighs interests alone.
	var directions int
	if err := tx.QueryRowContext(ctx, `
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

	var interestsA, interestsB []model.Interest
	interestsA, interestsB, res, err = interestOverlapOn(ctx, tx, res, first, second)
	if err != nil {
		return res, err
	}

	// The percent: matches count on both sides, so identical interest sets
	// reach the full 100. No interests on either side grades 0.
	total := len(interestsA) + len(interestsB)
	percent := 0.0
	if total > 0 {
		percent = 2 * float64(res.ExactMatches) / float64(total)
	}
	res.Points = math.Round(100*percent*100) / 100
	return res, tx.Commit()
}

// matchQuerier is the executor subset the V2 score reads on: transaction,
// pool or connection.
type matchQuerier interface {
	rowQuerier
	listQuerier
}

// matchSoftOn is the HARD match's calibrated soft score (feature #35) on
// the given executor, so the hard match can compute it inside its own read
// transaction. countHandshakes selects whether handshake directions weigh
// in: the standalone soft score counts them, while the HARD MATCH must
// ignore them entirely (owner directive) - every chatting pair is mutual by
// construction, so handshake points would be a constant +8 on every summary
// and say nothing about the pair. The hard match is driven by shared
// interests and the checkpoint quality alone.
//
// This is deliberately NOT the public /api/match/v2 formula anymore
// (feature #36 turned that endpoint into the pure interest percent): the
// tanh curve below keeps the hard match's documented difficulty
// calibration (a full match > 80 stays rare).
func matchSoftOn(ctx context.Context, q matchQuerier, roomAddress, first, second string, countHandshakes bool) (MatchResult, error) {
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
	// shared groups (the group appears on both sides).
	var err error
	_, _, res, err = interestOverlapOn(ctx, q, res, first, second)
	if err != nil {
		return res, err
	}

	coef := CoefHandshake*directions +
		CoefSameGroup*res.SharedGroups +
		CoefSameKey*res.ExactMatches

	res.Points = softScore(float64(coef))
	return res, nil
}

// interestOverlapOn compares the interest lists of two personas on the
// given executor: it fills the result's ExactMatches (same group AND key
// on both sides) and SharedGroups (the group appears on both sides) and
// returns both lists so callers can compute their totals.
func interestOverlapOn(ctx context.Context, q matchQuerier, res MatchResult, first, second string) ([]model.Interest, []model.Interest, MatchResult, error) {
	interestsA, err := listInterestsOn(ctx, q, first)
	if err != nil {
		return nil, nil, res, err
	}
	interestsB, err := listInterestsOn(ctx, q, second)
	if err != nil {
		return nil, nil, res, err
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
	return interestsA, interestsB, res, nil
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
