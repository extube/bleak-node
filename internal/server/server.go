// Package server wires the REST API handlers to the router.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/extube/bleak-node/internal/db"
	"github.com/swaggo/http-swagger"
)

// Server holds shared dependencies for all handlers.
type Server struct {
	db      *db.DB
	greeter Greeter
	// summarizer powers the hard match (feature #35); nil keeps the
	// deterministic heuristic fallback.
	summarizer ChatSummarizer
	// joinLimiter rate-limits join/invite-code attempts per source (the
	// 6-digit code space is brute-forceable; CONCEPT.md §7.4).
	joinLimiter *rateLimiter
}

// Greeter generates a user's greeting (hello) message from their bios.
// The greeting opens every new chat of the user, so it never carries
// questions; implementations fall back to a plain template when the
// generator is unavailable.
type Greeter interface {
	GenerateGreeting(name, publicBio, privateBio, language string) string
}

// joinAttemptLimit is the per-source sliding-window limit for room-join
// (invite-code) attempts.
const joinAttemptLimit = 10

// joinAttemptWindow is that limit's rolling window.
const joinAttemptWindow = time.Minute

// New creates a server with its routing table already registered. The
// greeter may be nil: user creation then falls back to the plain
// bio-based greeting.
func New(database *db.DB, greeter Greeter) *Server {
	return &Server{
		db:          database,
		greeter:     greeter,
		joinLimiter: newRateLimiter(joinAttemptLimit, joinAttemptWindow),
	}
}

// WithSummarizer wires the hard-match chat summarizer (feature #35).
// Without it the hard match falls back to the deterministic heuristic
// grading.
func (s *Server) WithSummarizer(summarizer ChatSummarizer) *Server {
	s.summarizer = summarizer
	return s
}

