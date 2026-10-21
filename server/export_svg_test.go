package server

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// The stored picture is page content: it is written by whoever last edited the
// page, and it lands in a document other people open. Mermaid rendering with
// securityLevel 'strict' is the first line; this is the second, and it has to
// hold even when the first was bypassed by writing the block over the API.
func TestSanitizeSVGDropsExecutableMarkup(t *testing.T) {
	good := `<svg viewBox="0 0 10 10"><path d="M0 0h10v10H0z"/><text>A --&gt; B</text></svg>`
	if got := sanitizeSVG(good); got != good {
		t.Errorf("a plain diagram was altered:\n%s", got)
	}

	for _, bad := range []string{
		`<svg><script>alert(1)</script></svg>`,
		`<svg><SCRIPT>alert(1)</SCRIPT></svg>`,
		`<svg onload="alert(1)"></svg>`,
		`<svg><rect onclick="alert(1)"/></svg>`,
		`<svg><image onerror="alert(1)" href="x"/></svg>`,
		`<svg><a href="javascript:alert(1)">x</a></svg>`,
		`<svg><foreignObject><iframe src="x"></iframe></foreignObject></svg>`,
	} {
		if got := sanitizeSVG(bad); got != "" {
			t.Errorf("sanitizeSVG kept executable markup: %q → %q", bad, got)
		}
	}
}

// The sanitizer is a list, and a list misses things: onbegin on an SVG
// animation walked past it and ran with the session of whoever opened the print
// view or the shared link. The drawing now goes out as an <img>, where no
// script runs at all, so the list no longer decides whether code executes.
func TestDiagramGoesOutAsAPictureNotAsMarkup(t *testing.T) {
	svg := `<svg viewBox="0 0 120 40"><rect width="10" height="10"><animate attributeName="x" dur="1s" onbegin="alert(document.domain)"/></rect><text>A</text></svg>`
	quoted, _ := json.Marshal(svg)
	content := `[{"id":"d1","type":"mermaid","props":{"code":"graph LR; A-->B","svg":` + string(quoted) + `},"content":[],"children":[]}]`
	out := blocksToHTML([]byte(content))

	if strings.Contains(out, "onbegin") || strings.Contains(out, "<animate") {
		t.Fatalf("the stored drawing is in the page as markup:\n%s", out)
	}
	const lead = `src="data:image/svg+xml;base64,`
	i := strings.Index(out, lead)
	if i < 0 || !strings.Contains(out, `<img alt="graph LR; A--&gt;B"`) {
		t.Fatalf("the diagram is not an image carrying its source as alt text:\n%s", out)
	}
	rest := out[i+len(lead):]
	encoded := rest[:strings.IndexByte(rest, '"')]
	if got, err := base64.StdEncoding.DecodeString(encoded); err != nil || string(got) != svg {
		t.Errorf("the picture is not the stored drawing: %v\n%s", err, got)
	}
	// The size still comes from the drawing, so a page break can be planned
	// before the picture has loaded.
	if !strings.Contains(out, `aspect-ratio:120 / 40`) {
		t.Errorf("the box lost the drawing's proportions:\n%s", out)
	}
}
