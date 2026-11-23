package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/extube/bleak-node/internal/db"
)

// matchV2 posts /api/match/v2 and returns (status, decoded body).
func matchV2(t *testing.T, tsURL, room, first, second, token string) (int, map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"room_address":%q,"first_user_address":%q,"second_user_address":%q}`,
		room, first, second)
	code, resp := doJSON(t, bearerReq(t, http.MethodPost, tsURL+"/api/match/v2", token, body))
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode match v2: %v", err)
	}
	return code, out
}

// hardMatch posts /api/hard-match and returns (status, decoded body).
func hardMatch(t *testing.T, tsURL, room, first, second, token string) (int, map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"room_address":%q,"first_user_address":%q,"second_user_address":%q}`,
		room, first, second)
	code, resp := doJSON(t, bearerReq(t, http.MethodPost, tsURL+"/api/hard-match", token, body))
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode hard match: %v", err)
	}
	return code, out
}

// shareContact posts /api/contact and returns the status.
func shareContact(t *testing.T, tsURL, from, to, token, content string) int {
	t.Helper()
	body := fmt.Sprintf(`{"from":%q,"to":%q,"content":%q}`, from, to, content)
	code, resp := doJSON(t, bearerReq(t, http.MethodPost, tsURL+"/api/contact", token, body))
	resp.Body.Close()
	return code
}

// listContacts GETs /api/contact/{address} and returns (status, decoded body);
// non-200 bodies arrive as error objects.
func listContacts(t *testing.T, tsURL, address, token string) (int, any) {
	t.Helper()
	code, resp := doJSON(t, bearerReq(t, http.MethodGet, tsURL+"/api/contact/"+address, token, ""))
	defer func() { _ = resp.Body.Close() }()
	var out any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode contacts: %v", err)
	}
	return code, out
}

// seedMutualPair creates a room with two users that hold handshakes in both
// directions, so a chat exists between them.
func seedMutualPair(t *testing.T, ts *httptest.Server, database *db.DB) (room, alice, aliceToken, bob, bobToken string) {
	t.Helper()
	room, _, invite := createTestRoom(t, ts, database, "Contact Room")
	alice, aliceToken = joinTestRoom(t, ts, room, invite, "Alice")
	bob, bobToken = joinTestRoom(t, ts, room, invite, "Bob")
	sendTestHandshake(t, ts.URL, alice, aliceToken, bob)
	sendTestHandshake(t, ts.URL, bob, bobToken, alice)
	return room, alice, aliceToken, bob, bobToken
}

func TestMatchV2Endpoint(t *testing.T) {
	ts, database := credTestServer(t)
	room, alice, aliceToken, bob, _ := seedMutualPair(t, ts, database)

	// Missing token: 401; wrong persona: 403.
	code, _ := matchV2(t, ts.URL, room, alice, bob, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", code)
	}
	code, _ = matchV2(t, ts.URL, room, alice, bob, "0xinvalid")
	if code != http.StatusUnauthorized {
		t.Fatalf("bad token: want 401, got %d", code)
	}

	// Mutual pair with no interests: the pure interest percent is 0; the
	// handshake flags stay informational.
	code, res := matchV2(t, ts.URL, room, alice, bob, aliceToken)
	if code != http.StatusOK {
		t.Fatalf("match v2: %s", http.StatusText(code))
	}
	if res["points"].(float64) != 0 {
		t.Fatalf("no interests must grade 0, got %v", res["points"])
	}
	if res["mutual_handshake"] != true {
		t.Fatalf("mutual handshake flag: %v", res["mutual_handshake"])
	}

	// Unknown users: 404.
	code, _ = matchV2(t, ts.URL, room, alice, "0xunknown", aliceToken)
	if code != http.StatusNotFound {
		t.Fatalf("unknown user: want 404, got %d", code)
	}
}

