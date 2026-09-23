package server

import (
	"fmt"
	"strings"
	"testing"
)

// i18n-ok-file: the fixture headings are German on purpose. A heading path is
// addressed by the agent saying it back, and findSection matches with
// EqualFold — so the fixtures have to carry characters where case folding is
// not a byte operation ("Überblick" / "überblick"), and where the " › "
// separator sits between multi-byte text. An ASCII fixture would exercise the
// separator and the fold on the easy path only, which is the path that was
// never going to break.

// Reading without swallowing the page whole.
//
// get_page had no cap of any kind. Most pages are small — measured on a live
// instance of 173, the median renders to 1.3k characters — so nothing looked
// wrong. The tail is where it hurts: the longest page is 137k characters, about
// 34,000 tokens, and an agent that wanted one paragraph of it had to take all
// of it or nothing.
//
// The tests below pin the two halves of the fix: the outline is a real map of
// the page (not a list of headings with no sense of scale), and the
// abbreviation fires on its own, because a parameter an agent has to know about
// is a parameter it will not use.

// longPage builds a page with headings and enough text to cross the threshold.
func longPage(t *testing.T, s *Server, ws, uid, id string) {
	t.Helper()
	filler := strings.Repeat("Der Vertrag endet zum Quartalsende. ", 120) // ~4.3k each
	blocks := []string{
		`{"type":"paragraph","content":[{"type":"text","text":"Was dieses Dokument regelt."}]}`,
		`{"type":"heading","props":{"level":1},"content":[{"type":"text","text":"Vertrag"}]}`,
		`{"type":"heading","props":{"level":2},"content":[{"type":"text","text":"Kündigung"}]}`,
		fmt.Sprintf(`{"type":"paragraph","content":[{"type":"text","text":%q}]}`, filler),
		`{"type":"heading","props":{"level":2},"content":[{"type":"text","text":"Zahlung"}]}`,
		fmt.Sprintf(`{"type":"paragraph","content":[{"type":"text","text":%q}]}`, filler),
		`{"type":"heading","props":{"level":1},"content":[{"type":"text","text":"Anhang"}]}`,
		`{"type":"paragraph","content":[{"type":"text","text":"Nur eine Zeile."}]}`,
	}
	content := "[" + strings.Join(blocks, ",") + "]"
	if _, err := s.db.Exec(`INSERT INTO pages (id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility)
		VALUES (?, 'Vertragsunterlagen', ?, 0, ?, ?, ?, ?, 'workspace')`, id, content, now(), now(), ws, uid); err != nil {
		t.Fatalf("insert page: %v", err)
	}
}

func readPage(t *testing.T, s *Server, id string, outline bool, section string) string {
	t.Helper()
	p, err := s.getPage(id)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	out, err := pageReadout(p, outline, section)
	if err != nil {
		t.Fatalf("readout: %v", err)
	}
	return out
}

// The whole point: asking for nothing in particular no longer costs the page.
func TestALongPageComesBackAsAnOutline(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	longPage(t, s, ws, uid, "p1")

	out := readPage(t, s, "p1", false, "")
	if len(out) > outlineThreshold {
		t.Errorf("the abbreviated answer is %d chars, no better than the page", len(out))
	}
	for _, want := range []string{"Vertrag", "Kündigung", "Zahlung", "Anhang"} {
		if !strings.Contains(out, want) {
			t.Errorf("the outline is missing the %q section:\n%s", want, out)
		}
	}
	// The body of a section must NOT be there — that is the whole saving.
	if strings.Contains(out, "Quartalsende") {
		t.Errorf("the outline carries the section bodies it was supposed to replace:\n%s", out)
	}
	// The opening comes along: it is usually the only summary a page has, and
	// without it the agent spends a second call learning what the page is about.
	if !strings.Contains(out, "Was dieses Dokument regelt") {
		t.Errorf("the opening paragraph was dropped:\n%s", out)
	}
	// And it says how to go on, with a path that actually resolves.
	if !strings.Contains(out, "section") {
		t.Errorf("the outline does not say how to read a section:\n%s", out)
	}
}

// An outline of bare headings makes an agent guess which one is worth opening.
// The sizes are what turn it into a decision.
func TestTheOutlineSaysWhatEachSectionCosts(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	longPage(t, s, ws, uid, "p1")

	out := readPage(t, s, "p1", true, "")
	if !strings.Contains(out, "k chars") {
		t.Errorf("no section sizes, so there is nothing to choose on:\n%s", out)
	}
	// Anhang is one line; Kündigung is thousands. If both printed the same the
	// figures would be decoration.
	if strings.Count(out, "k chars") < 2 {
		t.Errorf("the big sections are not distinguished from the small ones:\n%s", out)
	}
}

// Reading one section gives the section, and nothing either side of it.
func TestASectionReadReturnsOnlyThatSection(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	longPage(t, s, ws, uid, "p1")

	out := readPage(t, s, "p1", false, "Vertrag › Kündigung")
	if !strings.Contains(out, "Kündigung") || !strings.Contains(out, "Quartalsende") {
		t.Errorf("the section body is missing:\n%s", out)
	}
	if strings.Contains(out, "Zahlung") || strings.Contains(out, "Anhang") {
		t.Errorf("the read spilled into the neighbouring sections:\n%s", out)
	}
}

