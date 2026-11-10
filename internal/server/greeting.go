package server

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/extube/bleak-node/internal/agent"
)

// GreetingResponse is the greeting (hello) message of a user.
type GreetingResponse struct {
	Address  string `json:"address"`
	Greeting string `json:"greeting"`
}

// PutUserGreetingRequest is the body for PUT .../greeting. The greeting
// opens every new chat of the user, so questions are not allowed: the
// dialog starts with both users' greetings, and a question there would
// turn the hello exchange into an interview.
type PutUserGreetingRequest struct {
	Greeting string `json:"greeting"`
}

const maxGreetingLen = 255

// GetUserGreeting returns the greeting (hello) message of a user.
//
//	@Summary		get user greeting
//	@Description	Returns the greeting (hello) message of a non-deleted user.
//	@Tags			users
//	@Produce		json
//	@Param			room	path	string	true	"Room address"
//	@Param			address	path	string	true	"User address"
//	@Success		200	{object}	GreetingResponse
//	@Failure		404	{object}	string
//	@Router			/api/room/{room}/user/{address}/greeting [get]
func (s *Server) GetUserGreeting(w http.ResponseWriter, r *http.Request) {
	user, err := s.db.GetUser(r.Context(), r.PathValue("room"), r.PathValue("address"))
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, GreetingResponse{Address: user.Address, Greeting: user.Greeting})
}

// PutUserGreeting sets the greeting (hello) message of a user manually.
//
//	@Summary		set user greeting
//	@Description	Stores the greeting (hello) message of a non-deleted user. The greeting must be non-empty and must not contain questions: it opens every new chat of the user.
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Param			room	path	string					true	"Room address"
//	@Param			address	path	string					true	"User address"
//	@Param			request	body	PutUserGreetingRequest	true	"Greeting to set"
//	@Success		200	{object}	GreetingResponse
//	@Failure		400	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/room/{room}/user/{address}/greeting [put]
func (s *Server) PutUserGreeting(w http.ResponseWriter, r *http.Request) {
	var req PutUserGreetingRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	greeting := strings.TrimSpace(req.Greeting)
	if err := validateGreeting(greeting); err != "" {
		respond(w, http.StatusBadRequest, err)
		return
	}
	user, err := s.db.SetUserGreeting(r.Context(), r.PathValue("room"), r.PathValue("address"), greeting)
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, GreetingResponse{Address: user.Address, Greeting: user.Greeting})
}

// RegenerateUserGreeting re-generates the greeting (hello) message of a
// user from their bios with the LLM.
//
//	@Summary		regenerate user greeting
//	@Description	Re-generates the greeting (hello) message of a non-deleted user from their bios. The optional language query parameter selects "en" (default) or "ru".
//	@Tags			users
//	@Produce		json
//	@Param			room		path	string	true	"Room address"
//	@Param			address		path	string	true	"User address"
//	@Param			language	query	string	false	"Greeting language: en (default) or ru"
//	@Success		200	{object}	GreetingResponse
//	@Failure		400	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/room/{room}/user/{address}/greeting/regenerate [post]
func (s *Server) RegenerateUserGreeting(w http.ResponseWriter, r *http.Request) {
	user, err := s.db.GetUser(r.Context(), r.PathValue("room"), r.PathValue("address"))
	if err != nil {
		s.mapError(w, err)
		return
	}
	language := r.URL.Query().Get("language")
	if language == "" {
		language = "en"
	}
	if language != "en" && language != "ru" {
		respond(w, http.StatusBadRequest, "language must be one of {en, ru}")
		return
	}
	greeting := s.greetingFor(user.Name, user.PublicBio, user.PrivateBio, language)
	if _, err := s.db.SetUserGreeting(r.Context(), user.Room, user.Address, greeting); err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, GreetingResponse{Address: user.Address, Greeting: greeting})
}

// greetingFor produces the greeting text for a user: the LLM greeter when
// available, otherwise the plain bio-based template. Always non-empty and
// question-free.
func (s *Server) greetingFor(name, publicBio, privateBio, language string) string {
	if s.greeter != nil {
		return s.greeter.GenerateGreeting(name, publicBio, privateBio, language)
	}
	return agent.FallbackGreeting(name, publicBio, language == "ru")
}

// validateGreeting reports an error message when the manually set greeting
// does not fit the dialog rules: non-empty, fits the message limit, no
// questions (the greeting opens the chat, both users greet each other
// before any question is asked).
func validateGreeting(greeting string) string {
	if greeting == "" {
		return "greeting is required"
	}
	if n := utf8.RuneCountInString(greeting); n > maxGreetingLen {
		return "greeting must be at most 255 symbols"
	}
	if strings.ContainsRune(greeting, '?') {
		return "greeting must not contain questions"
	}
	return ""
}
