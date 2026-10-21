package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// A shared page went out as the print view, which renders every optional part
// and hides the unchosen ones by class. For an anonymous visitor that meant
// every comment on the page, with its author, and the name of the workspace sat
// in the page source, one "view source" away.

func shareForTest(t *testing.T, s *Server, cookie, pageID string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/pages/"+pageID+"/share", strings.NewReader(`{}`))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(rec, req)
	var share struct {
		Token string `json:"token"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &share) != nil || share.Token == "" {
		t.Fatalf("share: %d %s", rec.Code, rec.Body.String())
	}
	return share.Token
}

func publicSource(t *testing.T, s *Server, token string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/public/"+token, nil))
	if rec.Code != 200 {
		t.Fatalf("public view: %d", rec.Code)
	}
	return rec.Body.String()
}

func TestSharedPageCarriesNoCommentsOrHiddenNames(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "author@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	if _, err := s.db.Exec(`UPDATE workspaces SET name = 'Internal Payroll Team' WHERE id = ?`, ws); err != nil {
		t.Fatalf("name workspace: %v", err)
	}
	page := s.makePage(t, ws, uid, "", "Launch notes", `{}`)
	content := `[{"id":"b1","type":"paragraph","props":{},"content":[{"type":"text","text":"Public paragraph","styles":{}}],"children":[]}]`
	if _, err := s.db.Exec(`UPDATE pages SET content = ? WHERE id = ?`, content, page); err != nil {
		t.Fatalf("content: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/pages/"+page+"/comments", strings.NewReader(`{"body":"Do not ship before legal signs off"}`))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(rec, req)
	if rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("comment: %d %s", rec.Code, rec.Body.String())
	}

	// Both print switches as an instance might set them; neither may let the
	// comment out to somebody without an account.
	s.setSetting("pdf_comments", "1")
	src := publicSource(t, s, shareForTest(t, s, cookie, page))
	if !strings.Contains(src, "Public paragraph") {
		t.Fatal("the shared page lost its own content")
	}
	if strings.Contains(src, "legal signs off") || strings.Contains(src, `<section class="doc-comments">`) {
		t.Error("a comment on the page is in the source anybody with the link receives")
	}
	// No cover by default: the workspace name would only sit hidden in a
	// template nobody sees.
	if strings.Contains(src, "Internal Payroll Team") {
		t.Error("the workspace name is in the source although the page does not show it")
	}

	// With a cover that names its origin the name is visible by design, so it
	// may be there; the rule is "nothing hidden", not "never".
	s.setSetting("pdf_cover", "1")
	s.setSetting("pdf_workspace", "1")
	if src := publicSource(t, s, shareForTest(t, s, cookie, page)); !strings.Contains(src, "Internal Payroll Team") {
		t.Error("a cover set to name the workspace no longer does")
	}
}
