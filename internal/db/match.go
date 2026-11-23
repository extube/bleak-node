// The public match-v2 score (feature #36) — the pure matching-interest
// percent — and the open-interval clamp shared by the scores that must
// never read 0 or 100.
//
// Public v2 (POST /api/match/v2) is the pure matching-interest percent:
//
//	points = 100 * 2 * exactMatches / (totalInterestsFirst + totalInterestsSecond)
//
// every exact interest match (same group AND key) counts on both sides, so
// two identical interest sets grade 100 and disjoint sets grade 0. No time
// and no handshake input: the same interest sets always grade the same.
// The HARD match (feature #43) merges this score with the networking
// checklist: points = 16 * closed + 0.52 * MatchV2 (db/summary.go).
package db

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/extube/bleak-node/internal/model"
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

// clampOpen pins a score inside the open interval (0, 100): the endpoints
// themselves are impossible. The bounds absorb the rounding of values
// asymptotically close to 0 or 100.
func clampOpen(p float64) float64 {
	return math.Max(0.01, math.Min(99.99, p))
}
