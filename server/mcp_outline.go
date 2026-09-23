package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Reading a long page without swallowing it whole.
//
// get_page returned the entire page, always, with no cap anywhere on the path.
// That is right for most pages and ruinous for the tail: measured on a live
// instance of 173 pages, the median page renders to 1.3k characters — but the
// longest is 137k, some 34,000 tokens, and 16% are over 8k. An agent asked a
// question about one paragraph of the architecture notes had to load all of it.
//
// WHY NOT page_chunks. The chunk table already splits every page into passages
// with a heading path, maintained on every write, and it looks like exactly the
// right thing to read from. It is not: chunks.go builds them with
// blockPlainText, which keeps the words and drops everything else — no code
// fences, no table structure, no list markers, links reduced to their labels,
// images to nothing. That is correct for a search index, where only the words
// are searched, and wrong for a read, where an agent asking for the "Cursor"
// section of a setup document needs the JSON block intact. So the outline and
// the slicing are done here, on the block tree, the same source
// blocksToMarkdown renders from — and the one thing genuinely worth reusing
// from chunks.go is the heading PATH, which is the address an agent can say
// back.
//
// The cut is automatic, not opt-in. A parameter an agent has to know about is a
// parameter it will not use — the same lesson as the workspace tool two commits
// ago: an agent takes the move in front of it, so the default has to be the
// safe one. Over the threshold get_page answers with the outline and the first
// section, and says how to ask for the rest.

// outlineThreshold is where get_page stops handing back the whole page.
//
// 8000 characters is about 2000 tokens. On the instance this was measured
// against it leaves 84% of pages untouched and catches the 28 that are worth
// catching, which is the shape you want: the agent that reads an ordinary note
// never learns this mechanism exists.
const outlineThreshold = 8000

// listDepthDefault is how far down the page tree list goes before it stops and
// says what is left. Two levels — the roots and what hangs directly off them —
// is the depth at which the answer is still a map rather than the territory.
const listDepthDefault = 2

// headingSep joins a heading path. Same separator as chunks.go, so the address
// an agent reads out of a search hit is the address section takes.
const headingSep = " › "

// pageSection is one heading and everything under it, down to the next heading
// of the same or a higher level.
type pageSection struct {
	Path   string // "Hợp đồng › Chấm dứt"
	Level  int
	Blocks []mdBlock
	// Chars is the size of this section rendered, so the outline can say what a
	// read would cost before the agent commits to it.
	Chars int
}

// sectionsOf cuts a page into its headed sections.
//
// A section runs from its heading to the next heading at the same level or
// above — the way a reader understands a document, not the way the block list
// happens to be nested. Blocks before the first heading are a section too, with
// an empty path: dropping them would silently lose the opening paragraph, which
// is often the only summary a page has.
func sectionsOf(content []byte) []pageSection {
	var blocks []mdBlock
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	var out []pageSection
	var trail []string

	// Headings can arrive as siblings (what BlockNote writes) or as parents of
	// their own content (what an import can produce). Flattening first means the
	// cut below only has to handle one shape.
	var flat []mdBlock
	var flatten func(bs []mdBlock)
	flatten = func(bs []mdBlock) {
		for _, blk := range bs {
			if blk.Type == "heading" {
				kids := blk.Children
				blk.Children = nil
				flat = append(flat, blk)
				flatten(kids)
				continue
			}
			flat = append(flat, blk)
		}
	}
	flatten(blocks)

	cur := pageSection{}
	push := func() {
		if len(cur.Blocks) == 0 && cur.Path == "" {
			return
		}
		var b strings.Builder
		renderBlocks(&b, cur.Blocks, 0)
		cur.Chars = len(b.String())
		out = append(out, cur)
	}
	for _, blk := range flat {
		if blk.Type != "heading" {
			cur.Blocks = append(cur.Blocks, blk)
			continue
		}
		push()
		level := intProp(blk.Props, "level", 1)
		if level < 1 {
			level = 1
		}
		if level > 6 {
			level = 6
		}
		text := strings.TrimSpace(renderInline(blk.Content))
		if level > len(trail) {
			trail = append(trail, text)
		} else {
			trail = append(trail[:level-1], text)
		}
		cur = pageSection{Path: strings.Join(trail, headingSep), Level: level, Blocks: []mdBlock{blk}}
	}
	push()
	return out
}

// renderSection is the markdown of one section.
func renderSection(sec pageSection) string {
	var b strings.Builder
	renderBlocks(&b, sec.Blocks, 0)
	return strings.TrimRight(b.String(), "\n")
}

