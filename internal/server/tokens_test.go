package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/extube/bleak-node/internal/db"
)

// bearerReq builds a request with an Authorization header.
func bearerReq(t *testing.T, method, url, token, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// doJSON performs req and decodes the JSON body into out (may be nil).
func doJSON(t *testing.T, req *http.Request) (int, *http.Response) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s: %v", req.URL, err)
	}
	return resp.StatusCode, resp
}

func TestCreateRoomGating(t *testing.T) {
	ts, database := credTestServer(t)
	createRaw := seedCreateToken(t, database)
	roomToken := newTestSecret(t)
	body := fmt.Sprintf(`{"name":"Gated","token":%q}`, roomToken)

	// Missing header → 401.
	resp, err := http.Post(ts.URL+"/api/room", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post room: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no header status = %d, want 401", resp.StatusCode)
	}

	// Unknown creation token → 401.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/room", strings.NewReader(body))
	req.Header.Set(createTokenHeader, "wrong-token-value-at-least-32-symbols-long!!")
	if code, resp := doJSON(t, req); code != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("bad creation token status = %d, want 401", code)
	} else {
		resp.Body.Close()
	}

	// Body without the client-generated room token → 400 (the creation
	// token is NOT burned by a malformed body).
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/room", strings.NewReader(`{"name":"NoToken"}`))
	req.Header.Set(createTokenHeader, createRaw)
	if code, resp := doJSON(t, req); code != http.StatusBadRequest {
		resp.Body.Close()
		t.Fatalf("missing body token status = %d, want 400", code)
	} else {
		resp.Body.Close()
	}

	// Valid: 201, room + first invite code, and the room token is NOT in
	// the response (raw secrets are never returned).
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/room", strings.NewReader(body))
	req.Header.Set(createTokenHeader, createRaw)
	code, resp := doJSON(t, req)
	defer resp.Body.Close()
	if code != http.StatusCreated {
		t.Fatalf("valid create status = %d, want 201", code)
	}
	var out struct {
		Address    string `json:"address"`
		InviteCode string `json:"invite_code"`
		Token      string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Address == "" || len(out.InviteCode) != 6 {
		t.Fatalf("response = %+v", out)
	}
	if out.Token != "" {
		t.Fatal("the room token must never be returned")
	}

	// The creation token was consumed: reuse → 403.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/room", strings.NewReader(body))
	req.Header.Set(createTokenHeader, createRaw)
	code, resp = doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusForbidden {
		t.Fatalf("reused creation token status = %d, want 403", code)
	}
}

