package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// interestURL builds the interest endpoint URL of a user.
func interestURL(tsURL, room, address string) string {
	return tsURL + "/api/room/" + room + "/user/" + address + "/interest"
}

// addTestInterests posts the given interest rows for a user (Bearer token)
// and returns the decoded response body.
func addTestInterests(t *testing.T, tsURL, room, address, token, rows string) []map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"interests": [%s]}`, rows)
	code, resp := doJSON(t, bearerReq(t, http.MethodPost, interestURL(tsURL, room, address), token, body))
	if code != http.StatusCreated {
		t.Fatalf("add interests: %s", http.StatusText(code))
	}
	defer resp.Body.Close()
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode interests: %v", err)
	}
	return out
}

// matchPoints posts /api/match and returns (status, decoded body).
func matchPoints(t *testing.T, tsURL, room, first, second, token, time string) (int, map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"room_address":%q,"first_user_address":%q,"second_user_address":%q,"time":%s}`,
		room, first, second, time)
	code, resp := doJSON(t, bearerReq(t, http.MethodPost, tsURL+"/api/match", token, body))
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode match: %v", err)
	}
	return code, out
}

// sendTestHandshake posts a handshake between two users through the API.
func sendTestHandshake(t *testing.T, tsURL, from, token, to string) {
	t.Helper()
	body := fmt.Sprintf(`{"from_address":%q,"to_address":%q,"status":1}`, from, to)
	code, resp := doJSON(t, bearerReq(t, http.MethodPost, tsURL+"/api/handshake", token, body))
	resp.Body.Close()
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("handshake %s->%s: %s", from, to, http.StatusText(code))
	}
}

func TestInterestEndpointsAuth(t *testing.T) {
	ts, database := credTestServer(t)
	room, _, invite := createTestRoom(t, ts, database, "Interest Room")
	alice, aliceToken := joinTestRoom(t, ts, room, invite, "Alice")
	bob, bobToken := joinTestRoom(t, ts, room, invite, "Bob")

	// Missing token: 401.
	code, resp := doJSON(t, bearerReq(t, http.MethodPost, interestURL(ts.URL, room, alice), "",
		`{"interests":[{"title":"Chess","group":1,"key":7}]}`))
	resp.Body.Close()
	if code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", code)
	}

	// Another persona's token: 403 (valid token, wrong record).
	code, resp = doJSON(t, bearerReq(t, http.MethodDelete, interestURL(ts.URL, room, alice), bobToken, `{"keys":[1]}`))
	resp.Body.Close()
	if code != http.StatusForbidden {
		t.Fatalf("wrong token: want 403, got %d", code)
	}

	// Unknown user: the valid token of another record presented for an
	// unknown persona is a wrong-record case (house semantics: 403).
	code, resp = doJSON(t, bearerReq(t, http.MethodPost, interestURL(ts.URL, room, "0xunknown"), aliceToken,
		`{"interests":[{"title":"Chess","group":1,"key":7}]}`))
	resp.Body.Close()
	if code != http.StatusForbidden {
		t.Fatalf("unknown user: want 403, got %d", code)
	}

	// Empty list / oversized list / bad title / negative group: 400.
	for _, body := range []string{
		`{"interests":[]}`,
		`{"interests":[{"title":"","group":1,"key":1}]}`,
		`{"interests":[{"title":"x","group":-1,"key":1}]}`,
		`{"interests":[{"title":"x","group":1,"key":-1}]}`,
	} {
		code, resp = doJSON(t, bearerReq(t, http.MethodPost, interestURL(ts.URL, room, alice), aliceToken, body))
		resp.Body.Close()
		if code != http.StatusBadRequest {
			t.Fatalf("body %s: want 400, got %d", body, code)
		}
	}

	_ = bob
}

