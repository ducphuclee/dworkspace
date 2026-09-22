package server

import (
	"strings"
	"testing"
)

// The incident these tests come from, reported from a live instance:
//
//	an agent is told to log a task in Stockbook, finds the right board, and at
//	the moment of writing notices a workspace called Dtrade whose boards are
//	named almost identically. It says "odd, this looks just like Stockbook",
//	logs into Dtrade, notices, deletes the row, and logs again in Stockbook.
//
// Two separate defects made that possible, and both are covered here.
//
// The agent could not tell the two apart, because the answers it navigates by
// did not say which workspace anything was in — list merged every workspace
// into one flat tree and search returned bare titles.
//
// And when it chose wrong, the write SUCCEEDED. It only got fixed because the
// agent happened to notice. Nothing in the system was checking, even though the
// agent had stated, in the same call, which workspace it believed it was in.

// soleWorkspace returns the one workspace a fresh account has, and fails if
// the fixture quietly grew a second — which is the only thing that could make
// a "nobody asks a single-workspace account" test pass for the wrong reason.
func soleWorkspace(t *testing.T, s *Server, uid string) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT workspace_id FROM workspace_members WHERE user_id = ?`, uid)
	if err != nil {
		t.Fatalf("read memberships: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("this account is in %d workspaces, want 1", len(ids))
	}
	return ids[0]
}

// twoLookalikes builds the situation: one person, two workspaces, a board of
// the same name in each.
func twoLookalikes(t *testing.T, s *Server) (uid, cookie, stockbook, dtrade, sbBoard, dtBoard string) {
	t.Helper()
	uid, cookie = signedIn(t, s, "a@example.com")
	stockbook = makeWorkspace(t, s, uid)
	dtrade = makeWorkspace(t, s, uid)
	if _, err := s.db.Exec(`UPDATE workspaces SET name = 'Stockbook' WHERE id = ?`, stockbook); err != nil {
		t.Fatalf("name stockbook: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE workspaces SET name = 'Dtrade' WHERE id = ?`, dtrade); err != nil {
		t.Fatalf("name dtrade: %v", err)
	}
	mk := func(id, ws string) {
		if _, err := s.db.Exec(`INSERT INTO pages (id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility, type)
			VALUES (?, 'Tasks', '[]', 0, ?, ?, ?, ?, 'workspace', 'collection')`, id, now(), now(), ws, uid); err != nil {
			t.Fatalf("insert board %s: %v", id, err)
		}
		if _, err := s.db.Exec(`INSERT INTO collections (page_id, schema, views) VALUES (?, '[]', '[]')`, id); err != nil {
			t.Fatalf("insert collection %s: %v", id, err)
		}
	}
	sbBoard, dtBoard = "board-stockbook", "board-dtrade"
	mk(sbBoard, stockbook)
	mk(dtBoard, dtrade)
	return
}