// MatchPointsV2 computes the time-independent matching score of two
// personas: the same pair always grades to the same points, which stay
// strictly inside (0, 100) - 0 and 100 are impossible.
//
//	@Summary		time-independent matching score of two users
//	@Description	Computes the matching points of two personas inside one room WITHOUT any time input: points = 50 + 50 * tanh(coef / 12), where the coefficient adds +4 per handshake direction (a mutual pair counts both directions - there is no instant 100), +2 per shared interest group and +5 per exact interest match (same group AND same key). The score is always strictly inside (0, 100): 0 and 100 are impossible, and the same pair always grades to the same value. Difficulty calibration: a FULL match (> 80) is rare - a mutual handshake alone stays below the line (79.14) and only pairs with real shared interests cross it, so roughly one of five random pairs reaches the zone. Requires the FIRST persona's Bearer token; both users must live in the room.
//	@Tags			matching
//	@Security		UserToken
//	@Accept			json
//	@Produce		json
//	@Param			request	body	MatchV2Request	true	"Match request"
//	@Success		200	{object}	db.MatchResult
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/match/v2 [post]
func (s *Server) MatchPointsV2(w http.ResponseWriter, r *http.Request) {
	var req MatchV2Request
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
	if !s.authUserToken(w, r, req.RoomAddress, req.FirstUserAddress) {
		return
	}

	res, err := s.db.MatchPointsV2(r.Context(),
		req.RoomAddress, req.FirstUserAddress, req.SecondUserAddress)
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// MatchV2Request is the body for POST /api/match/v2. Unlike the legacy
// match it carries no time: the formula is time-independent.
type MatchV2Request struct {
	RoomAddress       string `json:"room_address" example:"0xroom123"`
	FirstUserAddress  string `json:"first_user_address" example:"0xuser123"`
	SecondUserAddress string `json:"second_user_address" example:"0xuser456"`
}

// Handler builds and returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Rooms.
	mux.HandleFunc("POST /api/room", s.CreateRoom)
	mux.HandleFunc("GET /api/room", s.ListRooms)
	mux.HandleFunc("GET /api/room/{address}", s.GetRoom)
	mux.HandleFunc("PUT /api/room/{address}", s.UpdateRoom)
	mux.HandleFunc("DELETE /api/room/{address}", s.DeleteRoom)

	// Users living in a room.
	mux.HandleFunc("POST /api/room/{room}/user", s.CreateUser)
	mux.HandleFunc("GET /api/room/{room}/user", s.ListRoomUsers)
	mux.HandleFunc("GET /api/room/{room}/user/{address}", s.GetUser)
	mux.HandleFunc("PUT /api/room/{room}/user/{address}", s.UpdateUser)
	mux.HandleFunc("DELETE /api/room/{room}/user/{address}", s.DeleteUser)
	mux.HandleFunc("GET /api/room/{room}/user/{address}/greeting", s.GetUserGreeting)
	mux.HandleFunc("PUT /api/room/{room}/user/{address}/greeting", s.PutUserGreeting)
	mux.HandleFunc("POST /api/room/{room}/user/{address}/greeting/regenerate", s.RegenerateUserGreeting)

	// Interests & matching score (feature #34): bulk add/delete of a
	// persona's interests, matching score between two personas.
	mux.HandleFunc("POST /api/room/{room}/user/{address}/interest", s.AddUserInterests)
	mux.HandleFunc("DELETE /api/room/{room}/user/{address}/interest", s.DeleteUserInterests)
	mux.HandleFunc("POST /api/match", s.MatchPoints)
	// Hard matching (feature #35): the time-independent score and the
	// once-only summarized match of a user pair.
	mux.HandleFunc("POST /api/match/v2", s.MatchPointsV2)
	mux.HandleFunc("POST /api/hard-match", s.HardMatch)

	// Shared contacts (feature #35): one share per pair, recipient-only
	// reads.
	mux.HandleFunc("POST /api/contact", s.ShareContact)
	mux.HandleFunc("GET /api/contact/{address}", s.ListContacts)

	// Ownership credentials (feature #33): token rotation, mutual codes,
	// invite management, join-code resolution.
	mux.HandleFunc("POST /api/room/{room}/user/{address}/token", s.RotateUserToken)
	mux.HandleFunc("POST /api/room/{room}/user/{address}/mutual-code", s.SetMutualCode)
	mux.HandleFunc("POST /api/room/{address}/token", s.RotateRoomToken)
	mux.HandleFunc("POST /api/room/{address}/invite", s.CreateRoomInvite)
	mux.HandleFunc("GET /api/room/{address}/invite", s.ListRoomInvites)
	mux.HandleFunc("DELETE /api/room/{address}/invite/{code}", s.RevokeRoomInvite)
	mux.HandleFunc("GET /api/invite/{code}", s.ResolveInvite)

	// Handshakes.
	mux.HandleFunc("POST /api/handshake", s.CreateHandshake)
	mux.HandleFunc("GET /api/handshake/user/{address}", s.ListUserHandshakes)
	mux.HandleFunc("POST /api/handshake/mutual", s.CreateMutualHandshake)

	// Chats & messages.
	mux.HandleFunc("POST /api/message", s.SendMessage)
	mux.HandleFunc("GET /api/user/{address}/chat", s.ListUserChats)
	mux.HandleFunc("GET /api/room/{room}/user/{address}/chat", s.ListRoomUserChats)
	mux.HandleFunc("GET /api/room/{room}/user/{address}/chat/{chat}/messages", s.ListRoomUserChatMessages)
	mux.HandleFunc("GET /api/chat/{address}", s.GetChat)
	mux.HandleFunc("DELETE /api/chat/{address}/message", s.ClearChatMessages)
	mux.HandleFunc("POST /api/chat/{address}/kickoff", s.ChatKickoff)
	mux.HandleFunc("GET /api/chat/{address}/message", s.ListChatMessages)

	// Swagger UI.
	mux.Handle("GET /swagger-api/", swaggerHandler())

	return middleware(mux)
}

// middleware wraps the router with cross-cutting behavior (content-type, logging).
func middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next.ServeHTTP(w, r)
	})
}

// swaggerHandler returns the Swagger UI bound under /swagger-api/.
func swaggerHandler() http.Handler {
	return httpSwagger.Handler(httpSwagger.URL("/swagger-api/doc.json"))
}

// ---- JSON helpers ----

// decodeJSON parses a strict request body into dst.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.Join(errInvalidInput, fmt.Errorf("invalid JSON body: %s", err))
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.WriteHeader(code)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func respond(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ---- sentinel error classification ----

var errInvalidInput = errors.New("invalid input")

// mapError converts persistence-layer errors into HTTP responses.
func (s *Server) mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidInput), errors.Is(err, db.ErrInvalid):
		respond(w, http.StatusBadRequest, message(err))
	case errors.Is(err, db.ErrNotFound):
		respond(w, http.StatusNotFound, message(err))
	case errors.Is(err, db.ErrConflict):
		respond(w, http.StatusConflict, message(err))
	case errors.Is(err, db.ErrBalance):
		respond(w, http.StatusForbidden, message(err))
	case errors.Is(err, db.ErrForbidden):
		respond(w, http.StatusForbidden, message(err))
	case errors.Is(err, db.ErrGone):
		respond(w, http.StatusGone, message(err))
	default:
		respond(w, http.StatusInternalServerError, err.Error())
	}
}

// message renders an error (possibly an errors.Join chain) on a single line.
func message(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", ": ")
}
