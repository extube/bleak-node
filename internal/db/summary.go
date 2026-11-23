// Hard matching (feature #35): the summarized, once-only record of a user
// pair. The method sums up the pair's chat, grades the networking
// checklist, merges the checkpoint result with the time-independent match
// score (POST /api/match/v2) and stores ONE summary per pair. A repeated
// hard-match request returns the stored record instead of re-summarizing.
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

// HardCheckpointCenter and HardCheckpointSpread shape the checkpoint -> hard
// score curve (see buildSummary): center = the closed count that grades as
// the neutral midpoint, spread = how sharply the score reacts to better or
// worse conversations.
const (
	HardCheckpointCenter = 3.0
	HardCheckpointSpread = 2.0
)

// SummaryInput is what a chat summarizer feeds into the hard match: the
// plain-text summarization and the closed flags aligned with the canonical
// model.NetworkingCheckpoints order.
type SummaryInput struct {
	Description string
	Closed      []bool
}

// SummarizeFunc summarizes one chat's transcript into the summary input.
// It is called only when the pair has no stored summary yet.
type SummarizeFunc func(ctx context.Context, chat model.Chat, msgs []model.Message) (SummaryInput, error)

// HardMatch runs the hard match of two personas inside one room:
//
//  1. both users must live in the room and differ (ErrNotFound / ErrInvalid);
//  2. the pair must have a chat (created once both handshake directions
//     exist) - ErrNotFound otherwise;
//  3. a stored summary returns as-is (created=false; the method runs only
//     once per pair);
//  4. otherwise the chat transcript is summarized, the checkpoint result is
//     merged with the soft (time-independent) score and the record is
//     stored. The merged score:
//
//     points = round2((soft + hard) / 2),  hard = 100 * closed / checkpoints
//
//     soft is always strictly inside (0, 100), so the merged value is too;
//     the summaries table CHECK constraint enforces the open interval.
func (d *DB) HardMatch(ctx context.Context, roomAddress, first, second string, summarize SummarizeFunc) (model.Summary, bool, error) {
	if first == second {
		return model.Summary{}, false, errors.Join(ErrInvalid,
			errors.New("first_user_address and second_user_address must differ"))
	}

	// ---- read phase: validation, chat, soft score, existing summary ----
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return model.Summary{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	// Handshakes weigh in NOTHING here (owner directive): hard matching is
	// only reachable for pairs with a mutual handshake anyway (the chat
	// requires both directions), so handshake points would be a constant
	// +8 on every summary. The soft score entering the merge is driven by
	// shared interests alone; countHandshakes=false.
	soft, err := matchSoftOn(ctx, tx, roomAddress, first, second, false)
	if err != nil {
		return model.Summary{}, false, err
	}

	chat, found, err := findChat(ctx, tx, first, second)
	if err != nil {
		return model.Summary{}, false, err
	}
	if !found {
		return model.Summary{}, false, errors.Join(ErrNotFound, fmt.Errorf(
			"chat between %q and %q not found: handshakes in both directions are required", first, second))
	}

	if existing, found, err := summaryByPairOn(ctx, tx, chat.Address); err != nil {
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
	in, err := summarize(ctx, chat, msgs)
	if err != nil {
		return model.Summary{}, false, err
	}

	// ---- write phase: re-check (idempotent) and store ----
	wtx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return model.Summary{}, false, err
	}
	defer func() { _ = wtx.Rollback() }()

	if existing, found, err := summaryByPairOn(ctx, wtx, chat.Address); err != nil {
		return model.Summary{}, false, err
	} else if found {
		return existing, false, wtx.Commit()
	}

	summary, err := buildSummary(chat, first, second, soft, in)
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
		// A concurrent hard match of the same pair won the race: return its
		// record (the method stays once-only even under concurrency).
		if isUnique(err) {
			if existing, found, err2 := summaryByPairOn(ctx, wtx, chat.Address); err2 == nil && found {
				return existing, false, wtx.Commit()
			}
		}
		return model.Summary{}, false, err
	}
	return summary, true, wtx.Commit()
}

// buildSummary assembles the stored summary record: the canonical
// (user_a < user_b) pair, the merged score and the rehydrated checkpoint
// list. The description is capped at MaxSummaryDescriptionLen symbols.
//
// The checkpoint result maps to the hard score with the same difficulty
// spirit as the soft score (owner directive: a full match > 80 must stay
// rare, ~20% of pairs):
//
//	hard = 50 + 50 * tanh((closed - HardCheckpointCenter) / HardCheckpointSpread)
//
// An average chat (3 of 5 checkpoints closed) stays at the neutral 50; a
// perfect 5/5 conversation tops out at 88.08 instead of an instant 100, so
// the merged score only crosses 80 when the soft score is strong as well.
func buildSummary(chat model.Chat, first, second string, soft MatchResult, in SummaryInput) (model.Summary, error) {
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
	hard := 50 + 50*math.Tanh(
		(float64(closedCount)-HardCheckpointCenter)/HardCheckpointSpread)
	merged := math.Round((soft.Points+hard)/2*100) / 100
	merged = clampOpen(merged)

	description := in.Description
	if n := utf8.RuneCountInString(description); n > model.MaxSummaryDescriptionLen {
		description = string([]rune(description)[:model.MaxSummaryDescriptionLen])
	}

	a, b := first, second
	if b < a {
		a, b = b, a
	}
	return model.Summary{
		Chat:        chat.Address,
		Room:        chat.Room,
		UserA:       a,
		UserB:       b,
		Score:       merged,
		Checkpoints: checkpoints,
		Description: description,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

// summaryByPairOn loads the stored summary of a pair (canonical a < b or
// the reverse), on the given executor. The bool reports whether one exists.
func summaryByPairOn(ctx context.Context, q rowQuerier, chatAddress string) (model.Summary, bool, error) {
	row := q.QueryRowContext(ctx, `
SELECT s.chat, s.room, s.user_a, s.user_b, s.score, s.checklist, s.description, s.created_at,
       (SELECT language FROM chats c WHERE c.address = s.chat)
FROM summaries s WHERE s.chat = ?`, chatAddress)

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