// An agent that read a heading off a search hit should not have to reconstruct
// the path above it to open it.
func TestTheLastHeadingAloneIsEnoughWhenItIsUnambiguous(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	longPage(t, s, ws, uid, "p1")

	out := readPage(t, s, "p1", false, "Zahlung")
	if !strings.Contains(out, "Quartalsende") {
		t.Errorf("a unique heading was not accepted on its own:\n%s", out)
	}
}

// Two sections of the same name is exactly where guessing is worst.
func TestARepeatedHeadingIsRefusedRatherThanGuessed(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	content := `[{"type":"heading","props":{"level":1},"content":[{"type":"text","text":"A"}]},
		{"type":"heading","props":{"level":2},"content":[{"type":"text","text":"Überblick"}]},
		{"type":"paragraph","content":[{"type":"text","text":"eins"}]},
		{"type":"heading","props":{"level":1},"content":[{"type":"text","text":"B"}]},
		{"type":"heading","props":{"level":2},"content":[{"type":"text","text":"Überblick"}]},
		{"type":"paragraph","content":[{"type":"text","text":"zwei"}]}]`
	if _, err := s.db.Exec(`INSERT INTO pages (id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility)
		VALUES ('p1', 'Doppelt', ?, 0, ?, ?, ?, ?, 'workspace')`, content, now(), now(), ws, uid); err != nil {
		t.Fatalf("insert: %v", err)
	}
	p, _ := s.getPage("p1")
	_, err := pageReadout(p, false, "Überblick")
	if err == nil {
		t.Fatal("an ambiguous section name was resolved silently")
	}
	// Both candidates named, or the agent cannot choose.
	if !strings.Contains(err.Error(), "A › Überblick") || !strings.Contains(err.Error(), "B › Überblick") {
		t.Errorf("the refusal does not name the candidates: %v", err)
	}
}

// A short page is untouched. The agent reading an ordinary note should never
// find out this mechanism exists.
func TestAShortPageIsStillReturnedWhole(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	indexedPage(t, s, ws, uid, "p1", "Kurz", "Nur ein kurzer Absatz.")

	out := readPage(t, s, "p1", false, "")
	if !strings.Contains(out, "Nur ein kurzer Absatz") {
		t.Errorf("a short page was abbreviated:\n%s", out)
	}
	if strings.Contains(out, "outline") {
		t.Errorf("a short page got an outline it does not need:\n%s", out)
	}
}

// "*" is the way out for an agent that really does want all of it.
func TestAStarReadsTheWholeLongPage(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	longPage(t, s, ws, uid, "p1")

	out := readPage(t, s, "p1", false, "*")
	if len(out) < outlineThreshold {
		t.Errorf("section \"*\" did not return the whole page (%d chars)", len(out))
	}
	if !strings.Contains(out, "Quartalsende") {
		t.Errorf("the body is missing from a full read:\n%s", out)
	}
}

// A long page with no headings cannot be abbreviated into anything useful, and
// an outline of one line would be a worse answer than the page itself.
func TestALongPageWithoutHeadingsIsStillReturnedWhole(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	body := strings.Repeat("Ein Absatz ohne jede Überschrift. ", 400)
	indexedPage(t, s, ws, uid, "p1", "Fließtext", body)

	out := readPage(t, s, "p1", false, "")
	if !strings.Contains(out, "Ein Absatz ohne jede Überschrift") {
		t.Errorf("a headingless page was cut to nothing:\n%s", out)
	}
}

// Sections must survive as MARKDOWN, not as the plain text the search index
// keeps. This is the reason the outline is built from the block tree and not
// from page_chunks, which drops code fences, list markers and link targets.
func TestASectionKeepsItsFormatting(t *testing.T) {
	s := testServer(t)
	uid, _ := signedIn(t, s, "a@example.com")
	ws := soleWorkspace(t, s, uid)
	content := `[{"type":"heading","props":{"level":1},"content":[{"type":"text","text":"Einrichtung"}]},
		{"type":"codeBlock","props":{"language":"json"},"content":[{"type":"text","text":"{\"url\": \"https://example.test\"}"}]},
		{"type":"bulletListItem","content":[{"type":"text","text":"Erster Punkt"}]}]`
	if _, err := s.db.Exec(`INSERT INTO pages (id, title, content, position, created_at, updated_at, workspace_id, owner_id, visibility)
		VALUES ('p1', 'Setup', ?, 0, ?, ?, ?, ?, 'workspace')`, content, now(), now(), ws, uid); err != nil {
		t.Fatalf("insert: %v", err)
	}
	out := readPage(t, s, "p1", false, "Einrichtung")
	if !strings.Contains(out, "```json") {
		t.Errorf("the code fence was flattened away — the section is unusable for setup instructions:\n%s", out)
	}
	if !strings.Contains(out, "- Erster Punkt") {
		t.Errorf("the list marker was lost:\n%s", out)
	}
}
