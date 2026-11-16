// Ownership-token endpoints (feature #33, CONCEPT.md §7.1–§7.4): token
// rotation, one-time mutual-code registration, the mutual-QR handshake,
// invite management and the public join-code resolution.
package server

import (
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/extube/bleak-node/internal/db"
	"github.com/extube/bleak-node/internal/model"
)

// mutualCodeHashRe is the SHA-256 hex form of a client-registered mutual
// code (the server never sees the raw code).
var mutualCodeHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ---- POST /api/handshake/mutual ----

// CreateMutualHandshakeRequest is the body for POST /api/handshake/mutual:
// the scanned persona's address and the RAW one-time code read from its QR.
type CreateMutualHandshakeRequest struct {
	UserAddress string `json:"user_address"`
	MutualCode  string `json:"mutual_code"`
}

// MutualHandshakeResponse reports the pair created (or re-confirmed) by the
// mutual-QR handshake.
type MutualHandshakeResponse struct {
	Handshakes []model.Handshake `json:"handshakes"`
	Chat       model.Chat        `json:"chat"`
}

// CreateMutualHandshake creates both directed handshakes for a QR scan.
//
//	@Summary		mutual handshake from QR scan
//	@Description	Verifies the scanned one-time mutual code against the target persona's stored hash; on a match the code is CONSUMED (burned) and both directed handshakes (scanner -> target and target -> scanner) are created idempotently, auto-creating the pair chat when new. One chat per pair: an already-mutual pair still burns the code but never creates a second chat. The room's income>outcome rule applies to the caller's own direction; a rejected attempt does NOT consume the code. Requires the SCANNER persona's Bearer token.
//	@Tags			handshakes
//	@Security		UserToken
//	@Accept			json
//	@Produce		json
//	@Param			request	body	CreateMutualHandshakeRequest	true	"Scanned persona address and the raw one-time mutual code"
//	@Success		201	{object}	MutualHandshakeResponse
//	@Success		200	{object}	MutualHandshakeResponse
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Failure		409	{object}	string
//	@Failure		410	{object}	string
//	@Router			/api/handshake/mutual [post]
func (s *Server) CreateMutualHandshake(w http.ResponseWriter, r *http.Request) {
	var req CreateMutualHandshakeRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if req.UserAddress == "" {
		respond(w, http.StatusBadRequest, "user_address is required")
		return
	}
	if utf8.RuneCountInString(req.UserAddress) > maxAddressLen {
		respond(w, http.StatusBadRequest, "user_address is too long")
		return
	}
	if req.MutualCode == "" {
		respond(w, http.StatusBadRequest, "mutual_code is required")
		return
	}

	// The scanner is identified only by their Bearer token.
	token := bearerToken(r)
	if token == "" {
		rejectMissingToken(w)
		return
	}
	scanner, ok, err := s.db.UserByTokenHash(r.Context(), db.HashToken(token))
	if err != nil {
		respond(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		unauthorized(w, "invalid token")
		return
	}

	res, err := s.db.ConsumeMutualCode(r.Context(), scanner.Address, req.UserAddress, req.MutualCode)
	if err != nil {
		s.mapError(w, err)
		return
	}
	code := http.StatusCreated
	if !res.ABNew && !res.BANew {
		// Pair already existed: the code is consumed, nothing new created.
		code = http.StatusOK
	}
	writeJSON(w, code, MutualHandshakeResponse{
		Handshakes: []model.Handshake{res.AB, res.BA},
		Chat:       res.Chat,
	})
}

// ---- POST /api/room/{room}/user/{address}/mutual-code ----

// SetMutualCodeRequest is the body for the mutual-code registration: the
// hash of the NEW one-time code (client-generated; the server never sees
// the raw code). Storing the hash makes the code live.
type SetMutualCodeRequest struct {
	MutualCodeHash string `json:"mutual_code_hash"`
}

// MutualCodeResponse reports the code state after a registration.
type MutualCodeResponse struct {
	Address        string `json:"address"`
	MutualCodeLive bool   `json:"mutual_code_live"`
}

// SetMutualCode registers a fresh one-time mutual code for the persona.
//
//	@Summary		register a one-time mutual code
//	@Description	Stores the SHA-256 hash of a fresh one-time mutual code for the persona; storing the hash makes the code live. Requires that persona's Bearer token.
//	@Tags			credentials
//	@Security		UserToken
//	@Accept			json
//	@Produce		json
//	@Param			room		path	string					true	"Room address"
//	@Param			address		path	string					true	"User address"
//	@Param			request		body	SetMutualCodeRequest	true	"Hash of the new one-time code"
//	@Success		200	{object}	MutualCodeResponse
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/room/{room}/user/{address}/mutual-code [post]
func (s *Server) SetMutualCode(w http.ResponseWriter, r *http.Request) {
	if !s.authUserToken(w, r, r.PathValue("room"), r.PathValue("address")) {
		return
	}
	var req SetMutualCodeRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if !mutualCodeHashRe.MatchString(req.MutualCodeHash) {
		respond(w, http.StatusBadRequest,
			"mutual_code_hash is required: the SHA-256 hex of the client-generated one-time code")
		return
	}
	if err := s.db.SetUserMutualCodeHash(r.Context(),
		r.PathValue("room"), r.PathValue("address"), req.MutualCodeHash); err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, MutualCodeResponse{
		Address:        r.PathValue("address"),
		MutualCodeLive: true,
	})
}

