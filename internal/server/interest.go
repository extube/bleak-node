// Interest endpoints (feature #34): bulk add / bulk delete of a persona's
// interests and the matching score between two personas.
package server

import (
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/extube/bleak-node/internal/model"
)

// InterestInput is one client-supplied interest row (the user_address is
// taken from the route, never from the body).
type InterestInput struct {
	Title string `json:"title" example:"Chess"`
	Group int    `json:"group" example:"1"`
	Key   int    `json:"key" example:"7"`
}

// AddInterestsRequest is the body for POST .../interest.
type AddInterestsRequest struct {
	Interests []InterestInput `json:"interests"`
}

// DeleteInterestsRequest is the body for DELETE .../interest.
type DeleteInterestsRequest struct {
	Keys []int `json:"keys" example:"1,2,3"`
}

// MatchRequest is the body for POST /api/match. It carries no time: the
// formula's t is computed server-side as the hours since the FIRST
// persona's last sent handshake (since their creation when they never sent
// one) — feature #38.
type MatchRequest struct {
	RoomAddress       string `json:"room_address" example:"0xroom123"`
	FirstUserAddress  string `json:"first_user_address" example:"0xuser123"`
	SecondUserAddress string `json:"second_user_address" example:"0xuser456"`
}

// AddUserInterests adds (upserts) many interests for a user.
//
//	@Summary		add interests of a user
//	@Description	Upserts many interests for a non-deleted user inside the room. Requires that persona's Bearer token. Each interest carries a title (1..64 symbols), a numeric group (the category) and a numeric key (the concrete interest); the key is unique per user, so re-adding an existing key replaces the title/group. The list holds 1..50 rows. Responds 201 with the user's full resulting list.
//	@Tags			interests
//	@Security		UserToken
//	@Accept			json
//	@Produce		json
//	@Param			room		path	string					true	"Room address"
//	@Param			address		path	string					true	"User address"
//	@Param			request		body	AddInterestsRequest		true	"Interests to add"
//	@Success		201	{array}	model.Interest
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/room/{room}/user/{address}/interest [post]
func (s *Server) AddUserInterests(w http.ResponseWriter, r *http.Request) {
	room, address := r.PathValue("room"), r.PathValue("address")
	if err := validateAddressArgs(room, address); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}

	var req AddInterestsRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if n := len(req.Interests); n == 0 || n > model.MaxInterestsPerRequest {
		respond(w, http.StatusBadRequest,
			"interests must hold 1.."+itoa(model.MaxInterestsPerRequest)+" rows")
		return
	}
	for _, in := range req.Interests {
		if n := utf8.RuneCountInString(in.Title); n == 0 || n > model.MaxInterestTitleLen {
			respond(w, http.StatusBadRequest,
				"interest title must be 1.."+itoa(model.MaxInterestTitleLen)+" symbols")
			return
		}
		if in.Group < 0 || in.Key < 0 {
			respond(w, http.StatusBadRequest, "interest group and key must be >= 0")
			return
		}
	}
	if !s.authUserToken(w, r, room, address) {
		return
	}

	list := make([]model.Interest, 0, len(req.Interests))
	for _, in := range req.Interests {
		list = append(list, model.Interest{Title: in.Title, Group: in.Group, Key: in.Key})
	}
	res, err := s.db.AddUserInterests(r.Context(), room, address, list)
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// DeleteUserInterests deletes many interests of a user (by their keys).
//
//	@Summary		delete interests of a user
//	@Description	Deletes the interests with the given keys of a non-deleted user inside the room. Requires that persona's Bearer token. The list holds 1..50 keys; unknown keys are ignored (idempotent delete). Responds 200 with the user's remaining interests.
//	@Tags			interests
//	@Security		UserToken
//	@Accept			json
//	@Produce		json
//	@Param			room		path	string						true	"Room address"
//	@Param			address		path	string						true	"User address"
//	@Param			request		body	DeleteInterestsRequest		true	"Interest keys to delete"
//	@Success		200	{array}	model.Interest
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/room/{room}/user/{address}/interest [delete]
func (s *Server) DeleteUserInterests(w http.ResponseWriter, r *http.Request) {
	room, address := r.PathValue("room"), r.PathValue("address")
	if err := validateAddressArgs(room, address); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}

	var req DeleteInterestsRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if n := len(req.Keys); n == 0 || n > model.MaxInterestsPerRequest {
		respond(w, http.StatusBadRequest,
			"keys must hold 1.."+itoa(model.MaxInterestsPerRequest)+" entries")
		return
	}
	if !s.authUserToken(w, r, room, address) {
		return
	}

	_, remaining, err := s.db.DeleteUserInterests(r.Context(), room, address, req.Keys)
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, remaining)
}

// MatchPoints computes the matching score of two personas.
//
//	@Summary		matching score of two users
//	@Description	Computes the matching points (0..100) of two personas inside one room: points = 100 * (1 - 1/(C * 2^sqrt(t))) with t the hours (float) since the FIRST persona's last sent handshake in the room (since their creation when they never sent one; computed server-side; the request carries no time). C = 1 + handshake directions (+1 per directed handshake between the two in the room) + the matching-interest percent (2 * exact interest matches / the interests total of both personas; every match is the same group AND key, counted on both sides — identical interest sets grade a full 100 %). The square root makes the score grow fast in the first hours and slower afterwards. Requires the FIRST persona's Bearer token; both users must live in the room.
//	@Tags			matching
//	@Security		UserToken
//	@Accept			json
//	@Produce		json
//	@Param			request	body	MatchRequest	true	"Match request"
//	@Success		200	{object}	db.MatchResult
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/match [post]
func (s *Server) MatchPoints(w http.ResponseWriter, r *http.Request) {
	var req MatchRequest
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
	// The score is computed AS the first persona: its token is required,
	// scoped to the room both users must live in.
	if !s.authUserToken(w, r, req.RoomAddress, req.FirstUserAddress) {
		return
	}

	res, err := s.db.MatchPoints(r.Context(),
		req.RoomAddress, req.FirstUserAddress, req.SecondUserAddress)
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// validateAddressArgs rejects empty or oversized address arguments (route
// params and body addresses share the handshake maxAddressLen cap).
func validateAddressArgs(addrs ...string) error {
	for _, a := range addrs {
		if a == "" {
			return errors.New("address is required")
		}
		if utf8.RuneCountInString(a) > maxAddressLen {
			return errors.New("address is too long")
		}
	}
	return nil
}

// itoa is a tiny strconv.Itoa alias for the message strings above.
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