// The exact wrong write, refused. The agent says Stockbook and hands over
// Dtrade's board — which is what "it looked just like Stockbook" produces.
func TestSayingOneWorkspaceAndPickingAnotherIsRefused(t *testing.T) {
	s := testServer(t)
	uid, _, stockbook, _, _, dtBoard := twoLookalikes(t, s)
	u := s.userByID(uid)

	err := s.mcpConfirmPageWorkspace(u, stockbook, dtBoard, "that database")
	if err == nil {
		t.Fatal("the write was allowed: the agent named Stockbook and was given Dtrade's board")
	}
	// The message has to name BOTH, or the agent cannot tell which of the two
	// ids it got wrong.
	for _, want := range []string{"Stockbook", "Dtrade", "that database"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// And the same call with the two in agreement goes through.
func TestSayingTheWorkspaceYouArePickingFromIsAllowed(t *testing.T) {
	s := testServer(t)
	uid, _, stockbook, _, sbBoard, _ := twoLookalikes(t, s)
	u := s.userByID(uid)

	if err := s.mcpConfirmPageWorkspace(u, stockbook, sbBoard, "that database"); err != nil {
		t.Fatalf("a correct write was refused: %v", err)
	}
}

// Silence is not consent once there is more than one place a write could land.
// This is where the old code picked "your first workspace" and said nothing.
func TestWritingWithoutSayingWhereIsRefusedWhenThereIsAChoice(t *testing.T) {
	s := testServer(t)
	uid, _, _, _, _, dtBoard := twoLookalikes(t, s)
	u := s.userByID(uid)

	err := s.mcpConfirmPageWorkspace(u, "", dtBoard, "that database")
	if err == nil {
		t.Fatal("a write with no workspace_id went through while two workspaces were reachable")
	}
	if !strings.Contains(err.Error(), "Stockbook") || !strings.Contains(err.Error(), "Dtrade") {
		t.Errorf("the refusal should list what can be written to: %v", err)
	}
}

// The refusal must NOT give away where the object actually is.
//
// The first version of this did, and it defeats the whole mechanism: an error
// reading "it is in Dtrade, pass that id" means whatever the agent echoes back
// is no longer an independent statement of intent, and the comparison is
// between a value and its own copy.
func TestTheRefusalDoesNotHandOverTheAnswer(t *testing.T) {
	s := testServer(t)
	uid, _, _, dtrade, _, dtBoard := twoLookalikes(t, s)
	u := s.userByID(uid)

	err := s.mcpConfirmPageWorkspace(u, "", dtBoard, "that database")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	// Naming both workspaces as CHOICES is fine and necessary. Saying which one
	// this page is in is not.
	for _, giveaway := range []string{
		"is in \"Dtrade\"", "that database is in", "Dtrade (id: " + dtrade + ") is",
	} {
		if strings.Contains(err.Error(), giveaway) {
			t.Errorf("the refusal reveals the answer (%q): %v", giveaway, err)
		}
	}
}

// Nobody with one workspace is ever asked. This is the objection the whole
// design had to survive: a requirement that fires for people who cannot
// possibly have the problem is a requirement they will route around.
func TestOneWorkspaceIsNeverAskedToNameIt(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	// No makeWorkspace: signing up already creates one, and the point of this
	// test is an account that has exactly that and nothing else.
	ws := soleWorkspace(t, s, uid)
	u := s.userByID(uid)
	if _, err := s.db.Exec(`INSERT INTO pages (id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility)
		VALUES ('p1', 'Solo', '[]', 0, ?, ?, ?, ?, 'workspace')`, now(), now(), ws, uid); err != nil {
		t.Fatalf("insert page: %v", err)
	}

	if err := s.mcpConfirmPageWorkspace(u, "", "p1", "that page"); err != nil {
		t.Errorf("a single-workspace account was asked to name its only workspace: %v", err)
	}
	got, err := s.mcpPlacementWorkspace(u, "", "", "")
	if err != nil {
		t.Fatalf("a top-level create in the only workspace was refused: %v", err)
	}
	if got != ws {
		t.Errorf("landed in %q, want the only workspace %q", got, ws)
	}
}

// A page id that does not resolve is left to the tool behind it, which is about
// to say "page not found" — the more useful of the two errors.
func TestAnUnknownPageIsNotAnsweredWithAWorkspaceComplaint(t *testing.T) {
	s := testServer(t)
	uid, _, stockbook, _, _, _ := twoLookalikes(t, s)
	u := s.userByID(uid)
	if err := s.mcpConfirmPageWorkspace(u, stockbook, "no-such-page", "that page"); err != nil {
		t.Errorf("a missing page produced a workspace error instead of passing through: %v", err)
	}
}

// The other half: the answers an agent navigates by have to say which workspace
// each thing is in, or it picks the wrong board before any check can run.
func TestTheePageTreeNamesTheWorkspaceOfEachRoot(t *testing.T) {
	s := testServer(t)
	uid, _, _, _, _, _ := twoLookalikes(t, s)
	u := s.userByID(uid)

	out, err := s.mcpListPages(u)
	if err != nil {
		t.Fatalf("list pages: %v", err)
	}
	if !strings.Contains(out, "Stockbook") || !strings.Contains(out, "Dtrade") {
		t.Errorf("the tree does not name the workspaces:\n%s", out)
	}
	// Both boards are called "Tasks". Without the headings above them the answer
	// contains two identical lines and no way to choose.
	if strings.Count(out, "workspace_id:") < 2 {
		t.Errorf("the tree gives no workspace id to write back:\n%s", out)
	}
}

// Search has the same duty as the tree: two pages of the same name in two
// workspaces came back as two identical bullets, and the choice between them
// was left to an agent with nothing to choose on.
func TestSearchResultsSayWhichWorkspaceTheyAreIn(t *testing.T) {
	s := testServer(t)
	uid, _, stockbook, dtrade, _, _ := twoLookalikes(t, s)
	indexedPage(t, s, stockbook, uid, "sb1", "Tasks", "Reconcile the quarterly ledger.")
	indexedPage(t, s, dtrade, uid, "dt1", "Tasks", "Reconcile the quarterly ledger.")

	out, err := s.mcpSearch(s.userByID(uid), "ledger")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "[Stockbook]") || !strings.Contains(out, "[Dtrade]") {
		t.Errorf("the hits do not say which workspace they are in:\n%s", out)
	}
}

// And with one workspace the label is left off, for the same reason the tree
// drops its heading: it would name the only place a result could come from.
func TestSearchStaysPlainWithOneWorkspace(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	indexedPage(t, s, ws, uid, "p1", "Tasks", "Reconcile the quarterly ledger.")

	out, err := s.mcpSearch(s.userByID(uid), "ledger")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if strings.Contains(out, "[") {
		t.Errorf("a single-workspace hit carries a label it does not need:\n%s", out)
	}
}

// One workspace gets no heading: it would name the only place anything could be.
func TestTheePageTreeStaysPlainWithOneWorkspace(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	if _, err := s.db.Exec(`INSERT INTO pages (id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility)
		VALUES ('p1', 'Solo', '[]', 0, ?, ?, ?, ?, 'workspace')`, now(), now(), ws, uid); err != nil {
		t.Fatalf("insert page: %v", err)
	}
	out, err := s.mcpListPages(s.userByID(uid))
	if err != nil {
		t.Fatalf("list pages: %v", err)
	}
	if strings.Contains(out, "workspace_id:") {
		t.Errorf("a single-workspace tree carries a heading it does not need:\n%s", out)
	}
	if !strings.Contains(out, "Solo") {
		t.Errorf("the page is missing:\n%s", out)
	}
}
