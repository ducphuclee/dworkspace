package server

import (
	"fmt"
	"strings"
)

// Saying where you are writing, and being held to it.
//
// The failure this exists for, observed repeatedly on an instance whose people
// belong to several workspaces: an agent is told to log a task in Stockbook,
// works out the right board, and then — at the moment of writing — notices a
// workspace called Dtrade whose boards are named almost identically, talks
// itself into that one, writes there, notices, deletes, and writes again in the
// right place. The intent was formed once and re-derived at write time, and the
// second derivation had nothing to anchor it.
//
// The fix is NOT to give the agent more information. It already had enough; it
// reasoned its way past it. The fix is to make the choice checkable.
//
// So a write that PLACES content somewhere must state the workspace it means,
// even when the server could work it out on its own — and that is the whole
// point. The server derives the workspace from the object being written to, the
// agent declares the workspace it believes it is writing to, and the two are
// compared. In the incident above they would have disagreed: the agent said
// Stockbook and picked Dtrade's board. Refusing there turns a silent wrong
// write into an error naming both sides, at the moment the agent was confusing
// itself rather than after.
//
// Two rules keep this from becoming noise:
//
//   - Nobody with ONE workspace is ever asked. There is nothing to confuse and
//     no checksum to run, so the requirement simply does not apply to them. The
//     people who see it are exactly the people who have the problem.
//
//   - The refusal NEVER names the workspace the object is in. That was the
//     first version and it defeats the mechanism: an error reading "it is in
//     Dtrade, pass that" hands the agent the answer, and what it echoes back is
//     no longer an independent statement of intent. It lists what the agent can
//     reach and makes it choose.
//
// What this does NOT catch: an agent that is wrong CONSISTENTLY — declares
// Dtrade and picks Dtrade's board, when the user meant Stockbook. Both sides
// agree and the write lands in the wrong place. Nothing here can see that; what
// prevents it is the workspace name beside every entry in list and search, so
// the wrong board is not picked in the first place.

// workspaceRef is a workspace as an error message needs to name it.
type workspaceRef struct {
	ID   string
	Name string
}

// mcpWritableWorkspaces lists the workspaces this credential may actually write
// into: membership, the token's own workspace list, and the workspace's agent
// policy all apply. Viewers are left out — naming a workspace an agent cannot
// write to as a choice would only produce a second, more confusing refusal.
func (s *Server) mcpWritableWorkspaces(u *user) []workspaceRef {
	rows, err := s.db.Query(`SELECT w.id, w.name, m.role FROM workspaces w
		JOIN workspace_members m ON m.workspace_id = w.id
		WHERE m.user_id = ? ORDER BY w.name`, u.ID)
	if err != nil {
		return nil
	}
	type row struct{ id, name, role string }
	var all []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.name, &r.role) == nil {
			all = append(all, r)
		}
	}
	rows.Close() // drain before credentialMayEnter, which queries (single connection)

	var out []workspaceRef
	for _, r := range all {
		if r.role == "viewer" || !s.credentialMayEnter(u, r.id) {
			continue
		}
		out = append(out, workspaceRef{ID: r.id, Name: r.name})
	}
	return out
}

// workspaceName is for error messages only. A missing name falls back to the
// id rather than to nothing: an error that says `workspace ""` helps no one.
func (s *Server) workspaceName(id string) string {
	var name string
	if s.db.QueryRow(`SELECT name FROM workspaces WHERE id = ?`, id).Scan(&name) != nil || name == "" {
		return id
	}
	return name
}

// listChoices renders the workspaces an agent may pick from.
func listChoices(ws []workspaceRef) string {
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		parts = append(parts, fmt.Sprintf("%q (id: %s)", w.Name, w.ID))
	}
	return strings.Join(parts, ", ")
}

// mcpConfirmPageWorkspace is mcpPlacementWorkspace for the writes whose
// destination is an existing page: the database being appended to, the page
// being rewritten or copied. The workspace is never in doubt — the page knows
// it — so the only job here is holding the agent to having said the same thing.
//
// A page that does not exist passes through untouched. The tool behind this is
// about to produce its own "page not found", which is the more useful error;
// answering "workspace_id is required" for an id that is wrong anyway would
// send the agent looking in the wrong direction.
func (s *Server) mcpConfirmPageWorkspace(u *user, declared, pageID, what string) error {
	ws := s.pageWorkspace(pageID)
	if ws == "" {
		return nil
	}
	_, err := s.mcpPlacementWorkspace(u, declared, ws, what)
	return err
}

// mcpPlacementWorkspace resolves the workspace a write lands in, and holds the
// agent to having meant it.
//
// derived is the workspace the target object already lives in — the parent
// page, the database being appended to, the page being rewritten — or "" when
// the call names no object and the declared workspace IS the destination (a
// top-level create).
//
// what names that object in an error, in the agent's own terms ("the parent
// page", "that database"), so a refusal says which of the two ids to re-check.
func (s *Server) mcpPlacementWorkspace(u *user, declared, derived, what string) (string, error) {
	if declared == "" {
		// Derivable or not, with one workspace there is only one answer.
		choices := s.mcpWritableWorkspaces(u)
		if len(choices) == 1 {
			if derived != "" {
				return derived, nil
			}
			return choices[0].ID, nil
		}
		if len(choices) == 0 {
			return "", fmt.Errorf("you have no workspace you can write to")
		}
		// Deliberately does not say where the object is — see the file comment.
		return "", fmt.Errorf(
			"workspace_id is required: you can write to %d workspaces, and which one you mean "+
				"cannot be guessed from the call. Say which: %s",
			len(choices), listChoices(choices))
	}

	// A declared workspace is checked as a workspace first, so "that is not a
	// workspace you can write to" never comes back dressed as a mismatch.
	//
	// The choices come along because a bare "not found" is a dead end, and an
	// agent at a dead end holding a workspace NAME it was given reaches for the
	// one move that cannot fail: creating a workspace by that name. Listing what
	// exists turns the dead end back into a lookup. (mcpCreateWorkspace refuses
	// the duplicate as well, but by then the agent has already gone the wrong
	// way — this is the earlier of the two places to stop it.)
	if !s.isMember(u.ID, declared) || !s.credentialMayEnter(u, declared) {
		if choices := s.mcpWritableWorkspaces(u); len(choices) > 0 {
			return "", fmt.Errorf(
				"workspace %q not found. You can write to: %s — one of these is the one you mean, "+
					"so pick it rather than creating anything", declared, listChoices(choices))
		}
		return "", fmt.Errorf("workspace %q not found", declared)
	}
	if s.workspaceRole(u.ID, declared) == "viewer" {
		return "", fmt.Errorf("you are a viewer in %q and cannot write there", s.workspaceName(declared))
	}
	if derived == "" {
		return declared, nil
	}
	if derived != declared {
		// Both sides named, because the agent got one of them wrong and cannot
		// tell which without seeing them together.
		return "", fmt.Errorf(
			"workspace_id says %q but %s is in %q. One of the two is wrong — "+
				"check which one you actually mean before writing",
			s.workspaceName(declared), what, s.workspaceName(derived))
	}
	return derived, nil
}
