package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/extube/bleak-node/internal/db"
)

// newTestSecret generates a raw client-style token: 32 random bytes,
// base64url (CONCEPT.md §7.4 shape).
func newTestSecret(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// credTestServer starts a server with its database exposed for direct
// credential seeding (creation-token minting etc.).
func credTestServer(t *testing.T) (*httptest.Server, *db.DB) {
	t.Helper()
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cred.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	ts := httptest.NewServer(New(database, nil).Handler())
	t.Cleanup(ts.Close)
	return ts, database
}

// seedCreateToken mints a one-time creation token directly (the operator
// CLI equivalent).
func seedCreateToken(t *testing.T, database *db.DB) string {
	t.Helper()
	raw := newTestSecret(t)
	if err := database.MintCreateToken(context.Background(), db.HashToken(raw)); err != nil {
		t.Fatalf("mint creation token: %v", err)
	}
	return raw
}

// createTestRoom creates a room through the API; returns its address,
// room token and first invite code.
func createTestRoom(t *testing.T, ts *httptest.Server, database *db.DB, name string) (address, token, invite string) {
	t.Helper()
	createRaw := seedCreateToken(t, database)
	token = newTestSecret(t)

	body := fmt.Sprintf(`{"name":%q,"token":%q}`, name, token)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/room", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new room request: %v", err)
	}
	req.Header.Set(createTokenHeader, createRaw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create room: %v %s", err, http.StatusText(statusOf(resp)))
	}
	defer resp.Body.Close()
	var out struct {
		Address    string `json:"address"`
		InviteCode string `json:"invite_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode room: %v", err)
	}
	return out.Address, token, out.InviteCode
}

// joinTestRoom creates a user through the API (invite-code gated); returns
// the user address and persona token.
func joinTestRoom(t *testing.T, ts *httptest.Server, room, invite, name string) (address, token string) {
	t.Helper()
	token = newTestSecret(t)
	body := fmt.Sprintf(`{"name":%q,"token":%q,"invite_code":%q}`, name, token, invite)
	resp, err := http.Post(ts.URL+"/api/room/"+room+"/user", "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create user: %v %s", err, http.StatusText(statusOf(resp)))
	}
	defer resp.Body.Close()
	var out struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode user: %v", err)
	}
	return out.Address, token
}

// statusOf degrades gracefully when the caller already closed the body.
func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}
