package server

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// An inline link went into the print view and the shared page exactly as it
// was typed. A javascript: address there ran, on one click, with the session
// of whoever had the page open: the print view lives on the app's own origin,
// and so does a shared page opened by somebody who is signed in.
func TestDocumentLinksCannotRunScript(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "author@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	page := s.makePage(t, ws, uid, "", "Launch notes", `{}`)
	content := `[{"id":"b1","type":"paragraph","props":{},"content":[` +
		`{"type":"link","href":"javascript:fetch('/api/me')","content":[{"type":"text","text":"click me","styles":{}}]},` +
		`{"type":"text","text":" and ","styles":{}},` +
		`{"type":"link","href":" JavaScript:alert(1)","content":[{"type":"text","text":"me too","styles":{}}]},` +
		`{"type":"link","href":"https://example.com/handbook","content":[{"type":"text","text":"the handbook","styles":{}}]}` +
		`],"children":[]}]`
	if _, err := s.db.Exec(`UPDATE pages SET content = ? WHERE id = ?`, content, page); err != nil {
		t.Fatalf("content: %v", err)
	}

	get := func(path string, withCookie bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		if withCookie {
			req.Header.Set("Cookie", cookie)
		}
		s.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		return rec
	}
	nonceRe := regexp.MustCompile(`<script nonce="([A-Za-z0-9_-]+)">`)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"print view":  get("/api/export/"+page+"?format=html&print=1", true),
		"shared page": get("/public/"+shareForTest(t, s, cookie, page), false),
	} {
		body := strings.ToLower(rec.Body.String())
		if strings.Contains(body, `href="javascript:`) || strings.Contains(body, `href=" javascript:`) {
			t.Errorf("%s: a javascript: link is still a link", name)
		}
		if !strings.Contains(body, `href="https://example.com/handbook"`) {
			t.Errorf("%s: an ordinary link was lost", name)
		}
		if strings.Contains(body, "onclick=") {
			t.Errorf("%s: an inline handler is back; the policy below would block it", name)
		}
		// Belt and braces: whatever the renderer lets through next time, only the
		// document's own script may run.
		csp := rec.Header().Get("Content-Security-Policy")
		m := nonceRe.FindStringSubmatch(rec.Body.String())
		if m == nil || !strings.Contains(csp, "script-src 'nonce-"+m[1]+"'") {
			t.Errorf("%s: script nonce %v does not match the policy %q", name, m, csp)
		}
	}
}
