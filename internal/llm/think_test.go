package llm

import (
	"testing"
)

func TestThinkRegions_Basic(t *testing.T) {
	r := ThinkRegions("answer <think>hmm</think> done")
	if len(r) != 1 {
		t.Fatalf("regions=%v", r)
	}
	if got := ThinkChars("answer <think>hmm</think> done"); got != 3 {
		t.Fatalf("chars=%d", got)
	}
}

func TestThinkRegions_UnclosedRunsToEnd(t *testing.T) {
	r := ThinkRegions("answer <think>cut off")
	if len(r) != 1 || r[0][1] != len("answer <think>cut off") {
		t.Fatalf("regions=%v", r)
	}
}

func TestThinkRegions_Multiple(t *testing.T) {
	r := ThinkRegions("<think>a</think> mid <think>bb</think>")
	if len(r) != 2 {
		t.Fatalf("regions=%v", r)
	}
	if got := ThinkChars("<think>a</think> mid <think>bb</think>"); got != 3 {
		t.Fatalf("chars=%d", got)
	}
}

func TestThinkRegions_CaseSensitive(t *testing.T) {
	if r := ThinkRegions("<THINK>x</THINK>"); len(r) != 0 {
		t.Fatalf("uppercase must not parse: %v", r)
	}
}

func TestThinkChars_Empty(t *testing.T) {
	if got := ThinkChars("no thinking here"); got != 0 {
		t.Fatalf("chars=%d", got)
	}
	if got := ThinkChars("<think></think>"); got != 0 {
		t.Fatalf("chars=%d", got)
	}
}

func TestThinkChars_Unicode(t *testing.T) {
	if got := ThinkChars("<think>привет</think>"); got != 6 {
		t.Fatalf("chars=%d, want runes not bytes", got)
	}
}

func TestDeliberationChars_BothChannels(t *testing.T) {
	m := Message{Role: RoleAssistant, Content: "<think>ab</think>ok", Reasoning: "cde"}
	if got := DeliberationChars(m); got != 5 {
		t.Fatalf("chars=%d, want 2 think + 3 reasoning", got)
	}
	if got := DeliberationChars(Message{Content: "plain"}); got != 0 {
		t.Fatalf("chars=%d, want 0", got)
	}
}

func TestStripRegions(t *testing.T) {
	got := stripRegions("a <think>b</think> c", [][2]int{{2, 18}})
	if got != "a  c" {
		t.Fatalf("got %q", got)
	}
	if got := stripRegions("plain", nil); got != "plain" {
		t.Fatalf("got %q", got)
	}
}

func TestInRegions(t *testing.T) {
	r := [][2]int{{5, 10}}
	if !inRegions(7, r) || inRegions(4, r) || inRegions(10, r) {
		t.Fatal("boundary wrong: start inclusive, end exclusive")
	}
}
