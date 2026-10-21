package server

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The MCP surface of the skill control plane.
//
// Four read tools, and the shape of them is the security design (a fifth,
// skill_push, writes — see below):
//
//	skill_catalog        metadata only — no instruction body can come out of it
//	skill_resolve        which exact version applies here, and why
//	skill_get            ONE exact approved Remote version, trusted-framed
//	skill_client_package ONE exact approved Client version, as a package
//
// What is deliberately absent is the tool somebody would otherwise ask for: a
// way to hand in a page id and get its contents back as instructions. That
// tool is the vulnerability. `search` and `get_page` continue to return page
// bodies fenced as UNTRUSTED, and there is no bridge from there to here — the
// only way text reaches skill_get is as a normalized skill_versions row that a
// workspace admin approved in a browser session.
//
// Nothing in this file re-implements a rule. Permission, lifecycle, scope and
// dependency logic all live in the service and the resolver, which the HTTP
// handlers call too. An agent and a person are therefore held to the same rules
// by the same code, which is the only way to keep them from drifting apart.

// mcpSkillCatalog answers "what is there", metadata only.
//
// Progressive disclosure starts here: an agent gets names, descriptions,
// triggers, scope and dependency summaries for the whole approved library —
// enough to decide what to ask for next — and not one instruction body. For a
// library of a hundred skills that is the difference between a usable context
// and a full one.
func (s *Server) mcpSkillCatalog(u *user, workspaceID, project, repository, role, taskType, agent string, capabilities []string) (string, error) {
	workspaces := s.skillWorkspacesFor(u)
	if workspaceID != "" {
		filtered := []string{}
		for _, w := range workspaces {
			if w == workspaceID {
				filtered = append(filtered, w)
			}
		}
		workspaces = filtered
		if len(workspaces) == 0 {
			return "", fmt.Errorf("workspace %q not found", workspaceID)
		}
	}
	skills, versions, err := s.publishedSkillVersions(workspaces)
	if err != nil {
		return "", err
	}
	req := resolveRequest{
		WorkspaceID: workspaceID, Project: project, Repository: repository, Role: role,
		TaskType: taskType, Agent: agent, Capabilities: normalizeStrings(capabilities),
	}
	entries := make([]catalogEntry, 0, len(versions))
	for i := range versions {
		// `scope_match` is advisory here, not a filter. A catalogue that hid
		// everything out of scope would leave an agent unable to see that a
		// skill exists for a context it could switch into — and the resolver is
		// where scope actually decides anything.
		_, _, match := scopeMatches(versions[i].Scope, req)
		entries = append(entries, catalogEntryFor(skills[i], versions[i], match))
	}
	if len(entries) == 0 {
		return "No approved skills in reach. Nothing has been published, or your credential " +
			"cannot see the workspace that holds them.", nil
	}
	return skillJSON(map[string]any{
		"skills": entries,
		"note": "Metadata only. Call skill_resolve with the task to find out which of these applies, " +
			"then skill_get for the exact approved instructions of the one that was selected.",
	}), nil
}

// mcpSkillResolve is the same resolver the Resolver test screen calls.
func (s *Server) mcpSkillResolve(u *user, req resolveRequest) (string, error) {
	res, err := s.resolveSkills(u, req)
	if err != nil {
		return "", err
	}
	workspace := req.WorkspaceID
	if workspace == "" {
		workspace = s.defaultWorkspaceFor(u)
	}
	s.recordResolution(u, workspace, req, res, skillActorType(u))

	// Missing dependencies are lifted to the top level as well as staying on
	// each selection: an agent that reads only the summary must not be able to
	// miss the reason a skill it expected is absent (§21, FM-2).
	missing := []missingDependency{}
	for _, sel := range res.Selected {
		missing = append(missing, sel.Missing...)
	}
	for _, ex := range res.Excluded {
		missing = append(missing, ex.Missing...)
	}
	out := map[string]any{
		"selected":             res.Selected,
		"excluded":             res.Excluded,
		"considered":           res.Considered,
		"missing_dependencies": missing,
	}
	switch {
	case len(res.Selected) == 0 && res.Considered == 0:
		out["note"] = "No approved skill is published in reach of this credential."
	case len(res.Selected) == 0:
		out["note"] = "No approved skill matched this context. `excluded` says which filter each " +
			"candidate failed."
	default:
		out["note"] = "Selection pins an EXACT version. Call skill_get with the skill_id and that " +
			"version number; do not assume the newest. Remote skills return instructions, client " +
			"skills return a package via skill_client_package."
	}
	return skillJSON(out), nil
}

