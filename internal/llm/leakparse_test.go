package llm

import (
	"strings"
	"testing"
)

func knownTools(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

var testKnown = knownTools("read_file", "write_file", "run_shell", "list_files")

func TestExtract_TaggedCallColon(t *testing.T) {
	content := `<|tool_call>call:read_file{"path": "a.txt"}`
	calls, cleaned, leak := ExtractLeakedCalls(content, testKnown)
	if !leak || len(calls) != 1 {
		t.Fatalf("calls=%v leak=%v", calls, leak)
	}
	if calls[0].Name != "read_file" || !strings.Contains(string(calls[0].Arguments), "a.txt") {
		t.Fatalf("call=%+v", calls[0])
	}
	if cleaned != "" {
		t.Fatalf("cleaned=%q, want empty", cleaned)
	}
}

func TestExtract_TaggedNameArgsObject(t *testing.T) {
	content := `Let me read it. <tool_call>{"name":"read_file","arguments":{"path":"a.txt"}}</tool_call> done.`
	calls, cleaned, leak := ExtractLeakedCalls(content, testKnown)
	if !leak || len(calls) != 1 || calls[0].Name != "read_file" {
		t.Fatalf("calls=%v leak=%v", calls, leak)
	}
	if !strings.Contains(cleaned, "Let me read it.") || !strings.Contains(cleaned, "done.") {
		t.Fatalf("prose lost: %q", cleaned)
	}
	if strings.Contains(cleaned, "tool_call") {
		t.Fatalf("tag left in: %q", cleaned)
	}
}

func TestExtract_Narrative(t *testing.T) {
	content := `(Made a function call call_92023 to read_file with arguments={"path": "a.txt"})`
	calls, _, leak := ExtractLeakedCalls(content, testKnown)
	if !leak || len(calls) != 1 || calls[0].Name != "read_file" {
		t.Fatalf("calls=%v leak=%v", calls, leak)
	}
}

func TestExtract_EnvelopeArray(t *testing.T) {
	content := `[{"name": "list_files", "arguments": {"path": "."}}, {"name": "read_file", "arguments": {"path": "a.txt"}}]`
	calls, cleaned, leak := ExtractLeakedCalls(content, testKnown)
	if !leak || len(calls) != 2 {
		t.Fatalf("calls=%v leak=%v", calls, leak)
	}
	if cleaned != "" {
		t.Fatalf("cleaned=%q", cleaned)
	}
}

func TestExtract_FencedEnvelope(t *testing.T) {
	content := "Here:\n```json\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"a.txt\"}}\n```\nThanks."
	calls, cleaned, leak := ExtractLeakedCalls(content, testKnown)
	if !leak || len(calls) != 1 {
		t.Fatalf("calls=%v leak=%v", calls, leak)
	}
	if !strings.Contains(cleaned, "Here:") || !strings.Contains(cleaned, "Thanks.") {
		t.Fatalf("prose lost: %q", cleaned)
	}
}

func TestExtract_UnknownToolNotRecovered(t *testing.T) {
	content := `<tool_call>call:delete_everything{"path": "."}`
	calls, _, leak := ExtractLeakedCalls(content, testKnown)
	if len(calls) != 0 {
		t.Fatalf("recovered unknown tool: %v", calls)
	}
	if !leak {
		t.Fatal("want leak=true for the nudge path")
	}
}

func TestExtract_BrokenJSONNotRecovered(t *testing.T) {
	content := `<tool_call>call:read_file{"path": `
	calls, cleaned, leak := ExtractLeakedCalls(content, testKnown)
	if len(calls) != 0 {
		t.Fatalf("recovered broken JSON: %v", calls)
	}
	if !leak {
		t.Fatal("want leak=true")
	}
	_ = cleaned
}

func TestExtract_PartialTailHeldBack(t *testing.T) {
	content := "Let me check <tool_"
	calls, cleaned, leak := ExtractLeakedCalls(content, testKnown)
	if len(calls) != 0 || !leak {
		t.Fatalf("calls=%v leak=%v", calls, leak)
	}
	if strings.Contains(cleaned, "<tool_") {
		t.Fatalf("fragment kept: %q", cleaned)
	}
}

func TestExtract_ProseNeverParses(t *testing.T) {
	prose := []string{
		"plain answer with no tools",
		"I used read_file to check a.txt and it returned 42 lines.",
		"The function call syntax is call_name(arguments) in Python.",
		"```python\nread_file('a.txt')\n```",
		`{"note": "not a tool call, just JSON"}`,
		"run_shell with ls shows the directory",
	}
	for _, p := range prose {
		calls, _, leak := ExtractLeakedCalls(p, testKnown)
		if len(calls) != 0 || leak {
			t.Errorf("%q: calls=%v leak=%v", p, calls, leak)
		}
	}
}

func TestExtract_ValuesMayContainTags(t *testing.T) {
	// Strata's case: argument values containing tag-like text must not
	// confuse the scanner.
	content := `<tool_call>{"name":"write_file","arguments":{"path":"a.txt","content":"<p>hi</p>"}}</tool_call>`
	calls, _, leak := ExtractLeakedCalls(content, testKnown)
	if !leak || len(calls) != 1 || calls[0].Name != "write_file" {
		t.Fatalf("calls=%v leak=%v", calls, leak)
	}
	if !strings.Contains(string(calls[0].Arguments), "<p>hi</p>") {
		t.Fatalf("args=%s", calls[0].Arguments)
	}
}

func TestLeakCallID_Stable(t *testing.T) {
	if LeakCallID(3, 0) == LeakCallID(3, 1) || LeakCallID(3, 0) == LeakCallID(4, 0) {
		t.Fatal("ids must be unique per step and index")
	}
}

func TestExtract_ThinkSketchIgnored(t *testing.T) {
	// Deliberation is not decision: a call sketched inside think tags,
	// then decided in prose, must neither execute nor nudge.
	content := `<think>I should call read_file on a.txt. <tool_call>call:read_file{"path": "a.txt"}</tool_call> On second thought, no need.</think> I already know the answer.`
	calls, cleaned, leak := ExtractLeakedCalls(content, testKnown)
	if len(calls) != 0 || leak {
		t.Fatalf("think sketch: calls=%v leak=%v", calls, leak)
	}
	if !strings.Contains(cleaned, "I already know") {
		t.Fatalf("prose lost: %q", cleaned)
	}
}

func TestExtract_ThinkBlockMessageIgnored(t *testing.T) {
	// A message that opens with a think block wrapping JSON is
	// deliberation, not a call envelope — even valid JSON inside.
	content := "<think>{\"name\": \"read_file\", \"arguments\": {\"path\": \"a.txt\"}}</think>"
	calls, _, leak := ExtractLeakedCalls(content, testKnown)
	if len(calls) != 0 || leak {
		t.Fatalf("think envelope: calls=%v leak=%v", calls, leak)
	}
}

func TestExtract_FencedInsideThinkIgnored(t *testing.T) {
	content := "<think>options:\n```json\n{\"name\": \"read_file\", \"arguments\": {}}\n```\nno</think> yes."
	calls, _, _ := ExtractLeakedCalls(content, testKnown)
	if len(calls) != 0 {
		t.Fatalf("fenced think sketch recovered: %v", calls)
	}
}

func TestExtract_RealCallBesideThinkRecovers(t *testing.T) {
	// Think-awareness must not blind the parser to a real leak outside
	// the think block.
	content := `<think>hmm</think> <tool_call>call:read_file{"path": "a.txt"}</tool_call>`
	calls, cleaned, leak := ExtractLeakedCalls(content, testKnown)
	if !leak || len(calls) != 1 || calls[0].Name != "read_file" {
		t.Fatalf("calls=%v leak=%v", calls, leak)
	}
	if strings.Contains(cleaned, "tool_call") {
		t.Fatalf("tag left in: %q", cleaned)
	}
}