func TestInterestAddDeleteFlow(t *testing.T) {
	ts, database := credTestServer(t)
	room, _, invite := createTestRoom(t, ts, database, "Flow Room")
	alice, aliceToken := joinTestRoom(t, ts, room, invite, "Alice")
	bob, bobToken := joinTestRoom(t, ts, room, invite, "Bob")

	// Add two interests for Alice (Bearer = Alice).
	added := addTestInterests(t, ts.URL, room, alice, aliceToken,
		`{"title":"Chess","group":1,"key":7},{"title":"Guitar","group":2,"key":3}`)
	if len(added) != 2 {
		t.Fatalf("want 2 added, got %d", len(added))
	}
	// user_address must come from the route, not the body.
	if added[0]["user_address"] != alice {
		t.Fatalf("user_address: got %v, want %s", added[0]["user_address"], alice)
	}

	// Upsert the same key: the list stays at 2, title replaced.
	added = addTestInterests(t, ts.URL, room, alice, aliceToken, `{"title":"Blitz","group":1,"key":7}`)
	if len(added) != 2 {
		t.Fatalf("upsert duplicated: got %d", len(added))
	}
	if added[0]["title"] != "Blitz" {
		t.Fatalf("upsert did not replace: %v", added[0])
	}

	// Bob's list is untouched (2 rows for Alice only).
	added = addTestInterests(t, ts.URL, room, bob, bobToken, `{"title":"Go","group":1,"key":8}`)
	if len(added) != 1 {
		t.Fatalf("bob's list polluted: %d", len(added))
	}

	// Delete by keys: response carries the remaining interests.
	code, resp := doJSON(t, bearerReq(t, http.MethodDelete, interestURL(ts.URL, room, alice), aliceToken, `{"keys":[7]}`))
	if code != http.StatusOK {
		t.Fatalf("delete: %s", http.StatusText(code))
	}
	var remaining []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&remaining); err != nil {
		t.Fatalf("decode remaining: %v", err)
	}
	resp.Body.Close()
	if len(remaining) != 1 || remaining[0]["key"].(float64) != 3 {
		t.Fatalf("want 1 remaining (key 3), got %v", remaining)
	}

	// Unknown keys only: no error, nothing removed.
	code, resp = doJSON(t, bearerReq(t, http.MethodDelete, interestURL(ts.URL, room, alice), aliceToken, `{"keys":[99]}`))
	resp.Body.Close()
	if code != http.StatusOK {
		t.Fatalf("idempotent delete: %s", http.StatusText(code))
	}

	// Empty keys: 400.
	code, resp = doJSON(t, bearerReq(t, http.MethodDelete, interestURL(ts.URL, room, alice), aliceToken, `{"keys":[]}`))
	resp.Body.Close()
	if code != http.StatusBadRequest {
		t.Fatalf("empty keys: want 400, got %d", code)
	}
}

func TestMatchEndpoint(t *testing.T) {
	ts, database := credTestServer(t)
	room, _, invite := createTestRoom(t, ts, database, "Match Room")
	alice, aliceToken := joinTestRoom(t, ts, room, invite, "Alice")
	bob, bobToken := joinTestRoom(t, ts, room, invite, "Bob")

	// t = 1h, no handshake, no interests: 75.
	code, res := matchPoints(t, ts.URL, room, alice, bob, aliceToken, "3600000")
	if code != http.StatusOK {
		t.Fatalf("match: %s %v", http.StatusText(code), res)
	}
	if res["points"].(float64) != 75 {
		t.Fatalf("want 75, got %v (%v)", res["points"], res)
	}

	// Missing token: 401.
	code, _ = matchPoints(t, ts.URL, room, alice, bob, "", "3600000")
	if code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", code)
	}

	// Wrong persona's token (bob's, addressed as alice): 403.
	code, _ = matchPoints(t, ts.URL, room, alice, bob, bobToken, "3600000")
	if code != http.StatusForbidden {
		t.Fatalf("wrong token: want 403, got %d", code)
	}

	// Same addresses: 400; negative time: 400; unknown user: 404.
	code, _ = matchPoints(t, ts.URL, room, alice, alice, aliceToken, "0")
	if code != http.StatusBadRequest {
		t.Fatalf("same address: want 400, got %d", code)
	}
	code, _ = matchPoints(t, ts.URL, room, alice, bob, aliceToken, "-1")
	if code != http.StatusBadRequest {
		t.Fatalf("negative time: want 400, got %d", code)
	}
	code, _ = matchPoints(t, ts.URL, room, alice, "0xunknown", aliceToken, "0")
	if code != http.StatusNotFound {
		t.Fatalf("unknown user: want 404, got %d", code)
	}

	// Shared group +2, exact match +5.
	addTestInterests(t, ts.URL, room, alice, aliceToken, `{"title":"Chess","group":1,"key":7}`)
	addTestInterests(t, ts.URL, room, bob, bobToken, `{"title":"Go","group":1,"key":8}`)
	_, res = matchPoints(t, ts.URL, room, alice, bob, aliceToken, "3600000")
	if res["points"].(float64) != 77 || res["shared_groups"].(float64) != 1 {
		t.Fatalf("shared group: want 77, got %v", res)
	}
	addTestInterests(t, ts.URL, room, bob, bobToken, `{"title":"Chess","group":1,"key":7}`)
	_, res = matchPoints(t, ts.URL, room, alice, bob, aliceToken, "3600000")
	if res["points"].(float64) != 82 || res["exact_interest_matches"].(float64) != 1 {
		t.Fatalf("exact match: want 82, got %v", res)
	}

	// One-sided handshake (+4; the balance comes from the welcome handshake).
	sendTestHandshake(t, ts.URL, alice, aliceToken, bob)
	_, res = matchPoints(t, ts.URL, room, alice, bob, aliceToken, "3600000")
	if res["points"].(float64) != 86 || res["handshake"] != true {
		t.Fatalf("one-sided handshake: want 86, got %v", res)
	}

	// Mutual handshake: instant 100, whatever the time.
	sendTestHandshake(t, ts.URL, bob, bobToken, alice)
	_, res = matchPoints(t, ts.URL, room, alice, bob, aliceToken, "360000000000000")
	if res["points"].(float64) != 100 || res["mutual_handshake"] != true {
		t.Fatalf("mutual handshake: want instant 100, got %v", res)
	}
}