// mcpSkillGet delivers one exact approved Remote version inside the trusted
// envelope.
//
// The version argument is REQUIRED and there is no "latest" spelling. That is
// deliberate: an agent resolved a specific version, the audit trail records
// that version, and a call meaning "whatever is current now" would break the
// one guarantee the immutable-version model exists to provide — that what ran
// can be reconstructed exactly.
func (s *Server) mcpSkillGet(u *user, skillID, slug, version string) (string, error) {
	if strings.TrimSpace(skillID) == "" && strings.TrimSpace(slug) == "" {
		return "", fmt.Errorf("skill_id (or slug) is required")
	}
	if strings.TrimSpace(version) == "" {
		return "", fmt.Errorf("version is required — resolve first, then fetch the exact version it selected")
	}
	if skillID == "" {
		// A slug is workspace-scoped, so it is only usable once the workspace
		// is known. Resolved through the caller's own workspaces, which keeps
		// the lookup inside the permission boundary.
		found := false
		for _, w := range s.skillWorkspacesFor(u) {
			sk, err := s.skillBySlug(w, slugify(slug))
			if err == nil {
				skillID, found = sk.ID, true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("no skill with slug %q in reach", slug)
		}
	}
	sk, v, err := s.approvedRemoteVersion(u, skillID, version)
	if err != nil {
		return "", err
	}
	// Recorded BEFORE the text is handed over, and a failure refuses the call.
	// This is the moment content crosses onto the trusted plane; an untraceable
	// crossing is not allowed to succeed. (Contrast `resolved`, which is
	// best-effort — see recordSkillAudit.)
	if err := s.recordSkillAudit(skillAuditEntry{
		WorkspaceID: sk.WorkspaceID, SkillID: sk.ID, SkillVersionID: v.ID, VersionNumber: v.Version,
		DeliveryMode: v.DeliveryMode, Action: skillActionFetched, ContentHash: v.ContentHash,
		ActorUserID: u.ID, ActorName: u.Name, ActorType: skillActorType(u),
	}); err != nil {
		return "", fmt.Errorf("the fetch could not be recorded in the audit trail, so the skill was not delivered — try again")
	}
	return trustedRemoteEnvelope(sk, v), nil
}

// mcpSkillClientPackage describes the package for an exact approved Client
// version: manifest, file list, archive hash, and the URL to download it from.
//
// The bytes travel over HTTP rather than through MCP. Two reasons: a base64
// archive in a tool result is megabytes of an agent's context spent on a file
// it will write to disk unread, and the download is the natural place for the
// same credential check plus the audit row that says a package left the
// workspace.
func (s *Server) mcpSkillClientPackage(u *user, skillID, slug, version, publicBase string) (string, error) {
	if strings.TrimSpace(skillID) == "" && strings.TrimSpace(slug) == "" {
		return "", fmt.Errorf("skill_id (or slug) is required")
	}
	if strings.TrimSpace(version) == "" {
		return "", fmt.Errorf("version is required — resolve first, then ask for the exact version it selected")
	}
	if skillID == "" {
		found := false
		for _, w := range s.skillWorkspacesFor(u) {
			sk, err := s.skillBySlug(w, slugify(slug))
			if err == nil {
				skillID, found = sk.ID, true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("no skill with slug %q in reach", slug)
		}
	}
	sk, v, err := s.approvedClientVersion(u, skillID, version)
	if err != nil {
		return "", err
	}
	info, err := s.skillPackageInfo(sk, v, publicBase)
	if err != nil {
		return "", err
	}
	return skillJSON(map[string]any{
		"package": info,
		"note": "Deterministic for this immutable version: the same version always produces the same " +
			"bytes, so archiveHash can be compared against what you installed. Download it with your " +
			"own credential. Nothing is installed for you — ask the person you are working with " +
			"before writing files into their environment.",
	}), nil
}

// ---- skill_push ----

// skillPushInput is one skill as somebody holds it locally.
type skillPushInput struct {
	WorkspaceID string
	Slug        string
	Name        string
	Description string
	SkillMD     string
	Submit      bool
	Draft       skillDraftInput
}

// mcpSkillPush is the way a skill leaves somebody's machine and reaches the
// workspace — and it is a one-way door into the DRAFT room only.
//
// The security design above says no text reaches an agent as instructions
// unless a workspace admin approved it in a browser. That does not change: this
// tool can create a skill, write or replace the open draft, or start a new
// draft version of a published skill, and can optionally put the draft in the
// review queue. It cannot approve, deprecate, or touch a version that is
// pending (frozen so a reviewer sees what they approve) or approved
// (immutable). A pushed skill is therefore inert until a person has read it —
// which is also why it is safe to let any member's agent call it.
//
// It is built for repetition. A sync tool will push every local skill on every
// run, so pushing content the workspace already holds changes nothing and says
// so, rather than minting a new version per run.
//
// Every rule — who may author, what a valid draft is, what is frozen — is the
// service's own, the same code the browser reaches. Nothing is re-implemented.
func (s *Server) mcpSkillPush(u *user, in skillPushInput) (string, error) {
	slug := strings.TrimSpace(in.Slug)
	name := strings.TrimSpace(in.Name)
	desc := strings.TrimSpace(in.Description)
	d := in.Draft

	if strings.TrimSpace(in.SkillMD) != "" {
		md := parseSkillMarkdown(in.SkillMD)
		if slug == "" {
			slug = md.Name
		}
		if name == "" {
			name = md.Name
		}
		if desc == "" {
			desc = md.Description
		}
		if d.Instructions == nil {
			body := md.Body
			d.Instructions = &body
		}
	}
	if slug == "" {
		slug = name
	}
	slug = slugify(slug)
	if slug == "" {
		return "", fmt.Errorf("a slug is required: pass slug, or a skill_md whose front matter has a name")
	}
	if name == "" {
		name = slug
	}
	if desc != "" {
		d.Description = &desc
	}

	ws, err := s.mcpPlacementWorkspace(u, in.WorkspaceID, "", "")
	if err != nil {
		return "", err
	}

	sk, err := s.skillBySlug(ws, slug)
	if errors.Is(err, sql.ErrNoRows) {
		// A skill on somebody's machine is something they installed, which is a
		// Client skill; a Remote one is a deliberate choice.
		if d.DeliveryMode == nil {
			client := skillDeliveryClient
			d.DeliveryMode = &client
		}
		d.Name, d.Slug = &name, &slug
		sk, v, err := s.createSkill(u, ws, d)
		if err != nil {
			return "", err
		}
		return s.finishSkillPush(u, sk, v, "created", in.Submit)
	}
	if err != nil {
		return "", err
	}
	if !s.skillCanAuthor(u, sk.WorkspaceID) {
		return "", errSkillForbidden
	}

	versions, err := s.skillVersions(sk.ID) // newest first
	if err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("skill %q has no versions", slug)
	}
	latest := versions[0]
	if latest.Status == skillStatusPending {
		return "", coded("skill_pending_exists", fmt.Sprintf(
			"version %d of %q is waiting for review and is frozen, so it cannot be changed. "+
				"A workspace admin can approve it or ask for changes at /skills/review; push again after that.",
			latest.Version, slug))
	}

	if name != "" && name != sk.Name && in.Name != "" {
		if sk, err = s.updateSkillMeta(u, sk, &name, nil, nil); err != nil {
			return "", err
		}
	}

	// Would this push change anything? The hash covers the version number, so
	// the candidate is hashed AS the version it would replace or follow.
	candidate := d.applyTo(latest, sk.WorkspaceID)
	if err := validateVersionDraft(candidate); err != nil {
		return "", err
	}
	if skillContentHash(sk.Slug, candidate) == latest.ContentHash {
		if latest.Status == skillStatusDraft && in.Submit {
			return s.finishSkillPush(u, sk, latest, "unchanged", true)
		}
		return s.finishSkillPush(u, sk, latest, "unchanged", false)
	}

	status := "updated"
	target := latest
	if latest.Status != skillStatusDraft {
		// Approved or deprecated: immutable, so the change is a new version.
		if target, err = s.newDraftVersion(u, sk, latest); err != nil {
			return "", err
		}
		status = "new_version"
	}
	updated, err := s.updateDraftVersion(u, sk, target, d, "")
	if err != nil {
		return "", err
	}
	return s.finishSkillPush(u, sk, updated, status, in.Submit)
}

// finishSkillPush optionally submits the draft, then reports what happened and
// what has NOT: the answer an agent relays to the person is "waiting for a
// reviewer", and it should not have to work that out.
func (s *Server) finishSkillPush(u *user, sk skill, v skillVersion, result string, submit bool) (string, error) {
	if submit && v.Status == skillStatusDraft {
		pending, err := s.submitForReview(u, sk, v)
		if err != nil {
			return "", fmt.Errorf("the draft was saved as version %d but could not be submitted: %w", v.Version, err)
		}
		v = pending
	}
	next := "It is a draft: finish it and submit it for review in the browser at /skills/" + sk.ID + "/edit/" + v.ID + "."
	switch {
	case v.Status == skillStatusPending:
		next = "It is waiting in the review queue (/skills/review). Nothing is executable until a workspace admin approves it."
	case v.Status == skillStatusApproved && result == "unchanged":
		next = "The workspace already holds exactly this content as the approved version; nothing was pushed."
	case v.Status == skillStatusDeprecated && result == "unchanged":
		next = "The workspace already holds exactly this content (version deprecated); nothing was pushed."
	}
	return skillJSON(map[string]any{
		"result":         result,
		"skill_id":       sk.ID,
		"slug":           sk.Slug,
		"name":           sk.Name,
		"version":        v.Version,
		"version_status": v.Status,
		"delivery_mode":  v.DeliveryMode,
		"content_hash":   v.ContentHash,
		"next":           next,
	}), nil
}

// skillMarkdown is a SKILL.md taken apart.
type skillMarkdown struct {
	Name, Description, Body string
}

// parseSkillMarkdown reads the front matter Claude Code and compatible runtimes
// use (name, description) and returns the rest as the body.
//
// A SKILL.md that THIS server rendered (it carries content_hash) has a title,
// the description repeated and a dependencies section added around the author's
// instructions. Pulling a package and pushing it back would add those again on
// every round trip, so for those the added parts are taken off first.
func parseSkillMarkdown(src string) skillMarkdown {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	out := skillMarkdown{Body: src}
	if !strings.HasPrefix(src, "---\n") {
		return out
	}
	rest := src[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return out
	}
	front, body := rest[:end], rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")
	generated := false
	lines := strings.Split(front, "\n")
	for i := 0; i < len(lines); i++ {
		key, val, ok := strings.Cut(lines[i], ":")
		if !ok || strings.HasPrefix(lines[i], " ") || strings.HasPrefix(lines[i], "\t") {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if key == "content_hash" {
			generated = true
		}
		// A folded or literal block (">", "|") or an empty value continues on the
		// indented lines below it.
		if val == "" || val == ">" || val == "|" || val == ">-" || val == "|-" {
			var parts []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t")) {
				i++
				parts = append(parts, strings.TrimSpace(lines[i]))
			}
			val = strings.Join(parts, " ")
		}
		val = strings.Trim(val, `"'`)
		switch key {
		case "name":
			out.Name = val
		case "description":
			out.Description = val
		}
	}
	body = strings.TrimLeft(body, "\n")
	if generated {
		body = stripRenderedSkillMarkdown(body, out.Description)
	}
	out.Body = body
	return out
}

// stripRenderedSkillMarkdown undoes clientSkillMarkdown's additions.
func stripRenderedSkillMarkdown(body, description string) string {
	if strings.HasPrefix(body, "# ") {
		if _, after, ok := strings.Cut(body, "\n"); ok {
			body = strings.TrimLeft(after, "\n")
		}
	}
	if description != "" && strings.HasPrefix(body, description+"\n") {
		body = strings.TrimLeft(strings.TrimPrefix(body, description+"\n"), "\n")
	}
	if i := strings.Index(body, "\n## What this skill needs\n"); i >= 0 {
		body = body[:i]
	}
	return strings.TrimRight(body, "\n") + "\n"
}
