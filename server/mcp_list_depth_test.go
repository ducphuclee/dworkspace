package server

import (
	"fmt"
	"strings"
	"testing"
)

// The page tree, which was the worst offender of the two.
//
// list with kind="pages" rendered every page at every depth. On the instance
// this was built against that is 2539 lines and 305,000 characters — and it was
// measured twice in one session by exceeding an agent's token limit outright,
// so the answer had to be written to a file and grepped. Nothing about the call
// warned anybody; it simply returned more than could be read.

// deepTree builds roots with children and grandchildren, so a depth limit has
// something to actually cut.
func deepTree(t *testing.T, s *Server, ws, uid string) {
	t.Helper()
	mk := func(id, title string, parent any) {
		if _, err := s.db.Exec(`INSERT INTO pages (id, parent_id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility)
			VALUES (?, ?, ?, '[]', 0, ?, ?, ?, ?, 'workspace')`, id, parent, title, now(), now(), ws, uid); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	mk("root", "Handbuch", nil)
	for i := 1; i <= 3; i++ {
		kid := fmt.Sprintf("kid%d", i)
		mk(kid, fmt.Sprintf("Kapitel %d", i), "root")
		for j := 1; j <= 4; j++ {
			mk(fmt.Sprintf("gk%d%d", i, j), fmt.Sprintf("Abschnitt %d.%d", i, j), kid)
		}
	}
}

// Two levels by default: the roots and what hangs off them, and no further.
func TestThePageTreeStopsAtTheDefaultDepth(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	deepTree(t, s, ws, uid)

	out, err := s.mcpListPages(s.userByID(uid), "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "Handbuch") || !strings.Contains(out, "Kapitel 1") {
		t.Errorf("the first two levels are not both there:\n%s", out)
	}
	if strings.Contains(out, "Abschnitt 1.1") {
		t.Errorf("the third level came along, so the depth limit does nothing:\n%s", out)
	}
}

// Cutting is only half of it. An answer that silently stops is worse than one
// that never stopped, because the agent cannot tell a leaf from a truncation.
func TestTheTreeSaysWhatItLeftOutAndHowToOpenIt(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	deepTree(t, s, ws, uid)

	out, err := s.mcpListPages(s.userByID(uid), "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// The count matters: "there is more" is true of almost anything, "4 more"
	// is something to act on.
	if !strings.Contains(out, "4 more below") {
		t.Errorf("the tree does not say how much it hid:\n%s", out)
	}
	// And the id to ask with, or the hint is unusable.
	if !strings.Contains(out, "under: kid1") {
		t.Errorf("the tree does not say how to open the branch:\n%s", out)
	}
}

// under opens one branch, and only that branch.
func TestUnderOpensOneBranch(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	deepTree(t, s, ws, uid)

	out, err := s.mcpListPages(s.userByID(uid), "kid1", 0)
	if err != nil {
		t.Fatalf("list under: %v", err)
	}
	if !strings.Contains(out, "Abschnitt 1.1") {
		t.Errorf("the branch did not open:\n%s", out)
	}
	if strings.Contains(out, "Kapitel 2") || strings.Contains(out, "Abschnitt 2.1") {
		t.Errorf("under returned more than the branch asked for:\n%s", out)
	}
}

// A deliberate depth still gets the whole thing — the limit is a default, not
// a ceiling.
func TestAHigherDepthShowsMore(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	deepTree(t, s, ws, uid)

	out, err := s.mcpListPages(s.userByID(uid), "", 5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "Abschnitt 3.4") {
		t.Errorf("depth 5 still hid the third level:\n%s", out)
	}
	if strings.Contains(out, "more below") {
		t.Errorf("nothing should be left to open at depth 5:\n%s", out)
	}
}

// A shallow tree is untouched: no counts, no hints, nothing about depth. Same
// rule as the page outline — whoever cannot have the problem never meets the
// mechanism.
func TestAShallowTreeCarriesNoTruncationNotice(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	if _, err := s.db.Exec(`INSERT INTO pages (id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility)
		VALUES ('p1', 'Allein', '[]', 0, ?, ?, ?, ?, 'workspace')`, now(), now(), ws, uid); err != nil {
		t.Fatalf("insert: %v", err)
	}
	out, err := s.mcpListPages(s.userByID(uid), "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out, "not shown") || strings.Contains(out, "more below") {
		t.Errorf("a shallow tree was given a truncation notice:\n%s", out)
	}
}

// under is a read, so it obeys read permissions like everything else.
func TestUnderRefusesAPageYouCannotRead(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	deepTree(t, s, ws, uid)

	other, _ := signedIn(t, s, "other@example.com")
	if _, err := s.mcpListPages(s.userByID(other), "kid1", 0); err == nil {
		t.Error("a stranger opened a branch of somebody else's tree")
	}
}

// What the depth limit did NOT fix, found by running the shipped code against a
// real instance instead of against the fixture above.
//
// The tree was 300,000 characters because it listed every DATABASE ROW as a
// page: 2393 of 2472 lines were the rows of a few boards. They sit at depth 2,
// so a depth limit of 2 left every one of them in place — the answer shrank by
// two percent. The fixture in this file is all documents, so it could never
// have shown that, and the tests passed on a change that did nearly nothing.
//
// The browser never had this problem: handleListPages has always applied
// liveTreeFilter, which keeps rows out of the tree. The MCP copy simply did not
// use it.
func boardWithRows(t *testing.T, s *Server, ws, uid string, rows int) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO pages (id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility, type)
		VALUES ('board', 'Aufgaben', '[]', 0, ?, ?, ?, ?, 'workspace', 'collection')`, now(), now(), ws, uid); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO collections (page_id, schema, views) VALUES ('board', '[]', '[]')`); err != nil {
		t.Fatalf("insert collection: %v", err)
	}
	for i := 0; i < rows; i++ {
		if _, err := s.db.Exec(`INSERT INTO pages (id, parent_id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility)
			VALUES (?, 'board', ?, '[]', ?, ?, ?, ?, ?, 'workspace')`,
			fmt.Sprintf("row%d", i), fmt.Sprintf("Aufgabe %d", i), i, now(), now(), ws, uid); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
}

// A board is one line, not one line per task.
func TestDatabaseRowsAreNotListedAsPages(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	boardWithRows(t, s, ws, uid, 60)

	out, err := s.mcpListPages(s.userByID(uid), "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "Aufgaben") {
		t.Fatalf("the board itself is missing:\n%s", out)
	}
	if strings.Contains(out, "Aufgabe 7") {
		t.Errorf("the board's rows are listed as pages — this is what made the tree 300k:\n%s", out)
	}
	// The rows are not shown, so the count is the only thing saying the board is
	// not empty.
	if !strings.Contains(out, "60 rows") {
		t.Errorf("the board gives no idea how much is in it:\n%s", out)
	}
}

// A row that has sub-pages of its own still appears, or those sub-pages would
// be unreachable from the tree. Same rule the browser applies.
func TestARowWithSubPagesStaysInTheTree(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	boardWithRows(t, s, ws, uid, 3)
	if _, err := s.db.Exec(`INSERT INTO pages (id, parent_id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility)
		VALUES ('note', 'row1', 'Notiz zur Aufgabe', '[]', 0, ?, ?, ?, ?, 'workspace')`, now(), now(), ws, uid); err != nil {
		t.Fatalf("insert note: %v", err)
	}
	out, err := s.mcpListPages(s.userByID(uid), "", 4)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "Notiz zur Aufgabe") {
		t.Errorf("a page hanging off a row became unreachable:\n%s", out)
	}
	if strings.Contains(out, "Aufgabe 2") {
		t.Errorf("a row without sub-pages is still listed:\n%s", out)
	}
}

// Templates are not tree pages either — list(kind: "templates") is where they
// belong, and the browser has always kept them out of this answer.
func TestTemplatesStayOutOfThePageTree(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	if _, err := s.db.Exec(`INSERT INTO pages (id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility, is_template)
		VALUES ('t1', 'Vorlage Bericht', '[]', 0, ?, ?, ?, ?, 'workspace', 1)`, now(), now(), ws, uid); err != nil {
		t.Fatalf("insert template: %v", err)
	}
	out, err := s.mcpListPages(s.userByID(uid), "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out, "Vorlage Bericht") {
		t.Errorf("a template is listed as a page:\n%s", out)
	}
}
