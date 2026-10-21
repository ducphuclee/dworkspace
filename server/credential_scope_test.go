package server

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// A credential narrowed to some workspaces, and a workspace's own rule for
// agents, are both gates for writing, not only for reading. A handful of write
// paths asked the bare permission (or nothing) and let a token for one
// workspace create pages in another, export it, take back changes in it, and
// write into a workspace closed to agents. And a viewer's content went
// wherever the account's default happened to be, read-only or not.

type scopeFixture struct {
	s                *Server
	owner, cookie    string
	team, other      string
	teamTok, openTok string // narrowed to Team; not narrowed at all
}

func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	s := testServer(t)
	owner, cookie := signedIn(t, s, "owner@example.test")
	f := scopeFixture{s: s, owner: owner, cookie: cookie, team: s.firstWorkspaceOf(t, owner), other: makeWorkspace(t, s, owner)}
	f.teamTok, f.openTok = "token-for-team-only", "token-for-everything"
	for tok, scope := range map[string]string{f.teamTok: f.team, f.openTok: ""} {
		if _, err := s.db.Exec(`INSERT INTO api_tokens (id, user_id, name, token_hash, scope, workspace_scope, created_at)
			VALUES (?, ?, 'agent', ?, 'write', ?, ?)`, newID(), owner, tokenHash(tok), scope, now()); err != nil {
			t.Fatalf("insert token: %v", err)
		}
	}
	return f
}

func (f scopeFixture) bearer(tok, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	f.s.ServeHTTP(rec, req)
	return rec
}

