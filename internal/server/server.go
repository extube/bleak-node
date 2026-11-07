// Package server wires the REST API handlers to the router.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/extube/bleak-node/internal/db"
	"github.com/swaggo/http-swagger"
)

// Server holds shared dependencies for all handlers.
type Server struct {
	db *db.DB
}

// New creates a server with its routing table already registered.
func New(database *db.DB) *Server {
	return &Server{db: database}
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

	// Handshakes.
	mux.HandleFunc("POST /api/handshake", s.CreateHandshake)
	mux.HandleFunc("GET /api/handshake/user/{address}", s.ListUserHandshakes)

	// Chats & messages.
	mux.HandleFunc("POST /api/message", s.SendMessage)
	mux.HandleFunc("GET /api/user/{address}/chat", s.ListUserChats)
	mux.HandleFunc("GET /api/room/{room}/user/{address}/chat", s.ListRoomUserChats)
	mux.HandleFunc("GET /api/room/{room}/user/{address}/chat/{chat}/messages", s.ListRoomUserChatMessages)
	mux.HandleFunc("GET /api/chat/{address}", s.GetChat)
	mux.HandleFunc("DELETE /api/chat/{address}", s.DeleteChat)
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
	default:
		respond(w, http.StatusInternalServerError, err.Error())
	}
}

// message renders an error (possibly an errors.Join chain) on a single line.
func message(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", ": ")
}
