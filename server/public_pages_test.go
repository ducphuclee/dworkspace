package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func publicGet(t *testing.T, s *Server, path string) (int, string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Header().Get("Content-Type"), rec.Body.String()
}

// A crawler with no cookie and no JS must get real content on each page.
func TestPublicPagesServeWithoutAuth(t *testing.T) {
	t.Setenv("DWORKSPACE_PUBLIC_APP_NAME", "Vườn Ươm")
	t.Setenv("DWORKSPACE_PUBLIC_CONTACT_EMAIL", "hello@example.com")
	s := testServer(t)
	for _, p := range []string{"/about", "/privacy", "/terms"} {
		code, ct, body := publicGet(t, s, p)
		if code != 200 {
			t.Fatalf("%s: status %d", p, code)
		}
		if !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: content-type %q", p, ct)
		}
		if !strings.Contains(body, "<title>Vườn Ươm</title>") || !strings.Contains(body, "<h1>Vườn Ươm</h1>") {
			t.Errorf("%s: app name missing from title/h1", p)
		}
		if !strings.Contains(body, "hello@example.com") {
			t.Errorf("%s: contact missing", p)
		}
	}
}

// Google's reviewer looks for the scope justification and the Limited Use text.
func TestPublicPrivacyStatesGoogleScopesAndLimitedUse(t *testing.T) {
	s := testServer(t)
	_, _, body := publicGet(t, s, "/privacy")
	for _, want := range []string{"gmail.send", "Limited Use"} {
		if !strings.Contains(body, want) {
			t.Errorf("privacy lacks %q", want)
		}
	}
}

// Name falls back to the instance_name setting, then "dworkspace", and the
// instance name is admin-controlled, so it must be escaped.
func TestPublicPagesNameFallbackAndEscaping(t *testing.T) {
	s := testServer(t)
	_, _, body := publicGet(t, s, "/about")
	if !strings.Contains(body, "<title>dworkspace</title>") {
		t.Errorf("default name not dworkspace")
	}
	evil := `<script>alert(1)</script>`
	if _, err := s.db.Exec(`INSERT INTO app_settings (key, value) VALUES ('instance_name', ?)`, evil); err != nil {
		t.Fatal(err)
	}
	_, _, body = publicGet(t, s, "/about")
	if strings.Contains(body, evil) {
		t.Fatal("instance name rendered unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected escaped instance name")
	}
}
