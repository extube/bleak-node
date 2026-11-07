package server

import (
	"net/http"
	"unicode/utf8"

	"github.com/extube/bleak-node/internal/db"
	"github.com/extube/bleak-node/internal/model"
)

// CreateHandshakeRequest is the body for POST /api/handshake.
type CreateHandshakeRequest struct {
	TxID        string `json:"tx_id"`
	FromAddress string `json:"from_address"`
	ToAddress   string `json:"to_address"`
	Status      int    `json:"status"`
}

// UpdateHandshakeRequest is the body for PUT /api/handshake/{tx_id}.
type UpdateHandshakeRequest struct {
	Status int `json:"status"`
}

const maxAddressLen = 64

// CreateHandshake registers a handshake between two users.
//
//	@Summary		create handshake
//	@Description	Registers a handshake between two existing users. Status must be in {0,1,2,3,4}.
//	@Tags			handshakes
//	@Accept			json
//	@Produce		json
//	@Param			request	body	CreateHandshakeRequest	true	"Handshake to create"
//	@Success		201	{object}	model.Handshake
//	@Failure		400	{object}	string
//	@Failure		404	{object}	string
//	@Failure		409	{object}	string
//	@Router			/api/handshake [post]
func (s *Server) CreateHandshake(w http.ResponseWriter, r *http.Request) {
	var req CreateHandshakeRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if req.TxID == "" {
		respond(w, http.StatusBadRequest, "tx_id is required")
		return
	}
	for _, f := range []struct{ name, addr string }{
		{"from_address", req.FromAddress},
		{"to_address", req.ToAddress},
	} {
		if f.addr == "" {
			respond(w, http.StatusBadRequest, f.name+" is required")
			return
		}
		if utf8.RuneCountInString(f.addr) > maxAddressLen {
			respond(w, http.StatusBadRequest, f.name+" is too long")
			return
		}
	}
	if !db.IsValidStatus(req.Status) {
		respond(w, http.StatusBadRequest, "status must be one of {0,1,2,3,4}")
		return
	}

	hs := model.Handshake{
		TxID:        req.TxID,
		FromAddress: req.FromAddress,
		ToAddress:   req.ToAddress,
		Status:      req.Status,
	}
	if err := s.db.CreateHandshake(r.Context(), &hs); err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, hs)
}

// UpdateHandshake updates the status of a handshake.
//
//	@Summary		update handshake
//	@Description	Updates a handshake's status. Must be one of {0,1,2,3,4}.
//	@Tags			handshakes
//	@Accept			json
//	@Produce		json
//	@Param			tx_id	path	string					true	"Handshake tx id"
//	@Param			request	body	UpdateHandshakeRequest	true	"New status"
//	@Success		200	{object}	model.Handshake
//	@Failure		400	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/handshake/{tx_id} [put]
func (s *Server) UpdateHandshake(w http.ResponseWriter, r *http.Request) {
	var req UpdateHandshakeRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if !db.IsValidStatus(req.Status) {
		respond(w, http.StatusBadRequest, "status must be one of {0,1,2,3,4}")
		return
	}

	hs, err := s.db.UpdateHandshakeStatus(r.Context(), r.PathValue("tx_id"), req.Status)
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hs)
}

// ListUserHandshakes returns all handshakes of a user.
//
//	@Summary		list handshakes of a user
//	@Description	Returns every handshake where the user is the sender or the receiver, newest first.
//	@Tags			handshakes
//	@Produce		json
//	@Param			address	path	string	true	"User address"
//	@Success		200	{array}	model.Handshake
//	@Router			/api/handshake/user/{address} [get]
func (s *Server) ListUserHandshakes(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.ListUserHandshakes(r.Context(), r.PathValue("address"))
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
