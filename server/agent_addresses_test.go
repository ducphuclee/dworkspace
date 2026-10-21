package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAddressListReadsWhatPeopleWrite(t *testing.T) {
	list, err := parseAddressList(`
# Claude's connectors
160.79.104.0/21   # Anthropic
84.119.10.20
2001:db8::/48
::ffff:192.0.2.0/120
fe80::1%eth0
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"160.79.104.0/21", "84.119.10.20/32", "2001:db8::/48", "192.0.2.0/24", "fe80::1/128"}
	if len(list) != len(want) {
		t.Fatalf("got %v, want %v", list, want)
	}
	for i, p := range list {
		if p.String() != want[i] {
			t.Errorf("entry %d: %s, want %s", i, p, want[i])
		}
	}
	for _, bad := range []string{"160.79.104.0/33", "160.79.104", "anthropic", "1.2.3.4-1.2.3.9"} {
		_, err := parseAddressList("84.119.10.20\n" + bad)
		le, ok := err.(*addressLineError)
		if !ok || le.Line != 2 || le.Text != bad {
			t.Errorf("%q: %v, want an error naming line 2", bad, err)
		}
	}
}

// agentFixture: an account with an API token and a session, and the list
// switched on with Anthropic's range only.
func agentFixture(t *testing.T) (s *Server, token, cookie string) {
	t.Helper()
	s = testServer(t)
	uid, cookie := signedIn(t, s, "agent-owner@example.test")
	token = "a-key-for-the-agent"
	if _, err := s.db.Exec(`INSERT INTO api_tokens (id, user_id, name, token_hash, scope, created_at)
		VALUES (?, ?, 'agent', ?, 'write', ?)`, newID(), uid, tokenHash(token), now()); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	s.setSetting(settingAgentAddresses, anthropicOutbound+" # Anthropic")
	s.setSetting(settingAgentAddressesOn, "1")
	return s, token, cookie
}

func call(s *Server, method, path, remote string, headers map[string]string, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote + ":51000"
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	s.ServeHTTP(rec, req)
	return rec
}

const mcpInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

func TestAgentKeysOnlyFromListedAddresses(t *testing.T) {
	s, token, cookie := agentFixture(t)
	bearer := map[string]string{"Authorization": "Bearer " + token}
	const claude, elsewhere = "160.79.104.10", "84.119.10.20"

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/pages", ""},
		{"POST", "/mcp", mcpInit},
	} {
		if rec := call(s, c.method, c.path, claude, bearer, c.body); rec.Code != 200 {
			t.Errorf("%s from a listed address: %d %s", c.path, rec.Code, rec.Body.String())
		}
		rec := call(s, c.method, c.path, elsewhere, bearer, c.body)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"agent_address_not_allowed"`) {
			t.Errorf("%s from elsewhere: %d %s, want 403 agent_address_not_allowed", c.path, rec.Code, rec.Body.String())
		}
	}
	// The key in the URL is the same key.
	if rec := call(s, "POST", "/mcp/"+token, elsewhere, nil, mcpInit); rec.Code != 403 {
		t.Errorf("/mcp/{token} from elsewhere: %d", rec.Code)
	}
	// A wrong key from elsewhere is answered the same way as a right one: the
	// refusal comes before the lookup and tells nothing about the key.
	if rec := call(s, "GET", "/api/pages", elsewhere, map[string]string{"Authorization": "Bearer nonsense"}, ""); rec.Code != 403 {
		t.Errorf("a wrong key from elsewhere: %d, want the same 403", rec.Code)
	}
	// People are not agents: a browser session works from anywhere.
	if rec := call(s, "GET", "/api/pages", elsewhere, map[string]string{"Cookie": cookie}, ""); rec.Code != 200 {
		t.Errorf("a signed-in browser from elsewhere: %d", rec.Code)
	}
	// Switched off, every address may again.
	s.setSetting(settingAgentAddressesOn, "")
	if rec := call(s, "GET", "/api/pages", elsewhere, bearer, ""); rec.Code != 200 {
		t.Errorf("switch off: %d", rec.Code)
	}
}