func TestANarrowedTokenCreatesOnlyInItsWorkspaces(t *testing.T) {
	f := newScopeFixture(t)
	inOther := f.s.makePage(t, f.other, f.owner, "", "Elsewhere", `{}`)
	for _, body := range []string{
		`{"title":"x","workspaceId":"` + f.other + `"}`,
		`{"title":"x","parentId":"` + inOther + `"}`,
	} {
		if rec := f.bearer(f.teamTok, "POST", "/api/pages", body); rec.Code != 403 {
			t.Errorf("%s: %d, want 403", body, rec.Code)
		}
	}
	rec := f.bearer(f.teamTok, "POST", "/api/pages", `{"title":"no workspace named"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), f.team) {
		t.Errorf("a page without a workspace should land in the token's own: %d %s", rec.Code, rec.Body.String())
	}

	// A template from a workspace the token was not given, over MCP.
	tpl := f.s.makePage(t, f.other, f.owner, "", "Other's template", `{}`)
	f.s.db.Exec(`UPDATE pages SET is_template = 1 WHERE id = ?`, tpl)
	u := &user{ID: f.owner, TokenKind: tokenKindAPI, TokenScope: "write", TokenWorkspaces: []string{f.team}}
	if out, err := callTool(t, f.s, u, "create_page", `{"template_id":"`+tpl+`"}`); err == nil {
		t.Errorf("a template from another workspace was instantiated: %s", out)
	}
}

func TestANarrowedTokenExportsAndRevertsOnlyItsWorkspaces(t *testing.T) {
	f := newScopeFixture(t)
	if rec := f.bearer(f.teamTok, "GET", "/api/workspaces/"+f.other+"/export", ""); rec.Code != 404 {
		t.Errorf("export of a workspace outside the token: %d, want 404", rec.Code)
	}
	if rec := f.bearer(f.teamTok, "GET", "/api/workspaces/"+f.team+"/export", ""); rec.Code != 200 {
		t.Errorf("export of the token's own workspace: %d", rec.Code)
	}

	col := f.s.makeCollection(t, f.other, f.owner, "Ledger", `[{"id":"note","name":"Note","type":"text"}]`)
	row := f.s.makeRow(t, f.other, f.owner, col, "Entry", `{"note":"after"}`)
	res, err := f.s.db.Exec(`INSERT INTO audit_log (created_at, actor_type, actor_id, actor_name, action, page_id, workspace_id, detail, changes)
		VALUES (?, 'agent', ?, 'agent', 'page.props', ?, ?, '', '{"note":{"from":"before","to":"after"}}')`, now(), f.owner, row, f.other)
	if err != nil {
		t.Fatalf("audit entry: %v", err)
	}
	id, _ := res.LastInsertId()
	if rec := f.bearer(f.teamTok, "POST", "/api/audit/"+strconv.FormatInt(id, 10)+"/revert", ""); rec.Code != 403 {
		t.Errorf("revert in a workspace outside the token: %d %s, want 403", rec.Code, rec.Body.String())
	}
}

func TestAWorkspaceClosedToAgentsIsClosedForWriting(t *testing.T) {
	f := newScopeFixture(t)
	page := f.s.makePage(t, f.team, f.owner, "", "Payroll", `{}`)
	f.s.db.Exec(`UPDATE workspaces SET agent_access = 'closed' WHERE id = ?`, f.team)
	if rec := f.bearer(f.openTok, "GET", "/api/pages/"+page, ""); rec.Code != 404 {
		t.Fatalf("reading: %d, want 404 (the premise)", rec.Code)
	}
	if rec := f.bearer(f.openTok, "PATCH", "/api/pages/"+page, `{"title":"Changed by an agent"}`); rec.Code == 200 {
		t.Error("an agent changed a page in a workspace closed to agents")
	}
	if rec := f.bearer(f.openTok, "POST", "/api/pages", `{"title":"x","workspaceId":"`+f.team+`"}`); rec.Code != 403 {
		t.Errorf("an agent created a page in a workspace closed to agents: %d", rec.Code)
	}
	// Nor over MCP with a token that names no workspaces at all.
	open := &user{ID: f.owner, TokenKind: tokenKindAPI, TokenScope: "write"}
	if out, err := callTool(t, f.s, open, "get_page", `{"page_id":"`+page+`"}`); err == nil {
		t.Errorf("get_page read a page in a workspace closed to agents: %.80s", out)
	}
	if _, err := callTool(t, f.s, open, "write_content", `{"page_id":"`+page+`","markdown":"agent was here"}`); err == nil {
		t.Error("write_content wrote into a workspace closed to agents")
	}
	// Nor can it carry a page in from outside.
	outside := f.s.makePage(t, f.other, f.owner, "", "Notes from elsewhere", `{}`)
	if rec := f.bearer(f.openTok, "PATCH", "/api/pages/"+outside, `{"workspaceId":"`+f.team+`"}`); rec.Code == 200 {
		t.Error("an agent moved a page into a workspace closed to agents")
	}
	// People are not agents.
	rec := requestAs(t, f.s, f.cookie, "PATCH", "/api/pages/"+page, `{"title":"Changed by a person"}`)
	if rec.Code != 200 {
		t.Errorf("the owner in a browser: %d", rec.Code)
	}
}

func TestAViewerCreatesNothingWhereTheyMayOnlyRead(t *testing.T) {
	f := newScopeFixture(t)
	viewer, cookie := signedIn(t, f.s, "viewer@example.test")
	f.s.addMember(t, f.team, viewer, "viewer")
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/import", `{"markdown":"# Notes\n\nhello"}`},
		{"POST", "/api/reindex-siblings", `{"workspaceId":"` + f.team + `","ids":[]}`},
		{"PUT", "/api/tag-colors", `{"workspaceId":"` + f.team + `","tag":"urgent","color":"red"}`},
	} {
		if rec := requestAs(t, f.s, cookie, c.method, c.path, c.body); rec.Code != 403 {
			t.Errorf("viewer %s %s: %d %s, want 403", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	u := &user{ID: viewer}
	for _, tool := range []string{"create_page", "create_database"} {
		if out, err := callTool(t, f.s, u, tool, `{"title":"x"}`); err == nil {
			t.Errorf("viewer %s without a workspace created something: %s", tool, out)
		}
	}
	var n int
	f.s.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE owner_id = ?`, viewer).Scan(&n)
	if n != 0 {
		t.Errorf("%d pages owned by the viewer exist", n)
	}
}
