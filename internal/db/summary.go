// Hard matching (feature #43): the summarized, per-direction record of a
// user pair. The method sums up the pair's chat, grades the networking
// checklist, merges the closed count with the time-independent match score
// (POST /api/match/v2) and stores ONE summary per DIRECTION: a summary for
// A -> B exists without a B -> A record — the reverse direction is a
// separate request of its own. A repeated hard-match request for the same
// direction returns the stored record instead of re-summarizing.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/extube/bleak-node/internal/model"
)

// Summary weights (feature #43): the checklist carries 48% of the score
// (3 criteria, 16 points each), the time-independent match score carries
// 52%.
const (
	summaryCheckpoints    = 3
	summaryItemPoints     = 16.0
	summaryChecklistShare = summaryCheckpoints * summaryItemPoints // 48
	summaryMatchShare     = 0.52
)

// SummaryInput is what a chat summarizer feeds into the hard match: the
// plain-text summarization and the closed flags aligned with the canonical
// model.NetworkingCheckpoints order.
type SummaryInput struct {
	Description string
	Closed      []bool
}

// SummarizeFunc summarizes one chat's transcript into the summary input.
// `subject` is the user the summary is FOR (the requester): the grading and
// the description judge the conversation from their perspective. It is
// called only when that direction has no stored summary yet.
type SummarizeFunc func(ctx context.Context, chat model.Chat, msgs []model.Message, subject string) (SummaryInput, error)

