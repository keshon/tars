package session

import (
	"os"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

func TestAppendLoadRoundTrip(t *testing.T) {
	path := LogPath(t.TempDir() + "/state.json")
	h := []llm.Message{{Role: llm.RoleUser, Content: "hi"}}
	Append(path[:len(path)-len(".jsonl")]+".json", 0, h)
	Append(path[:len(path)-len(".jsonl")]+".json", 1, append(h, llm.Message{Role: llm.RoleAssistant, Content: "ok"}))
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 || got[1].Content != "ok" {
		t.Fatalf("unexpected history: %+v", got)
	}
}

func TestLoadSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	state := dir + "/state.json"
	Append(state, 0, []llm.Message{{Role: llm.RoleUser, Content: "a"}})
	f, _ := os.OpenFile(LogPath(state), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("not json\n")
	_ = f.Close()
	got, err := Load(LogPath(state))
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestLoadEmptyFails(t *testing.T) {
	if _, err := Load(t.TempDir() + "/missing.jsonl"); err == nil {
		t.Fatal("expected error")
	}
}

func TestTitleFor_SkipsHarnessAndSystem(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "[harness] verify yourself"},
		{Role: llm.RoleUser, Content: "real task\nsecond line"},
	}
	if got := TitleFor(history); got != "real task" {
		t.Fatalf("TitleFor = %q", got)
	}
	if got := TitleFor(nil); got != "" {
		t.Fatalf("TitleFor(nil) = %q", got)
	}
}