// outlineOf renders the heading tree with the cost of each section.
//
// The sizes are the point. An outline that only lists headings makes an agent
// guess which one is worth opening; one that says "~14.2k" next to it turns
// that into a decision. Sizes are rounded, because a figure like 14237 invites
// arithmetic nobody needs.
func outlineOf(title string, secs []pageSection) string {
	var b strings.Builder
	total := 0
	for _, s := range secs {
		total += s.Chars
	}
	fmt.Fprintf(&b, "# %s — outline (%s in %d section(s))\n\n", title, approxSize(total), len(secs))
	for _, s := range secs {
		if s.Path == "" {
			fmt.Fprintf(&b, "- (opening, before the first heading) — %s\n", approxSize(s.Chars))
			continue
		}
		// The LAST heading is the one being listed; the path above it is already
		// visible from the indentation.
		name := s.Path
		if i := strings.LastIndex(name, headingSep); i >= 0 {
			name = name[i+len(headingSep):]
		}
		fmt.Fprintf(&b, "%s- %s — %s\n", strings.Repeat("  ", s.Level-1), name, approxSize(s.Chars))
	}
	b.WriteString("\nRead one with get_page(page_id, section: \"…\"), naming the full path as it is " +
		"written above, e.g. \"" + exampleSectionPath(secs) + "\". " +
		"Pass section: \"*\" to read the whole page anyway.\n")
	return b.String()
}

// pageReadout is what get_page hands back for an ordinary page.
//
// The order of the checks is the design. An explicit ask — outline, or a named
// section — is answered exactly, however long or short the page is; the
// abbreviation only ever applies to a call that asked for nothing in
// particular, which is the call that used to return 137k characters.
func pageReadout(p *page, outline bool, section string) (string, error) {
	head := "# "
	if p.Icon != "" {
		head += p.Icon + " "
	}
	title := p.Title
	if title == "" {
		title = "Untitled"
	}
	head += title

	full := blocksToMarkdown(p.Content)
	if outline || (section == "" && len(full) > outlineThreshold) {
		secs := sectionsOf(p.Content)
		// A long page with no headings cannot be abbreviated into anything
		// useful, and an outline of one line would be a worse answer than the
		// page. Handing it over whole is the honest outcome.
		if headedCount(secs) == 0 {
			if outline {
				return head + "\n\n(this page has no headings, so there is no outline — " +
					approxSize(len(full)) + " in all)\n", nil
			}
			return head + "\n\n" + full, nil
		}
		out := head + "\n\n" + outlineOf(title, secs)
		if !outline {
			// The opening comes along uninvited, because it is usually the summary
			// and an outline without it makes the agent spend a second call to
			// learn what the page is even about.
			for _, s := range secs {
				if s.Path == "" && s.Chars > 0 {
					out += "\n" + renderSection(s) + "\n"
					break
				}
			}
		}
		return out, nil
	}
	if section != "" && section != "*" {
		secs := sectionsOf(p.Content)
		sec, err := findSection(secs, section)
		if err != nil {
			return "", err
		}
		return head + "\n\n" + renderSection(sec) + "\n", nil
	}
	return head + "\n\n" + full, nil
}

// headedCount counts the sections that came from a heading — the opening block
// before the first one is a section, but it is not an outline entry.
func headedCount(secs []pageSection) int {
	n := 0
	for _, s := range secs {
		if s.Path != "" {
			n++
		}
	}
	return n
}

// exampleSectionPath picks a real path for the hint, so the example an agent
// copies is one that actually resolves.
func exampleSectionPath(secs []pageSection) string {
	for _, s := range secs {
		if s.Path != "" {
			return s.Path
		}
	}
	return ""
}

func approxSize(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d chars", n)
	}
	return fmt.Sprintf("~%.1fk chars", float64(n)/1000)
}

// findSection resolves what the agent said to a section.
//
// The full path is the canonical form, but an agent that read "Cursor" off a
// search hit should not have to reconstruct "Kết nối › Cursor" to open it — so
// a unique match on the last heading alone is accepted too. Ambiguity is
// refused rather than guessed: two sections called "Overview" in one document
// is exactly the situation where picking one silently is worst.
func findSection(secs []pageSection, want string) (pageSection, error) {
	want = strings.TrimSpace(want)
	for _, s := range secs {
		if strings.EqualFold(s.Path, want) {
			return s, nil
		}
	}
	var hits []pageSection
	for _, s := range secs {
		name := s.Path
		if i := strings.LastIndex(name, headingSep); i >= 0 {
			name = name[i+len(headingSep):]
		}
		if strings.EqualFold(name, want) {
			hits = append(hits, s)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		var paths []string
		for _, s := range hits {
			paths = append(paths, fmt.Sprintf("%q", s.Path))
		}
		return pageSection{}, fmt.Errorf("%q names %d sections on this page — say which by its full path: %s",
			want, len(hits), strings.Join(paths, ", "))
	}
	var all []string
	for _, s := range secs {
		if s.Path != "" {
			all = append(all, fmt.Sprintf("%q", s.Path))
		}
	}
	if len(all) == 0 {
		return pageSection{}, fmt.Errorf("this page has no headings, so it has no sections to ask for — read it without section")
	}
	return pageSection{}, fmt.Errorf("no section called %q on this page. It has: %s", want, strings.Join(all, ", "))
}
