package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// #68: "Stop sharing does not revoke a public page". The server side always
// deleted the link; what kept a page public was the menu publishing it again
// when it was opened to check. This pins the server half: a revoked address
// answers 404 at once, and no answer about a shared page may be kept by a cache
// between the visitor and the server, where it would outlive the revoke.

func TestStopSharingEndsTheAddressAtOnce(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "author@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	page := s.makePage(t, ws, uid, "", "Launch notes", `{}`)
	token := shareForTest(t, s, cookie, page)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	for _, path := range []string{"/public/" + token, "/api/public/" + token} {
		rec := get(path)
		if rec.Code != 200 {
			t.Fatalf("%s while shared: %d", path, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("%s may be cached (Cache-Control %q); a revoke would not reach that copy", path, cc)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/pages/"+page+"/share", nil)
	req.Header.Set("Cookie", cookie)
	s.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("stop sharing: %d %s", rec.Code, rec.Body.String())
	}

	for _, path := range []string{"/public/" + token, "/api/public/" + token} {
		rec := get(path)
		if rec.Code != 404 {
			t.Errorf("%s after stop sharing: %d, want 404", path, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("%s after stop sharing may be cached (Cache-Control %q)", path, cc)
		}
	}
	if s.liveShared([]string{page})[page] {
		t.Error("the page still counts as shared after stop sharing")
	}
}
