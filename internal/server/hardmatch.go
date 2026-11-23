// Hard matching endpoint (feature #43): the per-direction summarized match
// of a user pair.
package server

import (
	"context"
	"net/http"

	"github.com/extube/bleak-node/internal/agent"
	"github.com/extube/bleak-node/internal/db"
	"github.com/extube/bleak-node/internal/model"
)

// ChatSummarizer produces the hard-match summary input of one chat from
// the subject's perspective. The agent package's Summarizer (LLM-backed,
// heuristic fallback) satisfies it.
type ChatSummarizer interface {
	SummarizeChat(ctx context.Context, chat model.Chat, msgs []model.Message, subject string) (db.SummaryInput, error)
}

// HardMatchRequest is the body for POST /api/hard-match.
type HardMatchRequest struct {
	RoomAddress       string `json:"room_address" example:"0xroom123"`
	FirstUserAddress  string `json:"first_user_address" example:"0xuser123"`
	SecondUserAddress string `json:"second_user_address" example:"0xuser456"`
}

// hardMatchAdapter adapts the summarizer to the db.SummarizeFunc callback
// shape. No summarizer wired (tests): the agent summarizer's zero value
// runs its deterministic heuristic instead of calling an LLM.
func (s *Server) hardMatchSummarizer() db.SummarizeFunc {
	return func(ctx context.Context, chat model.Chat, msgs []model.Message, subject string) (db.SummaryInput, error) {
		if s.summarizer != nil {
			return s.summarizer.SummarizeChat(ctx, chat, msgs, subject)
		}
		summ := agent.Summarizer{} // heuristic-only fallback
		return summ.SummarizeChat(ctx, chat, msgs, subject)
	}
}

// HardMatch runs the hard match of two personas: it summarizes their chat
// FROM THE FIRST USER'S PERSPECTIVE, grades the networking checklist and
// merges the result with the time-independent match score. The summary is
// PER DIRECTION: a summary for A -> B exists without a B -> A record —
// the reverse is a separate request. Requires the FIRST persona's Bearer
// token; both users must live in the room and their chat must exist.
//
//	@Summary		hard match of two users (per direction)
//	@Description	Summarizes the chat FROM THE FIRST USER'S PERSPECTIVE (the subject: how good the networking experience was for them, how useful the learned information is for their future aims and interests growth), grades the 3 strict networking criteria (shared_idea — the subject left the conversation with a new idea or insight worth developing further; expertise_exchange — each side learned something concrete about the other's work, skills or projects; reciprocal_dialog — both sides asked follow-up questions and contributed roughly equally) and merges the closed count with the time-independent match score: points = 16 * closed + 0.52 * MatchV2, where MatchV2 = 100 * 2 * exact_interest_matches / (interests of the first + interests of the second) — the pure matching-interest percent of POST /api/match/v2 (no time, no handshakes). Three closed criteria add 48, a full MatchV2 adds 52; the result is pinned strictly inside (0, 100). The summary is stored PER DIRECTION (user_a = requester): a repeated call for the same direction returns the stored record without re-summarizing; the reverse direction (B -> A) is a separate record created by its own request. Requires the FIRST persona's Bearer token; both users must live in the room and their chat must exist.
//	@Tags			matching
//	@Security		UserToken
//	@Accept			json
//	@Produce		json
//	@Param			request	body	HardMatchRequest	true	"Hard match request"
//	@Success		200	{object}	model.Summary
//	@Success		201	{object}	model.Summary
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/hard-match [post]
func (s *Server) HardMatch(w http.ResponseWriter, r *http.Request) {
	var req HardMatchRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if err := validateAddressArgs(req.RoomAddress, req.FirstUserAddress, req.SecondUserAddress); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if req.FirstUserAddress == req.SecondUserAddress {
		respond(w, http.StatusBadRequest, "first_user_address and second_user_address must differ")
		return
	}
	// The match is computed AS the first persona: its token is required,
	// scoped to the room both users must live in.
	if !s.authUserToken(w, r, req.RoomAddress, req.FirstUserAddress) {
		return
	}

	summary, created, err := s.db.HardMatch(r.Context(),
		req.RoomAddress, req.FirstUserAddress, req.SecondUserAddress, s.hardMatchSummarizer())
	if err != nil {
		s.mapError(w, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, summary)
}
