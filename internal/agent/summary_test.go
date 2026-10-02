package agent

import (
	"strings"
	"testing"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
)

func TestFindCutPoint_NeverOrphansToolResult(t *testing.T) {
	groups := [][]llm.Message{
		{{Role: llm.RoleAssistant, Content: "a"}},
		{{Role: llm.RoleUser, Content: "stray"}},
		{{Role: llm.RoleAssistant, Content: "b"}},
	}
	// Wanting 1 would keep groups[1:] starting with non-assistant; must back off to 0.
	if got := findCutPoint(groups, 1); got != 0 {
		t.Fatalf("cut = %d, want 0", got)
	}
	if got := findCutPoint(groups, 2); got != 2 {
		t.Fatalf("orphan cut = %d, want 2", got)
	}
}

func TestSummarizeDropped_ListsToolsAndFiles(t *testing.T) {
	dropped := [][]llm.Message{{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "1", Name: "write_file", Arguments: []byte(`{"path":"a.go"}`)},
			{ID: "2", Name: "grep_files", Arguments: []byte(`{}`)},
		}},
		{Role: llm.RoleTool, ToolCallID: "1", Content: "ok"},
	}}
	s := summarizeDropped(dropped)
	if !strings.Contains(s, "write_file") || !strings.Contains(s, "a.go") {
		t.Fatalf("summary missing facts: %q", s)
	}
}

func TestCompactNotice_StartsWithBaseNotice(t *testing.T) {
	h := historyWithSteps(10)
	got := compactHistory(h, 2)
	if len(got) < 3 {
		t.Fatalf("too short: %d", len(got))
	}
	if !strings.HasPrefix(got[2].Content, prompts.CompactNotice) {
		t.Fatalf("notice prefix lost: %q", got[2].Content)
	}
	if !strings.Contains(got[2].Content, "dropped") {
		t.Fatalf("extractive summary missing: %q", got[2].Content)
	}
}
