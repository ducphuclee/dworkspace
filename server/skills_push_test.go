package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// skill_push: a skill leaves somebody's machine and lands in the DRAFT room,
// and only there. The tests are about what it must not be able to do as much as
// what it does.

type pushResult struct {
	Result        string `json:"result"`
	SkillID       string `json:"skill_id"`
	Slug          string `json:"slug"`
	Version       int    `json:"version"`
	VersionStatus string `json:"version_status"`
	DeliveryMode  string `json:"delivery_mode"`
	ContentHash   string `json:"content_hash"`
}

func pushSkill(t *testing.T, f *skillFixture, u *user, args string) pushResult {
	t.Helper()
	out, err := mcpSkillCall(t, f, u, "skill_push", args)
	if err != nil {
		t.Fatalf("skill_push %s: %v", args, err)
	}
	var r pushResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("skill_push answered %q: %v", out, err)
	}
	return r
}

const pushMD = "---\nname: key-rotation\ndescription: Rotate the signing key without downtime.\n---\n\n# Key rotation\n\nStep one: announce it.\n"

func TestPushCreatesAClientDraftThatIsNotExecutable(t *testing.T) {
	f := newSkillFixture(t)
	agent := f.agentUser(t, f.memberID)
	r := pushSkill(t, f, agent, `{"workspace_id":"`+f.ws+`","skill_md":`+jsonString(pushMD)+`}`)

	if r.Result != "created" || r.Slug != "key-rotation" || r.Version != 1 ||
		r.VersionStatus != skillStatusDraft || r.DeliveryMode != skillDeliveryClient {
		t.Fatalf("unexpected first push: %+v", r)
	}
	sk, err := f.s.skillBySlug(f.ws, "key-rotation")
	if err != nil {
		t.Fatalf("the skill was not stored: %v", err)
	}
	vs, _ := f.s.skillVersions(sk.ID)
	if vs[0].Description != "Rotate the signing key without downtime." ||
		!strings.Contains(vs[0].Instructions, "Step one: announce it.") {
		t.Errorf("front matter / body not split: %+v", vs[0])
	}
	if sk.CurrentVersionID != "" {
		t.Error("a pushed skill has a published pointer — nobody approved it")
	}
	// Invariant 1: a draft is not in the catalogue and cannot be fetched.
	cat, _ := mcpSkillCall(t, f, agent, "skill_catalog", `{"workspace_id":"`+f.ws+`"}`)
	if strings.Contains(cat, "key-rotation") {
		t.Errorf("a draft shows in skill_catalog:\n%s", cat)
	}
	if _, err := mcpSkillCall(t, f, agent, "skill_get", `{"slug":"key-rotation","version":"1"}`); err == nil {
		t.Error("skill_get handed out a draft")
	}
	// The audit trail says an agent did it.
	var actor string
	f.s.db.QueryRow(`SELECT actor_type FROM skill_audit WHERE skill_id = ? AND action = ?`, sk.ID, skillActionCreated).Scan(&actor)
	if actor != "agent" {
		t.Errorf("created by an API token, recorded as %q", actor)
	}
}

func TestPushTwiceChangesNothingAndChangedContentUpdatesTheDraft(t *testing.T) {
	f := newSkillFixture(t)
	agent := f.agentUser(t, f.memberID)
	args := `{"workspace_id":"` + f.ws + `","skill_md":` + jsonString(pushMD) + `}`
	first := pushSkill(t, f, agent, args)
	again := pushSkill(t, f, agent, args)
	if again.Result != "unchanged" || again.Version != 1 || again.ContentHash != first.ContentHash {
		t.Fatalf("pushing the same content again: %+v", again)
	}
	changed := pushSkill(t, f, agent, `{"workspace_id":"`+f.ws+`","skill_md":`+jsonString(strings.Replace(pushMD, "announce it", "announce it twice", 1))+`}`)
	if changed.Result != "updated" || changed.Version != 1 || changed.ContentHash == first.ContentHash {
		t.Fatalf("changed content should update the open draft in place: %+v", changed)
	}
}

func TestPushAgainstAPublishedSkillStartsANewDraftAndLeavesTheApprovedOneAlone(t *testing.T) {
	f := newSkillFixture(t)
	agent := f.agentUser(t, f.memberID)
	sk, v := f.makeSkill(t, f.memberID, "Key rotation", skillDeliveryClient, "Original approved text.", nil)
	f.publish(t, sk, v)

	r := pushSkill(t, f, agent, `{"workspace_id":"`+f.ws+`","slug":"key-rotation","instructions":"Edited on a laptop."}`)
	if r.Result != "new_version" || r.Version != 2 || r.VersionStatus != skillStatusDraft {
		t.Fatalf("push over a published skill: %+v", r)
	}
	vs, _ := f.s.skillVersions(sk.ID)
	var one skillVersion
	for _, x := range vs {
		if x.Version == 1 {
			one = x
		}
	}
	if one.Status != skillStatusApproved || one.Instructions != "Original approved text." {
		t.Errorf("the approved version moved: %+v", one)
	}
	cur, _ := f.s.skillBySlug(f.ws, "key-rotation")
	if cur.CurrentVersionNumber != 1 {
		t.Errorf("the published pointer is v%d, want it to stay on v1", cur.CurrentVersionNumber)
	}
	// Pushing exactly what is approved is a no-op, not a version.
	same := pushSkill(t, f, agent, `{"workspace_id":"`+f.ws+`","slug":"key-rotation","instructions":"Edited on a laptop."}`)
	if same.Result != "unchanged" || same.Version != 2 {
		t.Errorf("repeat push: %+v", same)
	}
}