// Behind a proxy the list sees what clientIP sees: the proxy's own entry.
// Naming an Anthropic address at the front of X-Forwarded-For gets nowhere.
func TestAgentAddressBehindAProxy(t *testing.T) {
	s, token, _ := agentFixture(t)
	s.setSetting("trust_proxy", "1")
	bearer := func(xff string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + token, "X-Forwarded-For": xff}
	}
	if rec := call(s, "GET", "/api/pages", "127.0.0.1", bearer("160.79.104.10"), ""); rec.Code != 200 {
		t.Errorf("a listed caller through the proxy: %d", rec.Code)
	}
	if rec := call(s, "GET", "/api/pages", "127.0.0.1", bearer("160.79.104.10, 84.119.10.20"), ""); rec.Code != 403 {
		t.Errorf("a forged first entry: %d, want 403", rec.Code)
	}
}

func TestAddressListIsCheckedBeforeAnythingIsSaved(t *testing.T) {
	s := testServer(t)
	_, cookie := signedIn(t, s, "admin@example.test")
	put := func(body string) *httptest.ResponseRecorder {
		return call(s, "PUT", "/api/settings", "127.0.0.1", map[string]string{"Cookie": cookie}, body)
	}
	code := func(rec *httptest.ResponseRecorder) string {
		var b struct {
			Code string `json:"code"`
			Line int    `json:"line"`
		}
		json.Unmarshal(rec.Body.Bytes(), &b)
		return b.Code
	}

	rec := put(`{"instanceName":"Changed","agentAddressesOn":true,"agentAddresses":"160.79.104.0/21\nnot-an-address"}`)
	if rec.Code != 400 || code(rec) != "agent_addresses_invalid" || !strings.Contains(rec.Body.String(), `"line":2`) {
		t.Fatalf("a bad line: %d %s", rec.Code, rec.Body.String())
	}
	if s.setting("instance_name", "") == "Changed" || s.boolSetting(settingAgentAddressesOn) {
		t.Error("a refused save stored part of the dialog anyway")
	}
	if rec := put(`{"agentAddressesOn":true,"agentAddresses":"  # nothing but a comment"}`); rec.Code != 400 || code(rec) != "agent_addresses_empty" {
		t.Fatalf("switching on over an empty list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := put(`{"agentAddressesOn":true,"agentAddresses":"160.79.104.0/21"}`); rec.Code != 200 {
		t.Fatalf("a good list: %d %s", rec.Code, rec.Body.String())
	}
	// Emptying the list while it is on is the same mistake from the other side.
	if rec := put(`{"agentAddresses":""}`); rec.Code != 400 || code(rec) != "agent_addresses_empty" {
		t.Fatalf("emptying a list that is on: %d %s", rec.Code, rec.Body.String())
	}
	var got appSettings
	json.Unmarshal(call(s, "GET", "/api/settings", "127.0.0.1", map[string]string{"Cookie": cookie}, "").Body.Bytes(), &got)
	if !got.AgentAddressesOn || got.AgentAddresses != "160.79.104.0/21" || got.AnthropicAddresses != anthropicOutbound {
		t.Errorf("settings read back as %+v", got)
	}
	// An API token cannot change the list, admin's or not.
	tok := "admins-own-key"
	var uid string
	s.db.QueryRow(`SELECT id FROM users WHERE email = 'admin@example.test'`).Scan(&uid)
	s.db.Exec(`INSERT INTO api_tokens (id, user_id, name, token_hash, scope, created_at) VALUES (?, ?, 'k', ?, 'write', ?)`, newID(), uid, tokenHash(tok), now())
	if rec := call(s, "PUT", "/api/settings", "160.79.104.10", map[string]string{"Authorization": "Bearer " + tok}, `{"agentAddressesOn":false}`); rec.Code == 200 {
		t.Error("an API token switched the list off")
	}
}
