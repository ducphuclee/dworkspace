package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A share password was compared as fast as guesses arrived: 200 wrong ones in
// six hundredths of a second, then the right one opened the page. Guesses are
// limited per link and address now, on the form and on the JSON route alike,
// and the limit is checked before the password, so the right one does not get
// through either once the budget is spent.
func TestSharePasswordGuessesAreLimited(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "author@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	page := s.makePage(t, ws, uid, "", "Board minutes", `{}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/pages/"+page+"/share", strings.NewReader(`{"password":"correct horse"}`))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(rec, req)
	var share struct {
		Token string `json:"token"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &share) != nil {
		t.Fatalf("share: %d %s", rec.Code, rec.Body.String())
	}

	form := func(pw, from string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/public/"+share.Token, strings.NewReader(url.Values{"pw": {pw}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = from + ":40000"
		s.ServeHTTP(rec, req)
		return rec.Code
	}
	api := func(pw, from string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/public/"+share.Token, nil)
		req.Header.Set("X-Share-Password", pw)
		req.RemoteAddr = from + ":40000"
		s.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := form("correct horse", "198.51.100.1"); code != 200 {
		t.Fatalf("the right password: %d", code)
	}
	limited := false
	for i := 0; i < 12 && !limited; i++ {
		limited = form("guess", "203.0.113.5") == 429
	}
	if !limited {
		t.Fatal("twelve wrong passwords in a row were never slowed down")
	}
	if code := form("correct horse", "203.0.113.5"); code != 429 {
		t.Errorf("the right password past the limit: %d, want 429 like any other guess", code)
	}
	if code := api("correct horse", "203.0.113.5"); code != 429 {
		t.Errorf("the JSON route is a way around the limit: %d", code)
	}
	// Somebody else, elsewhere, is not locked out by that.
	if code := form("correct horse", "198.51.100.2"); code != 200 {
		t.Errorf("another visitor of the same link: %d", code)
	}
	if code := api("correct horse", "198.51.100.2"); code != 200 {
		t.Errorf("another visitor over the JSON route: %d", code)
	}
}
