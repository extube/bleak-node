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

func greetingTestServer(t *testing.T) (*httptest.Server, *fakeGreeter) {
	t.Helper()
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "greeting.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	g := &fakeGreeter{}
	ts := httptest.NewServer(New(database, g).Handler())
	t.Cleanup(ts.Close)
	return ts, g
}

func TestGreetingGeneratedOnCreation(t *testing.T) {
	ts, greeter := greetingTestServer(t)
	defer ts.Close()

	// Room.
	resp, err := http.Post(ts.URL+"/api/room", "application/json",
		strings.NewReader(`{"name":"Greeting Room"}`))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create room: %v %s", err, resp.Status)
	}
	var room struct{ Address string }
	json.NewDecoder(resp.Body).Decode(&room)
	resp.Body.Close()

	// User: the greeting is generated during creation.
	resp, err = http.Post(ts.URL+"/api/room/"+room.Address+"/user", "application/json",
		strings.NewReader(`{"name":"Sofia","public_bio":"calligrapher","language":"en"}`))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create user: %v %s", err, resp.Status)
	}
	var user struct {
		Address  string `json:"address"`
		Greeting string `json:"greeting"`
	}
	json.NewDecoder(resp.Body).Decode(&user)
	resp.Body.Close()
	if user.Greeting == "" || !strings.Contains(user.Greeting, "Sofia") {
		t.Fatalf("greeting = %q, want generated hello with the name", user.Greeting)
	}
	if greeter.calls != 1 || greeter.language != "en" {
		t.Fatalf("greeter calls = %d language = %q", greeter.calls, greeter.language)
	}

	// GET greeting returns the stored text.
	resp, err = http.Get(ts.URL + "/api/room/" + room.Address + "/user/" + user.Address + "/greeting")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("get greeting: %v %s", err, resp.Status)
	}
	var gr GreetingResponse
	json.NewDecoder(resp.Body).Decode(&gr)
	resp.Body.Close()
	if gr.Greeting != user.Greeting {
		t.Fatalf("stored greeting = %q, want %q", gr.Greeting, user.Greeting)
	}

	// PUT replaces it manually.
	req, _ := http.NewRequest(http.MethodPut,
		ts.URL+"/api/room/"+room.Address+"/user/"+user.Address+"/greeting",
		strings.NewReader(`{"greeting":"Hello there, welcome to my corner."}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("put greeting: %v %s", err, resp.Status)
	}
	resp.Body.Close()

	// A question in the greeting is declined: the hello exchange must not
	// open with a question.
	req, _ = http.NewRequest(http.MethodPut,
		ts.URL+"/api/room/"+room.Address+"/user/"+user.Address+"/greeting",
		strings.NewReader(`{"greeting":"Hi! How are you?"}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("put question greeting: %v %s", err, resp.Status)
	}

	// Regenerate goes through the greeter again, honoring ?language=ru.
	req, _ = http.NewRequest(http.MethodPost,
		ts.URL+"/api/room/"+room.Address+"/user/"+user.Address+"/greeting/regenerate?language=ru", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("regenerate: %v %s", err, resp.Status)
	}
	resp.Body.Close()
	if greeter.calls != 2 || greeter.language != "ru" {
		t.Fatalf("after regenerate: calls = %d language = %q", greeter.calls, greeter.language)
	}
}