// HardMatch runs the hard match of two personas inside one room:
//
//  1. both users must live in the room and differ (ErrNotFound / ErrInvalid);
//
//  2. the pair must have a chat (created once both handshake directions
//     exist) - ErrNotFound otherwise;
//
//  3. a stored summary FOR THIS DIRECTION (first -> second) returns as-is
//     (created=false; the method runs only once per direction — the
//     reverse direction is a separate record);
//
//  4. otherwise the chat transcript is summarized, the closed count is
//     merged with the time-independent match score (MatchPointsV2) and the
//     record is stored under the requester's direction:
//
//     points = 16 * closed + 0.52 * MatchV2
//
//     with MatchV2 = 100 * 2 * exactInterestMatches /
//     (interests of the first + interests of the second) — the pure
//     matching-interest percent, no time and no handshakes. Three closed
//     criteria add 48, a full MatchV2 adds 52, so the merged score spans
//     (0, 100) and is pinned into the open interval by the summaries table
//     CHECK constraint.
func (d *DB) HardMatch(ctx context.Context, roomAddress, first, second string, summarize SummarizeFunc) (model.Summary, bool, error) {
	if first == second {
		return model.Summary{}, false, errors.Join(ErrInvalid,
			errors.New("first_user_address and second_user_address must differ"))
	}

	// ---- read phase: validation, chat, match-v2 score, existing summary ----
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return model.Summary{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	// The merge input is the PUBLIC match v2 formula (feature #43): the
	// pure matching-interest percent, no time and no handshakes. Computed
	// inline on the read transaction (MatchPointsV2 itself opens one, and
	// the single-connection pool would deadlock on the nested call).
	for _, addr := range []string{first, second} {
		ok, err := d.userInRoomOn(ctx, tx, roomAddress, addr)
		if err != nil {
			return model.Summary{}, false, err
		}
		if !ok {
			return model.Summary{}, false, errors.Join(ErrNotFound,
				fmt.Errorf("user %q not found in room %q", addr, roomAddress))
		}
	}
	interestsA, interestsB, overlap, err := interestOverlapOn(ctx, tx, MatchResult{}, first, second)
	if err != nil {
		return model.Summary{}, false, err
	}
	total := len(interestsA) + len(interestsB)
	v2 := 0.0
	if total > 0 {
		v2 = math.Round(100*(2*float64(overlap.ExactMatches)/float64(total))*100) / 100
	}

	chat, found, err := findChat(ctx, tx, first, second)
	if err != nil {
		return model.Summary{}, false, err
	}
	if !found {
		return model.Summary{}, false, errors.Join(ErrNotFound, fmt.Errorf(
			"chat between %q and %q not found: handshakes in both directions are required", first, second))
	}

	if existing, found, err := summaryByDirectionOn(ctx, tx, first, second); err != nil {
		return model.Summary{}, false, err
	} else if found {
		return existing, false, tx.Commit()
	}
	if err := tx.Commit(); err != nil {
		return model.Summary{}, false, err
	}

	// ---- summarize (LLM work happens outside any transaction) ----
	msgs, err := d.ListChatMessages(ctx, chat.Address)
	if err != nil {
		return model.Summary{}, false, err
	}
	if summarize == nil {
		return model.Summary{}, false, errors.Join(ErrInvalid, errors.New("no summarizer configured"))
	}
	in, err := summarize(ctx, chat, msgs, first)
	if err != nil {
		return model.Summary{}, false, err
	}

	// ---- write phase: re-check (idempotent) and store ----
	wtx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return model.Summary{}, false, err
	}
	defer func() { _ = wtx.Rollback() }()

	if existing, found, err := summaryByDirectionOn(ctx, wtx, first, second); err != nil {
		return model.Summary{}, false, err
	} else if found {
		return existing, false, wtx.Commit()
	}

	summary, err := buildSummary(chat, first, second, v2, in)
	if err != nil {
		return model.Summary{}, false, err
	}
	checklist, err := json.Marshal(in.Closed)
	if err != nil {
		return model.Summary{}, false, err
	}
	if _, err := wtx.ExecContext(ctx, `
		INSERT INTO summaries (chat, room, user_a, user_b, score, checklist, description, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		summary.Chat, summary.Room, summary.UserA, summary.UserB, summary.Score,
		string(checklist), summary.Description,
		summary.CreatedAt.Format(time.RFC3339)); err != nil {
		// A concurrent hard match of the same direction won the race:
		// return its record (the method stays once-only per direction
		// even under concurrency).
		if isUnique(err) {
			if existing, found, err2 := summaryByDirectionOn(ctx, wtx, first, second); err2 == nil && found {
				return existing, false, wtx.Commit()
			}
		}
		return model.Summary{}, false, err
	}
	return summary, true, wtx.Commit()
}

// buildSummary assembles the stored summary record: the DIRECTIONAL pair
// (user_a = the requester the summary is for, user_b = the other side), the
// merged score and the rehydrated checkpoint list. The description is
// capped at MaxSummaryDescriptionLen symbols.
//
// The merged score (feature #43):
//
//	points = 16 * closed + 0.52 * MatchV2
//
// with MatchV2 = 100 * 2 * exactInterestMatches / (interests of the first +
// interests of the second) — the pure matching-interest percent, no time
// and no handshakes. Three closed criteria add 48, a full MatchV2 adds 52;
// the result is rounded to 2 decimals and pinned into the open interval
// (0, 100) — the summaries table CHECK constraint enforces it.
func buildSummary(chat model.Chat, first, second string, matchV2 float64, in SummaryInput) (model.Summary, error) {
	checkpoints := model.NetworkingCheckpoints(chat.Language)
	// Normalize the closed flags against the canonical checklist: missing
	// entries count as closed=false, extra entries are ignored.
	closedCount := 0
	for i := range checkpoints {
		if i < len(in.Closed) && in.Closed[i] {
			checkpoints[i].Closed = true
			closedCount++
		}
	}
	merged := summaryItemPoints*float64(closedCount) + summaryMatchShare*matchV2
	merged = math.Round(merged*100) / 100
	merged = clampOpen(merged)

	description := in.Description
	if n := utf8.RuneCountInString(description); n > model.MaxSummaryDescriptionLen {
		description = string([]rune(description)[:model.MaxSummaryDescriptionLen])
	}

	return model.Summary{
		Chat:        chat.Address,
		Room:        chat.Room,
		UserA:       first,  // the requester: the summary is FOR this user
		UserB:       second, // the other side of the conversation
		Score:       merged,
		Checkpoints: checkpoints,
		Description: description,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

// summaryByDirectionOn loads the stored summary of ONE direction
// (user_a = requester, user_b = the other side), on the given executor.
// The reverse direction is a separate record. The bool reports whether one
// exists.
func summaryByDirectionOn(ctx context.Context, q rowQuerier, first, second string) (model.Summary, bool, error) {
	row := q.QueryRowContext(ctx, `
SELECT s.chat, s.room, s.user_a, s.user_b, s.score, s.checklist, s.description, s.created_at,
       (SELECT language FROM chats c WHERE c.address = s.chat)
FROM summaries s WHERE s.user_a = ? AND s.user_b = ?`, first, second)

	var (
		s         model.Summary
		createdAt string
		checklist string
		score     float64
		language  string
	)
	err := row.Scan(&s.Chat, &s.Room, &s.UserA, &s.UserB, &score, &checklist, &s.Description, &createdAt, &language)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Summary{}, false, nil
	}
	if err != nil {
		return model.Summary{}, false, err
	}

	s.Score = score
	s.CreatedAt = parseTS(createdAt)
	var closed []bool
	_ = json.Unmarshal([]byte(checklist), &closed) // an empty/legacy checklist reads as all-false
	s.Checkpoints = model.NetworkingCheckpoints(language)
	for i := range s.Checkpoints {
		if i < len(closed) {
			s.Checkpoints[i].Closed = closed[i]
		}
	}
	return s, true, nil
}
