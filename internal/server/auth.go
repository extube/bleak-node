// Bearer-token authentication for the ownership-token mechanism
// (CONCEPT.md §7.1). Every helper writes the error response itself and
// returns false when the caller must stop.
//
// Status semantics: 401 when the credential is missing or matches no
// record at all; 403 when the presented token is valid for SOME record but
// not for the addressed one (wrong-record/rule). Errors follow the house
// JSON style. All hash comparisons are constant-time.
package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/extube/bleak-node/internal/db"
)

// createTokenHeader is the header carrying the one-time room-creation token
// (bleak-web's proposed name, confirmed by WP-6 contract work).
const createTokenHeader = "x-create-token"

// bearerToken extracts the raw token from `Authorization: Bearer <token>`;
// empty when the header is missing or malformed.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// hashEqual compares two stored SHA-256 hex hashes in constant time.
func hashEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Token length bounds for client-generated secrets (persona, room):
// CONCEPT.md §7.4 recommends ≥ 32 random bytes base64url; the server
// enforces the floor and a sane maximum.
const (
	minTokenLen = 32
	maxTokenLen = 255
)

// validateToken checks a client-generated raw token shape.
func validateToken(raw string) bool {
	n := utf8.RuneCountInString(raw)
	if n < minTokenLen || n > maxTokenLen {
		return false
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// unauthorized writes 401 (missing / unknown credential).
func unauthorized(w http.ResponseWriter, msg string) {
	respond(w, http.StatusUnauthorized, msg)
}

// forbidden writes 403 (valid credential, wrong record or rule).
func forbidden(w http.ResponseWriter, msg string) {
	respond(w, http.StatusForbidden, msg)
}

// rejectMissingToken answers 401 for an absent or malformed header.
func rejectMissingToken(w http.ResponseWriter) bool {
	unauthorized(w, "bearer token required")
	return false
}

// rejectWrongRecord classifies a failed comparison: 403 when the token
// belongs to another record, 401 when it matches nothing.
func (s *Server) rejectWrongRecord(w http.ResponseWriter, r *http.Request, token, what string) bool {
	if token != "" && s.tokenBelongsToAnyRecord(r, token) {
		forbidden(w, "token does not own this "+what)
		return false
	}
	unauthorized(w, "invalid token")
	return false
}

// tokenBelongsToAnyRecord reports whether the presented token's hash
// matches the token hash of ANY record (user or room). Used only on failed
// authentications to distinguish 401 from 403.
func (s *Server) tokenBelongsToAnyRecord(r *http.Request, token string) bool {
	h := db.HashToken(token)
	if _, ok, err := s.db.UserByTokenHash(r.Context(), h); err == nil && ok {
		return true
	}
	// Room hashes live on the rooms table: a full scan is acceptable at the
	// scale this deployment runs (test/event rooms), and the check runs
	// only on failed authentications.
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT token_hash FROM rooms WHERE token_hash IS NOT NULL AND deleted_at IS NULL`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var stored *string
		if err := rows.Scan(&stored); err != nil || stored == nil {
			continue
		}
		if hashEqual(h, *stored) {
			return true
		}
	}
	_ = rows.Err()
	return false
}

// storedTokenHashes loads the candidate credential hashes for one
// authentication decision: the addressed persona (room-scoped when a room
// is given) and, optionally, the room itself (6.9a / chat rules).
func (s *Server) storedTokenHashes(r *http.Request, room, address string, withRoom bool) (userHash, roomHash string, err error) {
	if room != "" {
		userHash, err = s.db.UserTokenHash(r.Context(), room, address)
	} else {
		userHash, err = s.db.UserTokenHashByAddress(r.Context(), address)
	}
	if err != nil {
		return "", "", err
	}
	if withRoom && room != "" {
		roomHash, err = s.db.RoomTokenHash(r.Context(), room)
		if err != nil {
			return "", "", err
		}
	}
	return userHash, roomHash, nil
}

// matchOne reports whether the presented hash matches any of the stored
// hashes (constant-time per comparison).
func matchOne(presented string, stored ...string) bool {
	for _, s := range stored {
		if s != "" && hashEqual(presented, s) {
			return true
		}
	}
	return false
}

// authPersonaToken checks the bearer token against the persona's token
// hash. room != "" scopes the lookup to that room; withRoom additionally
// accepts the room token as a valid credential for the action.
func (s *Server) authPersonaToken(w http.ResponseWriter, r *http.Request, room, address string, withRoom bool) bool {
	token := bearerToken(r)
	if token == "" {
		return rejectMissingToken(w)
	}
	userHash, roomHash, err := s.storedTokenHashes(r, room, address, withRoom)
	if err != nil {
		respond(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if matchOne(db.HashToken(token), userHash, roomHash) {
		return true
	}
	what := "persona"
	if withRoom {
		what = "persona or room"
	}
	return s.rejectWrongRecord(w, r, token, what)
}

// authUserToken checks the bearer token against the persona's token hash
// (room-scoped: the user must live in the room).
func (s *Server) authUserToken(w http.ResponseWriter, r *http.Request, room, address string) bool {
	return s.authPersonaToken(w, r, room, address, false)
}

// userTokenMatches quietly checks whether the presented bearer token is the
// persona's own token. It writes nothing: public endpoints use it to decide
// whether gated fields (private_bio, mutual_code_live) may be returned.
func (s *Server) userTokenMatches(r *http.Request, room, address string) bool {
	token := bearerToken(r)
	if token == "" {
		return false
	}
	stored, err := s.db.UserTokenHash(r.Context(), room, address)
	if err != nil || stored == "" {
		return false
	}
	return hashEqual(db.HashToken(token), stored)
}

// authUserTokenByAddress is authUserToken for routes without a room in the
// path (the persona is identified by address alone).
func (s *Server) authUserTokenByAddress(w http.ResponseWriter, r *http.Request, address string) bool {
	return s.authPersonaToken(w, r, "", address, false)
}

// authUserTokenOrRoomToken accepts EITHER the persona's token OR the room
// token (6.9a amendment: room-scoped per-user reads stay available to the
// host console).
func (s *Server) authUserTokenOrRoomToken(w http.ResponseWriter, r *http.Request, room, address string) bool {
	return s.authPersonaToken(w, r, room, address, true)
}

// authRoomToken checks the bearer token against the room's token hash
// (full room control).
func (s *Server) authRoomToken(w http.ResponseWriter, r *http.Request, address string) bool {
	token := bearerToken(r)
	if token == "" {
		return rejectMissingToken(w)
	}
	stored, err := s.db.RoomTokenHash(r.Context(), address)
	if err != nil {
		respond(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if stored != "" && hashEqual(db.HashToken(token), stored) {
		return true
	}
	return s.rejectWrongRecord(w, r, token, "room")
}

// authParticipantOrRoomToken accepts a participant persona's token OR the
// room token of the chat's room (chat reads/clears/kickoff).
func (s *Server) authParticipantOrRoomToken(w http.ResponseWriter, r *http.Request, chatAddress string) bool {
	token := bearerToken(r)
	if token == "" {
		return rejectMissingToken(w)
	}
	chat, found, err := s.db.ChatByAddress(r.Context(), chatAddress)
	if err != nil {
		respond(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !found {
		respond(w, http.StatusNotFound, "chat not found")
		return false
	}
	parts, err := s.db.ChatParticipants(r.Context(), chatAddress)
	if err != nil {
		respond(w, http.StatusInternalServerError, err.Error())
		return false
	}
	stored := make([]string, 0, len(parts)+1)
	for _, p := range parts {
		h, err := s.db.UserTokenHashByAddress(r.Context(), p.User)
		if err != nil {
			respond(w, http.StatusInternalServerError, err.Error())
			return false
		}
		stored = append(stored, h)
	}
	roomHash, err := s.db.RoomTokenHash(r.Context(), chat.Room)
	if err != nil {
		respond(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if matchOne(db.HashToken(token), append(stored, roomHash)...) {
		return true
	}
	return s.rejectWrongRecord(w, r, token, "chat")
}

// authCreateToken validates the one-time room-creation token and consumes
// it atomically. The token is burned on success; a body-validation failure
// afterwards still counts as a use.
func (s *Server) authCreateToken(w http.ResponseWriter, r *http.Request) bool {
	raw := r.Header.Get(createTokenHeader)
	if raw == "" {
		unauthorized(w, createTokenHeader+" header required")
		return false
	}
	h := db.HashToken(raw)
	exists, used, err := s.db.CreateTokenState(r.Context(), h)
	if err != nil {
		respond(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !exists {
		unauthorized(w, "invalid creation token")
		return false
	}
	if used {
		forbidden(w, "creation token already used")
		return false
	}
	ok, err := s.db.ConsumeCreateToken(r.Context(), h)
	if err != nil {
		respond(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !ok {
		forbidden(w, "creation token already used")
		return false
	}
	return true
}

// ---- per-source rate limiting (join / invite-code attempts) ----

// rateLimiter is a small sliding-window limiter keyed by source. It guards
// the brute-forceable 6-digit invite-code space (CONCEPT.md §7.4).
type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	limit    int
	window   time.Duration
	now      func() time.Time
}

// newRateLimiter allows `limit` attempts per source per rolling window.
func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		attempts: map[string][]time.Time{},
		limit:    limit,
		window:   window,
		now:      time.Now,
	}
}

// allow records an attempt and reports whether it is within the limit.
func (l *rateLimiter) allow(source string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	keep := now.Add(-l.window)
	fresh := l.attempts[source][:0]
	for _, t := range l.attempts[source] {
		if t.After(keep) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= l.limit {
		l.attempts[source] = fresh
		return false
	}
	l.attempts[source] = append(fresh, now)
	if len(l.attempts) > 10_000 { // bound the map; stale keys self-clean on use
		for k, v := range l.attempts {
			if len(v) == 0 || v[len(v)-1].Before(keep) {
				delete(l.attempts, k)
			}
		}
	}
	return true
}

// requestSource identifies the caller: remote address host (the server
// fronts no trusted proxy layer, so no X-Forwarded-For handling).
func requestSource(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimit responds 429 and returns false when the source is over the
// join/invite attempt limit.
func (s *Server) rateLimit(w http.ResponseWriter, r *http.Request, scope string) bool {
	if s.joinLimiter.allow(scope + ":" + requestSource(r)) {
		return true
	}
	respond(w, http.StatusTooManyRequests, "too many attempts, try again later")
	return false
}