func TestPushSubmitQueuesItAndAPendingVersionIsFrozen(t *testing.T) {
	f := newSkillFixture(t)
	agent := f.agentUser(t, f.memberID)
	r := pushSkill(t, f, agent, `{"workspace_id":"`+f.ws+`","skill_md":`+jsonString(pushMD)+`,"submit":true}`)
	if r.VersionStatus != skillStatusPending {
		t.Fatalf("submit=true left it %q", r.VersionStatus)
	}
	_, err := mcpSkillCall(t, f, agent, "skill_push", `{"workspace_id":"`+f.ws+`","slug":"key-rotation","instructions":"sneak a change in after submitting"}`)
	if err == nil || !strings.Contains(err.Error(), "waiting for review") {
		t.Fatalf("a pending version accepted a change (err=%v)", err)
	}
	// Even an admin's agent cannot approve through this tool: it is browser-only.
	admin := f.agentUser(t, f.adminID)
	if _, err := mcpSkillCall(t, f, admin, "skill_push", `{"workspace_id":"`+f.ws+`","slug":"key-rotation","instructions":"x","submit":true}`); err == nil {
		t.Error("an admin's token changed a pending version")
	}
	sk, _ := f.s.skillBySlug(f.ws, "key-rotation")
	if sk.CurrentVersionID != "" {
		t.Error("a push published something")
	}
}

func TestPushRefusesReadOnlyTokensViewersAndOutsiders(t *testing.T) {
	f := newSkillFixture(t)
	readOnly := f.agentUser(t, f.memberID)
	readOnly.TokenScope = "read"
	if _, err := mcpSkillCall(t, f, readOnly, "skill_push", `{"workspace_id":"`+f.ws+`","skill_md":`+jsonString(pushMD)+`}`); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Errorf("a read-only token pushed (err=%v)", err)
	}
	f.s.db.Exec(`UPDATE workspace_members SET role = 'viewer' WHERE workspace_id = ? AND user_id = ?`, f.ws, f.memberID)
	if _, err := mcpSkillCall(t, f, f.agentUser(t, f.memberID), "skill_push", `{"workspace_id":"`+f.ws+`","skill_md":`+jsonString(pushMD)+`}`); err == nil {
		t.Error("a viewer pushed")
	}
	if _, err := mcpSkillCall(t, f, f.agentUser(t, f.outsiderID), "skill_push", `{"workspace_id":"`+f.ws+`","skill_md":`+jsonString(pushMD)+`}`); err == nil {
		t.Error("a non-member pushed")
	}
	if n := countSkills(f); n != 0 {
		t.Errorf("%d skills exist after refused pushes", n)
	}
}

// Pulling a package and pushing it back must not grow the skill each time.
func TestSkillMarkdownRoundTripsThroughAPackage(t *testing.T) {
	f := newSkillFixture(t)
	deps := []skillDependency{{Type: skillDepCapability, ID: "browser", Required: true, Fallback: "ask the user"}}
	sk, v := f.makeSkill(t, f.memberID, "UI review", skillDeliveryClient, "Open the page and compare.", func(in *skillDraftInput) {
		in.Dependencies = &deps
	})
	rendered := clientSkillMarkdown(sk, v)
	md := parseSkillMarkdown(rendered)
	if md.Name != sk.Slug || md.Description != v.Description {
		t.Errorf("front matter: %+v", md)
	}
	if strings.TrimSpace(md.Body) != "Open the page and compare." {
		t.Errorf("round trip body = %q", md.Body)
	}
}

func TestParseSkillMarkdownReadsFoldedAndQuotedValues(t *testing.T) {
	md := parseSkillMarkdown("---\nname: \"my-skill\"\ndescription: >\n  First part\n  second part\nallowed-tools: Bash\n---\nBody\n")
	if md.Name != "my-skill" || md.Description != "First part second part" || md.Body != "Body\n" {
		t.Errorf("%+v", md)
	}
	plain := parseSkillMarkdown("No front matter at all.\n")
	if plain.Name != "" || plain.Body != "No front matter at all.\n" {
		t.Errorf("%+v", plain)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func countSkills(f *skillFixture) int {
	var n int
	f.s.db.QueryRow(`SELECT COUNT(*) FROM skills`).Scan(&n)
	return n
}
