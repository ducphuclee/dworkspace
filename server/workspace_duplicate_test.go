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
// the right one. Creating a workspace called "Stockbook" was the one that could
// not fail: it returned a real id, and the write after it succeeded. The live
// instance grew a second, empty "Stockbook" exactly that way, with nothing in
// the trace looking like an error.
//
// The answer is not a better warning in the tool description. An agent reaching
// for a tool because it is stuck is not in a state to be talked out of it. The
// tool is gone: creating a workspace is a decision about how a team is
// organised, it happens perhaps twice a year, and it belongs to the person in
// the browser where they can see what already exists.
//
// What remains over MCP is the lookup, so being stuck has an exit that is not
// creation — which is what the second test here is about.

// The tool is not merely refused, it is not offered. A refusal still invites a
// retry with different arguments; an absent tool ends the line of thought.
func TestNoToolCreatesAWorkspace(t *testing.T) {
	s := testServer(t)
	for _, tool := range mcpTools {
		name, _ := tool["name"].(string)
		if name == "workspace" {
			t.Fatal("the workspace tool is back: an agent can create workspaces again")
		}
	}

	// And the dispatch does not answer it either, in case a client has the old
	// schema cached and calls it anyway.
	uid, _ := signedIn(t, s, "a@example.com")
	out, err := s.mcpCall(s.userByID(uid), "workspace", []byte(`{"name":"Stockbook"}`), "")
	if err == nil {
		t.Fatalf("a cached workspace call was served: %s", out)
	}

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE name = 'Stockbook'`).Scan(&n)
	if n != 0 {
		t.Errorf("the call created %d workspaces on its way to failing", n)
	}
}

// Taking the tool away only works if the agent has somewhere else to go. A
// workspace_id that does not resolve used to dead-end on "not found", which
// reads as "this workspace does not exist" — the exact thought that led to
// creating one. It now lists what can be written to instead.
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

// whoami is where an agent looks first when a write fails, so it has to say
// that creating a workspace is not on the table — otherwise the agent works
// that out by trying, and trying is how this started.
func TestWhoamiSaysWorkspacesAreNotCreatedHere(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")

	out, err := s.mcpWhoami(s.userByID(uid))
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if !strings.Contains(out, "creating, renaming or deleting a workspace") {
		t.Errorf("whoami does not list workspace creation as unavailable:\n%s", out)
	}
	// And points at the thing to do instead.
	if !strings.Contains(out, "kind=\\\"workspaces\\\"") {
		t.Errorf("whoami does not say where the id it is missing comes from:\n%s", out)
	}
}

// The browser keeps both paths. Creating a workspace was never the problem —
// an agent creating one was.
func TestTheBrowserCanStillCreateAWorkspace(t *testing.T) {
	s := testServer(t)
	_, cookie := signedIn(t, s, "a@example.com")

	rec := requestAs(t, s, cookie, "POST", "/api/workspaces", `{"name":"Stockbook"}`)
	if rec.Code != 200 {
		t.Fatalf("the browser was refused: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Stockbook") {
		t.Errorf("unexpected answer: %s", rec.Body.String())
	}
}
