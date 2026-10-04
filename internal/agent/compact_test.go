package agent

import (
	"strings"
	"testing"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
)

func assistantStep(id string) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "echo"}}},
		{Role: llm.RoleTool, ToolCallID: id, Content: "ok"},
	}
}

func TestCompactHistory_KeepsPrefixAndRecentGroups(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
	}
	for i := 0; i < 15; i++ {
		history = append(history, assistantStep("c")...)
	}

	got := compactHistory(history, 8)
	if len(got) < 2+1+8*2 {
		t.Fatalf("too short: %d messages", len(got))
	}
	if got[0].Content != "sys" || got[1].Content != "task" {
		t.Fatal("prefix lost")
	}
	if got[2].Content != harnessText(prompts.CompactNotice) && len(got[2].Content) <= len(prompts.CompactNotice) {
		t.Fatalf("expected compact notice at [2], got %q", got[2].Content)
	}
	if want := harnessText(prompts.CompactNotice); len(got[2].Content) < len(want) || got[2].Content[:len(want)] != want {
		t.Fatalf("expected marked compact notice prefix at [2], got %q", got[2].Content)
	}
}

func TestCompactHistory_NoOrphanToolResults(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
	}
	for i := 0; i < 10; i++ {
		id := "call"
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "echo"}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: "ok"},
		)
	}

	got := compactHistory(history, 3)
	for i, m := range got {
		if m.Role != llm.RoleTool {
			continue
		}
		if i == 0 || got[i-1].Role != llm.RoleAssistant {
			t.Fatalf("orphan tool result at %d", i)
		}
	}
}

func TestCompactHistory_DisabledWhenKeepStepsZero(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "hi"},
	}
	if got := compactHistory(history, 0); len(got) != len(history) {
		t.Fatal("expected unchanged history")
	}
}

func TestStepGroups_SplitsOnAssistant(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: "a1"},
		{Role: llm.RoleTool, Content: "t1"},
		{Role: llm.RoleUser, Content: "nudge"},
		{Role: llm.RoleAssistant, Content: "a2"},
	}
	groups := stepGroups(msgs)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if len(groups[0]) != 3 || len(groups[1]) != 1 {
		t.Fatalf("unexpected group sizes: %d, %d", len(groups[0]), len(groups[1]))
	}
}

// Delegate reports name no files in their call arguments — the touched
// paths live in the result envelope. They must survive in the notice,
// or post-compaction history remembers that a subagent ran but not what
// it changed.
func TestSummarizeDropped_KeepsDelegatePaths(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
	}
	for i := 0; i < 10; i++ {
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c", Name: "delegate_task"}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: "c", Content: "DELEGATE\nmutations: 2\npaths: sub/a.go\npaths: sub/b.go\n----\ndone"},
		)
	}
	got := compactHistory(history, 3)
	if len(got) < 3 {
		t.Fatalf("too short: %d messages", len(got))
	}
	notice := got[2].Content
	if !strings.Contains(notice, "sub/a.go") || !strings.Contains(notice, "sub/b.go") {
		t.Fatalf("delegate paths lost from notice: %q", notice)
	}
}

// history builds a transcript with n assistant-led step groups after the
// system/user prefix, which is the shape compactHistory splits on.
func historyWithSteps(n int) []llm.Message {
	out := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
	}
	for i := 0; i < n; i++ {
		out = append(out,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c", Name: "read_file"}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: "c", Content: "contents"},
		)
	}
	return out
}

func compactAgent(t *testing.T, atPercent int) *Agent {
	t.Helper()
	return New(Config{
		Client: &stubClient{}, Tools: NewRegistry(), System: "sys",
		ContextLimit: 1000, CompactKeepSteps: 2, CompactAtPercent: atPercent,
	})
}

func TestMaybeCompact_LeavesHistoryAloneBelowThreshold(t *testing.T) {
	a := compactAgent(t, 60)
	h := historyWithSteps(10)
	before := len(h)
	if a.maybeCompact(&h, llm.Usage{PromptTokens: 500}, &runState{}) {
		t.Error("compacted at 50% with a 60% threshold")
	}
	if len(h) != before {
		t.Errorf("history changed anyway: %d -> %d", before, len(h))
	}
}

func TestMaybeCompact_FiresAtTheThreshold(t *testing.T) {
	a := compactAgent(t, 60)
	h := historyWithSteps(10)
	before := len(h)
	if !a.maybeCompact(&h, llm.Usage{PromptTokens: 600}, &runState{}) {
		t.Fatal("did not compact at the threshold")
	}
	if len(h) >= before {
		t.Errorf("history not reduced: %d -> %d", before, len(h))
	}
}

// A long run that compacted once and then grew again is in exactly the
// state compaction exists for. The old implementation compacted at most
// once per run and left everything after that unprotected.
func TestMaybeCompact_CompactsAgainAfterHistoryGrows(t *testing.T) {
	a := compactAgent(t, 60)
	st := &runState{}
	h := historyWithSteps(10)
	if !a.maybeCompact(&h, llm.Usage{PromptTokens: 700}, st) {
		t.Fatal("first compaction did not fire")
	}
	h = append(h, historyWithSteps(10)[2:]...) // the run keeps going
	if !a.maybeCompact(&h, llm.Usage{PromptTokens: 700}, st) {
		t.Fatal("second compaction did not fire — a long run stays unprotected")
	}
}

// Compacting an already-minimal history every step would spend work and
// drop nothing.
func TestMaybeCompact_StopsWhenThereIsNothingLeftToDrop(t *testing.T) {
	a := compactAgent(t, 60)
	st := &runState{}
	h := historyWithSteps(10)
	if !a.maybeCompact(&h, llm.Usage{PromptTokens: 900}, st) {
		t.Fatal("first compaction did not fire")
	}
	if a.maybeCompact(&h, llm.Usage{PromptTokens: 900}, st) {
		t.Error("compacted again with nothing left to drop")
	}
}
