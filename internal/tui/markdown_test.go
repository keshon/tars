package tui

import (
	"strings"
	"testing"
)

func TestMarkdownSpans(t *testing.T) {
	st := defaultStyles()
	out := renderBlock(answerBlock("Say **hi** and `code`."), st, false, 80)
	if strings.Contains(out, "**") || strings.Contains(out, "`") {
		t.Fatalf("markers survived: %q", out)
	}
	for _, want := range []string{"hi", "code"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %q", want, out)
		}
	}
}

func TestMarkdownHeaders(t *testing.T) {
	st := defaultStyles()
	out := renderBlock(answerBlock("# Title\n#tag stays"), st, false, 80)
	if !strings.Contains(out, "Title") || !strings.Contains(out, "#tag stays") {
		t.Fatalf("header handling wrong: %q", out)
	}
	// The "# Title" marker must go; "#tag" must survive.
	lines := strings.Split(out, "\n")
	for _, ln := range lines {
		if strings.Contains(ln, "Title") && strings.Contains(ln, "#") {
			t.Fatalf("header marker survived: %q", ln)
		}
	}
}

func TestMarkdownFence(t *testing.T) {
	st := defaultStyles()
	out := renderBlock(answerBlock("```go\n# not header\n```"), st, false, 80)
	if !strings.Contains(out, "# not header") {
		t.Fatalf("fence content altered: %q", out)
	}
}

func TestMarkdownBullets(t *testing.T) {
	st := defaultStyles()
	out := renderBlock(answerBlock("- item\n  * sub"), st, false, 80)
	if !strings.Contains(out, "• item") || !strings.Contains(out, "• sub") {
		t.Fatalf("bullets not prettified: %q", out)
	}
}

func TestMarkdownWrapSplitSpan(t *testing.T) {
	st := defaultStyles()
	// Unstamped to isolate wrapping from the timestamp reserve.
	out := renderBlock(block{role: roleAnswer, text: "say **bold words here** today"}, st, false, 14)
	if strings.Contains(out, "**") {
		t.Fatalf("wrap split the span: %q", out)
	}
	for _, want := range []string{"bold", "today"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %q", want, out)
		}
	}
}

func TestMarkdownGlobSafe(t *testing.T) {
	st := defaultStyles()
	out := renderBlock(toolCardBlock("", "run rm **/*.go"), st, false, 80)
	if !strings.Contains(out, "**/*.go") {
		t.Fatalf("tool args must stay raw: %q", out)
	}
}

func TestMarkdownTableGrid(t *testing.T) {
	rows := renderTable([]string{
		"| A | B |",
		"| --- | --- |",
		"| x | yy |",
	})
	want := []string{"| A | B  |", "| ─── | ─── |", "| x | yy |"}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for i := range want {
		if rows[i].text != want[i] {
			t.Fatalf("row %d = %q, want %q", i, rows[i].text, want[i])
		}
	}
	if !rows[0].header || !rows[1].fence || rows[2].header || rows[2].fence {
		t.Fatalf("row kinds wrong: %+v", rows)
	}
}

func TestMarkdownTableAlignRight(t *testing.T) {
	rows := renderTable([]string{
		"| Name | Number |",
		"| --- | ---: |",
		"| ab | 7 |",
	})
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if !strings.Contains(rows[2].text, "|      7 |") {
		t.Fatalf("right align missing: %q", rows[2].text)
	}
}

func TestMarkdownTableEndToEnd(t *testing.T) {
	st := defaultStyles()
	in := "4. Risks\n| Risk | Level |\n| --- | --- |\n| Sandbox | High |"
	out := renderBlock(answerBlock(in), st, false, 80)
	if strings.Contains(out, ":---") || strings.Contains(out, "| ---") {
		t.Fatalf("separator row leaked: %q", out)
	}
	for _, want := range []string{"Risk", "Sandbox", "High"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %q", want, out)
		}
	}
}

func TestMarkdownPipesLiteral(t *testing.T) {
	st := defaultStyles()
	out := renderBlock(answerBlock("echo a | grep b"), st, false, 80)
	if !strings.Contains(out, "echo a | grep b") {
		t.Fatalf("pipe line altered: %q", out)
	}
}
