// Shared contacts endpoints (feature #35): one persona shares its contact
// details with another, once per pair; only the recipient reads them.
package server

import (
	"net/http"
	"unicode/utf8"

	"github.com/extube/bleak-node/internal/model"
)

// ShareContactRequest is the body for POST /api/contact.
type ShareContactRequest struct {
	From string `json:"from" example:"0xuser123"`
	To   string `json:"to" example:"0xuser456"`
	// Content is the contact detail itself (a messenger handle, an email,
	// a phone line, ...).
	Content string `json:"content" example:"telegram: @alice_paints"`
}

// ShareContact shares the caller's contacts with another user. Requires
// the FROM persona's Bearer token. The (from, to) pair is unique: a
// repeated share of the same pair answers 409.
//
//	@Summary		share contacts
//	@Description	Shares contact details from one persona to another inside the network. Requires the FROM persona's Bearer token; both users must exist and differ. The (from, to) pair is unique: a persona can share its contacts with a given user only once - a repeated share answers 409. Content is required and limited to 255 symbols.
//	@Tags			contacts
//	@Security		UserToken
//	@Accept			json
//	@Produce		json
//	@Param			request	body	ShareContactRequest	true	"Contact to share"
//	@Success		201	{object}	model.Contact
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Failure		409	{object}	string
//	@Router			/api/contact [post]
func (s *Server) ShareContact(w http.ResponseWriter, r *http.Request) {
	var req ShareContactRequest
	if err := decodeJSON(r, &req); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if err := validateAddressArgs(req.From, req.To); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	if req.From == req.To {
		respond(w, http.StatusBadRequest, "from and to must differ")
		return
	}
	if n := utf8.RuneCountInString(req.Content); n == 0 || n > model.MaxContactContentLen {
		respond(w, http.StatusBadRequest,
			"content must be 1.."+itoa(model.MaxContactContentLen)+" symbols")
		return
	}
	// Only the sender shares: its persona token gates the write.
	if !s.authUserTokenByAddress(w, r, req.From) {
		return
	}

	contact, err := s.db.ShareContact(r.Context(), req.From, req.To, req.Content)
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, contact)
}

// ListContacts returns the contacts shared with the given user. Only the
// recipient has access: requires that persona's Bearer token.
//
//	@Summary		list contacts of a user
//	@Description	Returns every contact shared with the given persona, newest first. ONLY the recipient has access to the records: requires that persona's Bearer token.
//	@Tags			contacts
//	@Security		UserToken
//	@Produce		json
//	@Param			address	path	string	true	"User address (the recipient)"
//	@Success		200	{array}	model.Contact
//	@Failure		400	{object}	string
//	@Failure		401	{object}	string
//	@Failure		403	{object}	string
//	@Failure		404	{object}	string
//	@Router			/api/contact/{address} [get]
func (s *Server) ListContacts(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	if err := validateAddressArgs(address); err != nil {
		respond(w, http.StatusBadRequest, message(err))
		return
	}
	// Only the recipient reads its incoming contacts.
	if !s.authUserTokenByAddress(w, r, address) {
		return
	}

	contacts, err := s.db.ListContactsTo(r.Context(), address)
	if err != nil {
		s.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contacts)
}
