package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/extube/bleak-node/internal/db"
)

// fakeGreeter counts the greeting generations and records the language.
type fakeGreeter struct {
	calls    int
	language string
}

func (f *fakeGreeter) GenerateGreeting(name, publicBio, privateBio, language string) string {
	f.calls++
	f.language = language
	return "Hello, I'm " + name + " — greetings number " + string(rune('0'+f.calls)) + "."
}

func greetingTestServer(t *testing.T) (*httptest.Server, *db.DB, *fakeGreeter) {
	t.Helper()
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "greeting.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	g := &fakeGreeter{}
	ts := httptest.NewServer(New(database, g).Handler())
	t.Cleanup(ts.Close)
	return ts, database, g
}

func TestGreetingGeneratedOnCreation(t *testing.T) {
	ts, database, greeter := greetingTestServer(t)
	defer ts.Close()

	// Room (creation-token gated); the first invite code comes back.
	room, roomToken, invite := createTestRoom(t, ts, database, "Greeting Room")

	// User (invite-code gated), carrying the client-generated persona token.
	userAddr, userToken := joinTestRoom(t, ts, room, invite, "Sofia")

	// GET greeting returns the stored text.
	resp, err := http.Get(ts.URL + "/api/room/" + room + "/user/" + userAddr + "/greeting")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("get greeting: %v %s", err, resp.Status)
	}
	var gr GreetingResponse
	json.NewDecoder(resp.Body).Decode(&gr)
	resp.Body.Close()
	if gr.Greeting == "" || !strings.Contains(gr.Greeting, "Sofia") {
		t.Fatalf("greeting = %q, want generated hello with the name", gr.Greeting)
	}
	if greeter.calls != 1 || greeter.language != "en" {
		t.Fatalf("greeter calls = %d language = %q", greeter.calls, greeter.language)
	}

	// PUT replaces it manually — gated: the persona token is required.
	req, _ := http.NewRequest(http.MethodPut,
		ts.URL+"/api/room/"+room+"/user/"+userAddr+"/greeting",
		strings.NewReader(`{"greeting":"Hello there, welcome to my corner."}`))
	req.Header.Set("Authorization", "Bearer "+userToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("put greeting: %v %s", err, resp.Status)
	}
	resp.Body.Close()

	// A question in the greeting is declined: the hello exchange must not
	// open with a question.
	req, _ = http.NewRequest(http.MethodPut,
		ts.URL+"/api/room/"+room+"/user/"+userAddr+"/greeting",
		strings.NewReader(`{"greeting":"Hi! How are you?"}`))
	req.Header.Set("Authorization", "Bearer "+userToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("put question greeting: %v %s", err, resp.Status)
	}
	resp.Body.Close()

	// Regenerate goes through the greeter again, still in the room language.
	req, _ = http.NewRequest(http.MethodPost,
		ts.URL+"/api/room/"+room+"/user/"+userAddr+"/greeting/regenerate", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("regenerate: %v %s", err, resp.Status)
	}
	resp.Body.Close()
	if greeter.calls != 2 || greeter.language != "en" {
		t.Fatalf("after regenerate: calls = %d language = %q", greeter.calls, greeter.language)
	}

	// The room token does not authorize persona mutations (wrong record →
	// valid token, wrong record → 403).
	req, _ = http.NewRequest(http.MethodPost,
		ts.URL+"/api/room/"+room+"/user/"+userAddr+"/greeting/regenerate", nil)
	req.Header.Set("Authorization", "Bearer "+roomToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("regenerate with room token: %v %s", err, resp.Status)
	}
	resp.Body.Close()
}

