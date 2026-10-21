package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// Behind a proxy that appends to X-Forwarded-For (Cloudflare, so the built-in
// tunnel too, and nginx's usual setup), the header arrives as
// "<whatever the caller sent>, <the caller's real address>". Measured through a
// quick tunnel: a request carrying X-Forwarded-For: 203.0.113.7 was seen as
// coming from 203.0.113.7. Only the last entry is the proxy's own.

func TestClientIPBelievesOnlyTheProxysOwnEntry(t *testing.T) {
	s := testServer(t)
	ip := func(headers ...string) string {
		r := httptest.NewRequest("GET", "/api/health", nil)
		r.RemoteAddr = "127.0.0.1:52000"
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Add(headers[i], headers[i+1])
		}
		return s.clientIP(r)
	}

	// Without the setting the headers are the caller's own words.
	if got := ip("X-Forwarded-For", "203.0.113.7"); got != "127.0.0.1" {
		t.Errorf("trust_proxy off: %q, want the connection's address", got)
	}

	s.setSetting("trust_proxy", "1")
	cases := []struct {
		headers []string
		want    string
	}{
		{[]string{"X-Forwarded-For", "84.119.10.20"}, "84.119.10.20"},
		{[]string{"X-Forwarded-For", "203.0.113.7, 84.119.10.20"}, "84.119.10.20"},
		{[]string{"X-Forwarded-For", "203.0.113.7", "X-Forwarded-For", "198.51.100.1, 84.119.10.20"}, "84.119.10.20"},
		{[]string{"X-Forwarded-For", "203.0.113.7,2001:db8::5"}, "2001:db8::5"},
		{[]string{"X-Forwarded-For", "[2001:db8::5]:4711"}, "2001:db8::5"},
		{[]string{"X-Forwarded-For", "84.119.10.20:4711"}, "84.119.10.20"},
		{[]string{"X-Real-Ip", "84.119.10.20"}, "84.119.10.20"},
		{[]string{"X-Forwarded-For", "not an address"}, "127.0.0.1"},
	}
	for _, c := range cases {
		if got := ip(c.headers...); got != c.want {
			t.Errorf("%v: %q, want %q", c.headers, got, c.want)
		}
	}
}

// Rotating the first entry used to give every password guess a fresh address.
func TestRotatingForwardedForDoesNotDodgeTheLoginLimit(t *testing.T) {
	s := testServer(t)
	signedIn(t, s, "owner@example.test")
	s.setSetting("trust_proxy", "1")
	limited := false
	for i := 0; i < 15 && !limited; i++ {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"email":"owner@example.test","password":"wrong-guess"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d, 84.119.10.20", i+1))
		s.ServeHTTP(rec, r)
		limited = rec.Code == 429
	}
	if !limited {
		t.Fatal("15 wrong passwords from one real address were never throttled: the caller-written part of X-Forwarded-For is still believed")
	}
}

// And nobody can spend somebody else's budget of wrong tokens. The limiter
// refuses every bearer token from an exhausted address, valid ones included, so
// naming the address an agent connects from (a connector platform's published
// range, say) used to switch that agent off.
func TestWrongTokensCannotBeChargedToSomebodyElse(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "agent-owner@example.test")
	tok := "the-agents-good-token"
	if _, err := s.db.Exec(`INSERT INTO api_tokens (id, user_id, name, token_hash, scope, created_at)
		VALUES (?, ?, 'agent', ?, 'write', ?)`, newID(), uid, tokenHash(tok), now()); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	s.setSetting("trust_proxy", "1")
	const agent = "160.79.104.10"
	for i := 0; i < 100; i++ {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.RemoteAddr = "127.0.0.1:52000"
		r.Header.Set("Authorization", "Bearer guess")
		r.Header.Set("X-Forwarded-For", agent+", 84.119.10.20")
		s.currentUser(r)
	}
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.RemoteAddr = "127.0.0.1:52000"
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("X-Forwarded-For", agent)
	if s.currentUser(r) == nil {
		t.Fatal("the agent's valid token was refused after somebody else guessed in its name")
	}
}