// ---- POST /api/room/{room}/user/{address}/token ----

// RotateTokenRequest is the body for token rotation: the NEW raw token
// (client-generated; hashed once server-side, the old token dies).
type RotateTokenRequest struct {
	Token string `json:"token"`
}

// RotateUserToken rotates a persona's token.
//
//	@Summary		rotate persona token
//	@Description	Stores the hash of the NEW client-generated persona token; the old token is invalid afterwards. Requires the CURRENT persona token as Bearer.
//	@Tags			credentials
//	@Security		UserToken
//	@Accept			json
//	@Produce		json
//	@Param			room		path	string					true	"Room address"
//	@Param			address		path	string					true	"User address"
//	@Param			request		body	RotateTokenRequest		true	"New raw token"
//	@Success		200	{object}	MutualCodeResponse
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/room/{room}/user/{address}/token [post]
func (s *Server) RotateUserToken(w http.ResponseWriter, r *http.Request) {
	if !s.authUserToken(w, r, r.PathValue("room"), r.PathValue("address")) {
		return
	}
	var req RotateTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if !validateToken(req.Token) {
		respond(w, http.StatusBadRequest,
			"token is required: a client-generated secret of at least 32 symbols")
		return
	}
	if err := s.db.SetUserTokenHash(r.Context(),
		r.PathValue("room"), r.PathValue("address"), db.HashToken(req.Token)); err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, MutualCodeResponse{Address: r.PathValue("address")})
}

// RotateRoomToken rotates a room's token.
//
//	@Summary		rotate room token
//	@Description	Stores the hash of the NEW client-generated room token; the old token is invalid afterwards. Requires the CURRENT room token as Bearer.
//	@Tags			credentials
//	@Security		RoomToken
//	@Accept			json
//	@Produce		json
//	@Param			address		path	string					true	"Room address"
//	@Param			request		body	RotateTokenRequest		true	"New raw token"
//	@Success		200	{object}	MutualCodeResponse
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/room/{address}/token [post]
func (s *Server) RotateRoomToken(w http.ResponseWriter, r *http.Request) {
	if !s.authRoomToken(w, r, r.PathValue("address")) {
		return
	}
	var req RotateTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if !validateToken(req.Token) {
		respond(w, http.StatusBadRequest,
			"token is required: a client-generated secret of at least 32 symbols")
		return
	}
	if err := s.db.SetRoomTokenHash(r.Context(), r.PathValue("address"), db.HashToken(req.Token)); err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, MutualCodeResponse{Address: r.PathValue("address")})
}

// ---- invite management (room token) ----

// CreateInviteRequest is the body for POST /api/room/{address}/invite. The
// expiry is optional; the default is 24 hours. Codes are multi-use until
// expiry, revocable by the room owner.
type CreateInviteRequest struct {
	ExpiresAt string `json:"expires_at"`
}