func TestHardMatchEndpoint(t *testing.T) {
	ts, database := credTestServer(t)
	room, alice, aliceToken, bob, _ := seedMutualPair(t, ts, database)

	// Token rules mirror the legacy match: 401 without, 403 wrong record.
	code, _ := hardMatch(t, ts.URL, room, alice, bob, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", code)
	}
	code, _ = hardMatch(t, ts.URL, room, alice, bob, "0xinvalid")
	if code != http.StatusUnauthorized {
		t.Fatalf("bad token: want 401, got %d", code)
	}

	// First call creates the summary: 201, strictly inside (0,100).
	code, first := hardMatch(t, ts.URL, room, alice, bob, aliceToken)
	if code != http.StatusCreated {
		t.Fatalf("hard match: %s", http.StatusText(code))
	}
	score, ok := first["score"].(float64)
	if !ok || score <= 0 || score >= 100 {
		t.Fatalf("score must sit strictly inside (0,100), got %v", first["score"])
	}
	if first["chat"] == nil || first["description"] == nil {
		t.Fatalf("summary incomplete: %v", first)
	}

	// A repeated call just returns the stored summary: 200, same record.
	code, again := hardMatch(t, ts.URL, room, alice, bob, aliceToken)
	if code != http.StatusOK {
		t.Fatalf("hard match repeat: %s", http.StatusText(code))
	}
	if again["score"] != first["score"] || again["description"] != first["description"] {
		t.Fatalf("stored summary changed: %v vs %v", again, first)
	}

	// A pair without a chat: 404.
	code, _ = hardMatch(t, ts.URL, room, alice, "0xstranger", aliceToken)
	if code != http.StatusNotFound {
		t.Fatalf("missing chat: want 404, got %d", code)
	}
}

func TestContactEndpoints(t *testing.T) {
	ts, database := credTestServer(t)
	_, alice, aliceToken, bob, bobToken := seedMutualPair(t, ts, database)

	// Missing token: 401; wrong persona: 403.
	if code := shareContact(t, ts.URL, alice, bob, "", "telegram: @alice"); code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", code)
	}
	if code := shareContact(t, ts.URL, alice, bob, bobToken, "telegram: @alice"); code != http.StatusForbidden {
		t.Fatalf("wrong persona: want 403, got %d", code)
	}

	// The share: 201.
	if code := shareContact(t, ts.URL, alice, bob, aliceToken, "telegram: @alice"); code != http.StatusCreated {
		t.Fatalf("share: %s", http.StatusText(code))
	}

	// The pair is unique: a repeated share answers 409.
	if code := shareContact(t, ts.URL, alice, bob, aliceToken, "another detail"); code != http.StatusConflict {
		t.Fatalf("repeated share: want 409, got %d", code)
	}

	// Validation: 400 for self-share, empty content.
	if code := shareContact(t, ts.URL, alice, alice, aliceToken, "x"); code != http.StatusBadRequest {
		t.Fatalf("self share: want 400, got %d", code)
	}
	if code := shareContact(t, ts.URL, alice, bob, aliceToken, ""); code != http.StatusBadRequest {
		t.Fatalf("empty content: want 400, got %d", code)
	}

	// Reads are recipient-only: bob reads its incoming contacts with the
	// right token; alice's token for bob's inbox is 403.
	code, list := listContacts(t, ts.URL, bob, bobToken)
	if code != http.StatusOK {
		t.Fatalf("list: %s", http.StatusText(code))
	}
	rows, ok := list.([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("contacts wrong: %v", list)
	}
	row, _ := rows[0].(map[string]any)
	if row["from"] != alice || row["to"] != bob {
		t.Fatalf("contact row wrong: %v", row)
	}
	code, _ = listContacts(t, ts.URL, bob, aliceToken)
	if code != http.StatusForbidden {
		t.Fatalf("foreign inbox: want 403, got %d", code)
	}
	code, _ = listContacts(t, ts.URL, bob, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("inbox no token: want 401, got %d", code)
	}
}
