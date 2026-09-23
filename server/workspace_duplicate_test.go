package server

import (
	"strings"
	"testing"
)

// The second incident, and a direct consequence of the first fix.
//
// Requiring workspace_id on writes closed the silent-wrong-write hole and
// opened a louder one. An agent told to log something in Stockbook, holding no
// id, now gets refused — and from there it has two moves. Looking the id up is
// the right one. Creating a workspace called "Stockbook" is the one that CANNOT
// FAIL: it returns a real id, and the write that follows it succeeds.
//
// The live instance grew a second, empty "Stockbook" exactly that way. Nothing
// in the trace looked like an error; every call returned success.
//
// These tests cover the three places an agent can take that turn: the refusal
// that sends it looking (it must list what exists), the wrong-id dead end (same),
// and the create itself (it must refuse the duplicate and hand over the id the
// agent was actually missing).

// The whole incident, as one call: the agent reaches for the name it was given.
func TestCreatingAWorkspaceThatAlreadyExistsIsRefused(t *testing.T) {
	s := testServer(t)
	uid, _, stockbook, _, _, _ := twoLookalikes(t, s)

	out, err := s.mcpCreateWorkspace(uid, "Stockbook")
	if err == nil {
		t.Fatalf("a second workspace called Stockbook was created: %s", out)
	}
	// The id is the thing the agent came here without. Withholding it leaves it
	// exactly where it was, which is how it ended up here.
	if !strings.Contains(err.Error(), stockbook) {
		t.Errorf("the refusal does not give the id of the workspace that already exists: %v", err)
	}
	if !strings.Contains(err.Error(), "Stockbook") {
		t.Errorf("the refusal does not name it: %v", err)
	}

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE name = 'Stockbook'`).Scan(&n)
	if n != 1 {
		t.Errorf("there are now %d workspaces called Stockbook", n)
	}
}

// Case and stray spacing are not a distinction anybody can see in a sidebar, so
// they must not be enough to get a duplicate past the guard.
func TestNearlyTheSameNameIsStillADuplicate(t *testing.T) {
	s := testServer(t)
	uid, _, _, _, _, _ := twoLookalikes(t, s)

	for _, name := range []string{"stockbook", "STOCKBOOK", "  Stockbook  "} {
		if _, err := s.mcpCreateWorkspace(uid, name); err == nil {
			t.Errorf("%q was accepted as a new workspace alongside Stockbook", name)
		}
	}
}

// And a genuinely new name still goes through: the guard is against collisions,
// not against creating workspaces.
func TestADistinctNameStillCreates(t *testing.T) {
	s := testServer(t)
	uid, _, _, _, _, _ := twoLookalikes(t, s)

	out, err := s.mcpCreateWorkspace(uid, "Stockbook Archive")
	if err != nil {
		t.Fatalf("a workspace with an unused name was refused: %v", err)
	}
	if !strings.Contains(out, "Created workspace") {
		t.Errorf("unexpected answer: %s", out)
	}
}

// Somebody else's workspace of the same name is not this person's problem: the
// guard looks at what THEY are in, not at the whole instance.
func TestAnotherPersonsWorkspaceOfTheSameNameDoesNotBlockYou(t *testing.T) {
	s := testServer(t)
	other, _ := signedIn(t, s, "other@example.com")
	ws := makeWorkspace(t, s, other)
	if _, err := s.db.Exec(`UPDATE workspaces SET name = 'Stockbook' WHERE id = ?`, ws); err != nil {
		t.Fatalf("name it: %v", err)
	}

	uid, _ := signedIn(t, s, "a@example.com")
	if _, err := s.mcpCreateWorkspace(uid, "Stockbook"); err != nil {
		t.Errorf("a name used only by somebody else was refused: %v", err)
	}
}

// The earlier of the two places to stop this. An agent that guesses an id — or
// carries a stale one — used to get a bare "not found", which reads as "this
// workspace does not exist" and invites making it.
func TestAWrongWorkspaceIdIsAnsweredWithTheOnesThatExist(t *testing.T) {
	s := testServer(t)
	uid, _, _, _, _, dtBoard := twoLookalikes(t, s)
	u := s.userByID(uid)

	err := s.mcpConfirmPageWorkspace(u, "not-a-real-workspace-id", dtBoard, "that database")
	if err == nil {
		t.Fatal("a write naming a workspace that does not exist went through")
	}
	if !strings.Contains(err.Error(), "Stockbook") || !strings.Contains(err.Error(), "Dtrade") {
		t.Errorf("the refusal leaves the agent with no id to use instead: %v", err)
	}
}

// The blueprint path creates a workspace too, and goes through the same guard —
// worth pinning, because it is the one that would otherwise be forgotten.
func TestABlueprintCannotDuplicateANameEither(t *testing.T) {
	s := testServer(t)
	uid, _, stockbook, _, _, _ := twoLookalikes(t, s)
	u := s.userByID(uid)

	if _, err := s.blueprintWorkspace(u, "Stockbook", stockbook); err == nil {
		t.Error("a blueprint made a second workspace called Stockbook")
	}
}