// InviteResponse reports a minted invite code.
type InviteResponse struct {
	Room      string    `json:"room"`
	Code      string    `json:"code"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// InviteListResponse reports the active invite codes of a room.
type InviteListResponse struct {
	Invites []InviteResponse `json:"invites"`
}

// CreateRoomInvite mints a join code for the room.
//
//	@Summary		mint a room invite code
//	@Description	Mints a fresh 6-digit join code for the room. Multi-use until expiry (default 24 h, settable via expires_at); several codes may be active per room; revocation is immediate. Requires the room token as Bearer.
//	@Tags			credentials
//	@Security		RoomToken
//	@Accept			json
//	@Produce		json
//	@Param			address		path	string					true	"Room address"
//	@Param			request		body	CreateInviteRequest	false	"Optional expiry"
//	@Success		201	{object}	InviteResponse
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/room/{address}/invite [post]
func (s *Server) CreateRoomInvite(w http.ResponseWriter, r *http.Request) {
	if !s.authRoomToken(w, r, r.PathValue("address")) {
		return
	}
	var req CreateInviteRequest
	// The body is optional: an empty body keeps the default expiry.
	if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	expires := time.Now().UTC().Add(db.DefaultInviteExpiry)
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			respond(w, http.StatusBadRequest, "expires_at must be an RFC3339 timestamp")
			return
		}
		if !t.After(time.Now().UTC()) {
			respond(w, http.StatusBadRequest, "expires_at must be in the future")
			return
		}
		expires = t.UTC()
	}
	inv, err := s.db.CreateRoomInvite(r.Context(), r.PathValue("address"), expires)
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, InviteResponse{
		Room:      inv.Room,
		Code:      inv.Code,
		CreatedAt: inv.CreatedAt,
		ExpiresAt: inv.ExpiresAt,
	})
}

// ListRoomInvites lists the active invite codes of a room.
//
//	@Summary		list active invite codes
//	@Description	Returns the not-yet-revoked, unexpired invite codes of the room. Requires the room token as Bearer.
//	@Tags			credentials
//	@Security		RoomToken
//	@Produce		json
//	@Param			address	path	string	true	"Room address"
//	@Success		200	{object}	InviteListResponse
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/room/{address}/invite [get]
func (s *Server) ListRoomInvites(w http.ResponseWriter, r *http.Request) {
	if !s.authRoomToken(w, r, r.PathValue("address")) {
		return
	}
	invites, err := s.db.ListRoomInvites(r.Context(), r.PathValue("address"))
	if err != nil {
		s.mapError(w, err)
		return
	}
	out := make([]InviteResponse, 0, len(invites))
	for _, inv := range invites {
		out = append(out, InviteResponse{
			Room:      inv.Room,
			Code:      inv.Code,
			CreatedAt: inv.CreatedAt,
			ExpiresAt: inv.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, InviteListResponse{Invites: out})
}

// RevokeRoomInvite revokes an invite code immediately.
//
//	@Summary		revoke an invite code
//	@Description	Revokes the invite code of the room immediately. Requires the room token as Bearer.
//	@Tags			credentials
//	@Security		RoomToken
//	@Produce		json
//	@Param			address	path	string	true	"Room address"
//	@Param			code	path	string	true	"Invite code"
//	@Success		200	{object}	MutualCodeResponse
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/room/{address}/invite/{code} [delete]
func (s *Server) RevokeRoomInvite(w http.ResponseWriter, r *http.Request) {
	if !s.authRoomToken(w, r, r.PathValue("address")) {
		return
	}
	if err := s.db.RevokeRoomInvite(r.Context(),
		r.PathValue("address"), r.PathValue("code")); err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, MutualCodeResponse{Address: r.PathValue("address")})
}

// ---- GET /api/invite/{code} (public resolution, 6.9b) ----

// ResolveInviteResponse maps a join code to its room.
type ResolveInviteResponse struct {
	Room string `json:"room" example:"0xroom123"`
}

// ResolveInvite resolves an invite code to its room address. Public but
// rate-limited (6-digit space).
//
//	@Summary		resolve an invite code
//	@Description	Resolves a currently-valid 6-digit invite code to its room address. Public read, rate-limited per source.
//	@Tags			credentials
//	@Produce		json
//	@Param			code	path	string	true	"Invite code"
//	@Success		200	{object}	ResolveInviteResponse
//	@Failure		404	{object}	string
//	@Failure		429	{object}	string
//	@Router			/api/invite/{code} [get]
func (s *Server) ResolveInvite(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, "resolve") {
		return
	}
	room, ok, err := s.db.ResolveRoomInvite(r.Context(), r.PathValue("code"))
	if err != nil {
		s.mapError(w, err)
		return
	}
	if !ok {
		s.mapError(w, db.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, ResolveInviteResponse{Room: room})
}