func TestJoinGating(t *testing.T) {
	ts, database := credTestServer(t)
	room, _, invite := createTestRoom(t, ts, database, "Join Room")
	join := func(token, code string) int {
		body := fmt.Sprintf(`{"name":"Jo","token":%q,"invite_code":%q}`, token, code)
		resp, err := http.Post(ts.URL+"/api/room/"+room+"/user", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("join: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	token := newTestSecret(t)

	// Missing invite code → 400; wrong code → 403.
	if got := join(token, ""); got != http.StatusBadRequest {
		t.Fatalf("missing invite status = %d, want 400", got)
	}
	if got := join(token, "000000"); got != http.StatusForbidden {
		t.Fatalf("wrong invite status = %d, want 403", got)
	}

	// Valid code joins; the persona token authenticates persona actions.
	if got := join(token, invite); got != http.StatusCreated {
		t.Fatalf("valid join status = %d, want 201", got)
	}

	// The join attempts of one source are rate-limited (429): keep trying
	// wrong codes until the window fills.
	limited := false
	for i := 0; i < 20; i++ {
		if got := join(newTestSecret(t), "999999"); got == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("join attempts were never rate-limited")
	}
}

func TestGetUserPrivateBioGating(t *testing.T) {
	ts, database := credTestServer(t)
	room, _, invite := createTestRoom(t, ts, database, "Bio Room")
	addr, token := joinTestRoom(t, ts, room, invite, "Secretive")
	// Set a private bio (token-gated mutation).
	req := bearerReq(t, http.MethodPut, ts.URL+"/api/room/"+room+"/user/"+addr,
		token, `{"private_bio":"the real me"}`)
	code, resp := doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusOK {
		t.Fatalf("put private bio status = %d, want 200", code)
	}

	readUser := func(bearer string) (string, bool) {
		req, _ := http.NewRequest(http.MethodGet,
			ts.URL+"/api/room/"+room+"/user/"+addr, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		code, resp := doJSON(t, req)
		defer resp.Body.Close()
		if code != http.StatusOK {
			t.Fatalf("get user status = %d, want 200", code)
		}
		var u struct {
			PrivateBio     string `json:"private_bio"`
			MutualCodeLive *bool  `json:"mutual_code_live"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
			t.Fatalf("decode user: %v", err)
		}
		return u.PrivateBio, u.MutualCodeLive != nil && *u.MutualCodeLive
	}

	// Anonymous read: public fields only.
	if bio, live := readUser(""); bio != "" || live {
		t.Fatalf("anonymous read: private_bio=%q mutual_code_live=%t", bio, live)
	}
	// Own token: private bio + derived live flag (no code registered yet).
	if bio, live := readUser(token); bio != "the real me" || live {
		t.Fatalf("token read: private_bio=%q mutual_code_live=%t", bio, live)
	}

	// Registering a one-time code flips the derived flag.
	req = bearerReq(t, http.MethodPost, ts.URL+"/api/room/"+room+"/user/"+addr+"/mutual-code",
		token, `{"mutual_code_hash":"`+strings.Repeat("a", 64)+`"}`)
	code, resp = doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusOK {
		t.Fatalf("register mutual code status = %d, want 200", code)
	}
	if bio, live := readUser(token); !live {
		t.Fatalf("after registration: private_bio=%q mutual_code_live=%t", bio, live)
	}

	// Someone else's token is still an anonymous-equivalent read.
	if bio, _ := readUser(newTestSecret(t)); bio != "" {
		t.Fatalf("foreign token leaked private_bio=%q", bio)
	}
}

func TestTokenRotation(t *testing.T) {
	ts, database := credTestServer(t)
	room, roomToken, invite := createTestRoom(t, ts, database, "Rotate Room")
	addr, userToken := joinTestRoom(t, ts, room, invite, "Rotator")

	rotate := func(path, bearer, newToken string) int {
		req := bearerReq(t, http.MethodPost, ts.URL+path, bearer,
			fmt.Sprintf(`{"token":%q}`, newToken))
		code, resp := doJSON(t, req)
		resp.Body.Close()
		return code
	}

	// Persona rotation: current token required; old token dies.
	newUser := newTestSecret(t)
	if code := rotate("/api/room/"+room+"/user/"+addr+"/token", userToken, newUser); code != http.StatusOK {
		t.Fatalf("rotate user token status = %d, want 200", code)
	}
	req := bearerReq(t, http.MethodPut, ts.URL+"/api/room/"+room+"/user/"+addr,
		userToken, `{"name":"Renamed"}`)
	code, resp := doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusUnauthorized {
		t.Fatalf("old persona token status = %d, want 401", code)
	}
	req = bearerReq(t, http.MethodPut, ts.URL+"/api/room/"+room+"/user/"+addr,
		newUser, `{"name":"Renamed"}`)
	code, resp = doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusOK {
		t.Fatalf("new persona token status = %d, want 200", code)
	}

	// Room rotation: same contract.
	newRoom := newTestSecret(t)
	if code := rotate("/api/room/"+room+"/token", roomToken, newRoom); code != http.StatusOK {
		t.Fatalf("rotate room token status = %d, want 200", code)
	}
	req = bearerReq(t, http.MethodPut, ts.URL+"/api/room/"+room,
		roomToken, `{"name":"Renamed Room"}`)
	code, resp = doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusUnauthorized {
		t.Fatalf("old room token status = %d, want 401", code)
	}
	req = bearerReq(t, http.MethodPut, ts.URL+"/api/room/"+room,
		newRoom, `{"name":"Renamed Room"}`)
	code, resp = doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusOK {
		t.Fatalf("new room token status = %d, want 200", code)
	}
}

func TestMutualHandshakeEndpoint(t *testing.T) {
	ts, database := credTestServer(t)
	room, _, invite := createTestRoom(t, ts, database, "QR Room")
	owner, ownerToken := joinTestRoom(t, ts, room, invite, "BadgeOwner")
	_, scannerToken := joinTestRoom(t, ts, room, invite, "Scanner")

	codeHash := strings.Repeat("5a", 32)
	req := bearerReq(t, http.MethodPost, ts.URL+"/api/room/"+room+"/user/"+owner+"/mutual-code",
		ownerToken, `{"mutual_code_hash":"`+codeHash+`"}`)
	if code, resp := doJSON(t, req); code != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("register code status = %d, want 200", code)
	} else {
		resp.Body.Close()
	}

	// Scan with the WRONG code → 403, the code stays live.
	req = bearerReq(t, http.MethodPost, ts.URL+"/api/handshake/mutual", scannerToken,
		`{"user_address":"`+owner+`","mutual_code":"bad-code"}`)
	if code, resp := doJSON(t, req); code != http.StatusForbidden {
		resp.Body.Close()
		t.Fatalf("wrong code status = %d, want 403", code)
	} else {
		resp.Body.Close()
	}

	// Scan without a token → 401.
	req = bearerReq(t, http.MethodPost, ts.URL+"/api/handshake/mutual", "",
		`{"user_address":"`+owner+`","mutual_code":"x"}`)
	if code, resp := doJSON(t, req); code != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("no token status = %d, want 401", code)
	} else {
		resp.Body.Close()
	}

	// Unknown scanned address → 404 (the code is untouched).
	req = bearerReq(t, http.MethodPost, ts.URL+"/api/handshake/mutual", scannerToken,
		`{"user_address":"0xmissing","mutual_code":"x"}`)
	if code, resp := doJSON(t, req); code != http.StatusNotFound {
		resp.Body.Close()
		t.Fatalf("unknown target status = %d, want 404", code)
	} else {
		resp.Body.Close()
	}
}

func TestMutualHandshakeFullFlow(t *testing.T) {
	ts, database := credTestServer(t)
	room, _, invite := createTestRoom(t, ts, database, "QR Flow")
	owner, ownerToken := joinTestRoom(t, ts, room, invite, "Owner")
	_, scannerToken := joinTestRoom(t, ts, room, invite, "Visitor")

	// Owner registers the hash of the code their badge displays.
	rawCode := "one-time-code-0123456789abcdef"
	req := bearerReq(t, http.MethodPost, ts.URL+"/api/room/"+room+"/user/"+owner+"/mutual-code",
		ownerToken, `{"mutual_code_hash":"`+db.HashToken(rawCode)+`"}`)
	if code, resp := doJSON(t, req); code != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("register status = %d, want 200", code)
	} else {
		resp.Body.Close()
	}

	// Scanner scans: both handshakes + the chat come back; the raw code
	// never appears in the response.
	req = bearerReq(t, http.MethodPost, ts.URL+"/api/handshake/mutual", scannerToken,
		fmt.Sprintf(`{"user_address":%q,"mutual_code":%q}`, owner, rawCode))
	code, resp := doJSON(t, req)
	if code != http.StatusCreated {
		resp.Body.Close()
		t.Fatalf("mutual status = %d, want 201", code)
	}
	var out struct {
		Handshakes []struct {
			FromAddress string `json:"from_address"`
			ToAddress   string `json:"to_address"`
		} `json:"handshakes"`
		Chat struct {
			Address string `json:"address"`
		} `json:"chat"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode mutual: %v", err)
	}
	resp.Body.Close()
	if len(out.Handshakes) != 2 || out.Chat.Address == "" {
		t.Fatalf("mutual response = %+v", out)
	}
	// The raw code never comes back in a response.
	if raw := respToString(out); strings.Contains(raw, rawCode) {
		t.Fatalf("the raw code leaked into the response: %s", raw)
	}

	// Replay: 410 "code already used".
	req = bearerReq(t, http.MethodPost, ts.URL+"/api/handshake/mutual", scannerToken,
		fmt.Sprintf(`{"user_address":%q,"mutual_code":%q}`, owner, rawCode))
	code, resp = doJSON(t, req)
	defer resp.Body.Close()
	if code != http.StatusGone {
		t.Fatalf("replay status = %d, want 410", code)
	}

	// The owner re-registers a fresh code; scanning the SAME pair again
	// burns it but returns 200 (nothing new is created; one chat per pair).
	req = bearerReq(t, http.MethodPost, ts.URL+"/api/room/"+room+"/user/"+owner+"/mutual-code",
		ownerToken, `{"mutual_code_hash":"`+db.HashToken("second-code")+`"}`)
	if code, resp := doJSON(t, req); code != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("re-register status = %d, want 200", code)
	} else {
		resp.Body.Close()
	}
	req = bearerReq(t, http.MethodPost, ts.URL+"/api/handshake/mutual", scannerToken,
		fmt.Sprintf(`{"user_address":%q,"mutual_code":%q}`, owner, "second-code"))
	code, resp = doJSON(t, req)
	if code != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("second scan of the same pair status = %d, want 200", code)
	}
	var again struct {
		Handshakes []struct{} `json:"handshakes"`
		Chat       struct {
			Address string `json:"address"`
		} `json:"chat"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&again); err != nil {
		t.Fatalf("decode second mutual: %v", err)
	}
	resp.Body.Close()
	if len(again.Handshakes) != 2 || again.Chat.Address != out.Chat.Address {
		t.Fatalf("second scan response = %+v (same chat expected)", again)
	}
}

// respToString renders v as JSON for leak assertions.
func respToString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestInviteManagement(t *testing.T) {
	ts, database := credTestServer(t)
	room, roomToken, invite := createTestRoom(t, ts, database, "Invite Room")

	// Minting requires the room token.
	req := bearerReq(t, http.MethodPost, ts.URL+"/api/room/"+room+"/invite", "",
		`{}`)
	code, resp := doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusUnauthorized {
		t.Fatalf("mint without token status = %d, want 401", code)
	}

	req = bearerReq(t, http.MethodPost, ts.URL+"/api/room/"+room+"/invite", roomToken, `{}`)
	code, resp = doJSON(t, req)
	if code != http.StatusCreated {
		resp.Body.Close()
		t.Fatalf("mint status = %d, want 201", code)
	}
	var minted struct {
		Code string `json:"code"`
	}
	json.NewDecoder(resp.Body).Decode(&minted)
	resp.Body.Close()
	if len(minted.Code) != 6 {
		t.Fatalf("minted code = %q", minted.Code)
	}

	// List shows both active codes.
	req = bearerReq(t, http.MethodGet, ts.URL+"/api/room/"+room+"/invite", roomToken, "")
	code, resp = doJSON(t, req)
	if code != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("list status = %d, want 200", code)
	}
	var list struct {
		Invites []struct{ Code string `json:"code"` } `json:"invites"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Invites) != 2 {
		t.Fatalf("active invites = %d, want 2", len(list.Invites))
	}

	// Bad expiry → 400.
	req = bearerReq(t, http.MethodPost, ts.URL+"/api/room/"+room+"/invite", roomToken,
		`{"expires_at":"not-a-time"}`)
	code, resp = doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusBadRequest {
		t.Fatalf("bad expiry status = %d, want 400", code)
	}

	// Revocation is immediate and the revoked code no longer joins.
	req = bearerReq(t, http.MethodDelete, ts.URL+"/api/room/"+room+"/invite/"+invite, roomToken, "")
	code, resp = doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusOK {
		t.Fatalf("revoke status = %d, want 200", code)
	}
	body := fmt.Sprintf(`{"name":"Late","token":%q,"invite_code":%q}`, newTestSecret(t), invite)
	resp2, err := http.Post(ts.URL+"/api/room/"+room+"/user", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("join with revoked code status = %d, want 403", resp2.StatusCode)
	}

	// Revoking an unknown code → 404.
	req = bearerReq(t, http.MethodDelete, ts.URL+"/api/room/"+room+"/invite/000001", roomToken, "")
	code, resp = doJSON(t, req)
	resp.Body.Close()
	if code != http.StatusNotFound {
		t.Fatalf("unknown revoke status = %d, want 404", code)
	}
}

func TestResolveInvite(t *testing.T) {
	ts, database := credTestServer(t)
	room, _, invite := createTestRoom(t, ts, database, "Resolve Room")

	resp, err := http.Get(ts.URL + "/api/invite/" + invite)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: %v %s", err, resp.Status)
	}
	var out struct {
		Room string `json:"room"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Room != room {
		t.Fatalf("resolve = %q, want %q", out.Room, room)
	}

	resp, err = http.Get(ts.URL + "/api/invite/000000")
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("resolve unknown: %v %s", err, resp.Status)
	}
	resp.Body.Close()
}
