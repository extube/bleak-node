// Hard matching endpoint (feature #35): the once-only summarized match of
// a user pair.
package server

import (
	"context"
	"net/http"

	"github.com/extube/bleak-node/internal/agent"
	"github.com/extube/bleak-node/internal/db"
	"github.com/extube/bleak-node/internal/model"
)

// ChatSummarizer produces the hard-match summary input of one chat. The
// agent package's Summarizer (LLM-backed, heuristic fallback) satisfies it.
type ChatSummarizer interface {
	SummarizeChat(ctx context.Context, chat model.Chat, msgs []model.Message) (db.SummaryInput, error)
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
	return func(ctx context.Context, chat model.Chat, msgs []model.Message) (db.SummaryInput, error) {
		if s.summarizer != nil {
			return s.summarizer.SummarizeChat(ctx, chat, msgs)
		}
		summ := agent.Summarizer{} // heuristic-only fallback
		return summ.SummarizeChat(ctx, chat, msgs)
	}
}

// HardMatch runs the hard match of two personas: it summarizes their chat,
// grades the networking checklist, merges the result with the
// time-independent match score and stores ONE summary per pair. A repeated
// call for the same pair returns the stored summary. Requires the FIRST
// persona's Bearer token; both users must live in the room and their chat
// must exist.
//
//	@Summary		hard match of two users
//	@Description	Summarizes the chat of the pair, grades the networking checklist (both introduced themselves, found a shared topic, balanced turns, questions answered, warm farewell), merges the checkpoint result with the time-independent match score (POST /api/match/v2) and stores one Summary per pair. The method activates only once: a repeated call for a pair that already has a summary returns the stored record without re-summarizing. Handshakes impact NO value: the method is only reachable for pairs with a mutual handshake anyway (the chat requires both directions), so the soft score entering the merge is driven by shared interests alone. The merged score stays strictly inside (0, 100) and a full match (> 80) is rare: hard = 50 + 50 * tanh((closed - 3) / 2) keeps an average 3/5 chat at the neutral 50 and a perfect 5/5 conversation at 88.08, so the merged score only crosses 80 when the interest-driven soft score is strong as well. Requires the FIRST persona's Bearer token; both users must live in the room and their chat must exist.
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
