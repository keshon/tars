package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/keshon/tars/internal/workspace"
)

func testWS(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return ws, dir
}

func writeTmp(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runTool(t *testing.T, tool interface {
	Run(context.Context, json.RawMessage) (string, error)
}, args string) (string, error) {
	t.Helper()
	return tool.Run(context.Background(), json.RawMessage(args))
}

func TestPatchFile_EmptyOldContentRejected(t *testing.T) {
	ws, dir := testWS(t)
	writeTmp(t, dir, "a.txt", "one\ntwo\nthree\nfour\nfive\n")
	_, err := runTool(t, PatchFile{WS: ws}, `{"path":"a.txt","old_content":"","new_content":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty rejection, got %v", err)
	}
}

func TestPatchFile_IdenticalRejected(t *testing.T) {
	ws, dir := testWS(t)
	writeTmp(t, dir, "a.txt", "one\ntwo\nthree\nfour\nfive\n")
	_, err := runTool(t, PatchFile{WS: ws}, `{"path":"a.txt","old_content":"two","new_content":"two"}`)
	if err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("expected identical rejection, got %v", err)
	}
}

func TestPatchFile_OversizedNewContentRejected(t *testing.T) {
	ws, dir := testWS(t)
	writeTmp(t, dir, "a.txt", "one\ntwo\nthree\nfour\nfive\n")
	big := strings.Repeat("x", writeMaxBytes+1)
	_, err := runTool(t, PatchFile{WS: ws}, `{"path":"a.txt","old_content":"two","new_content":"`+big+`"}`)
	if err == nil || !strings.Contains(err.Error(), "write cap") {
		t.Fatalf("expected write-cap refusal, got %v", err)
	}
}

func TestPatchFile_ReturnsDiff(t *testing.T) {
	ws, dir := testWS(t)
	writeTmp(t, dir, "a.txt", "one\ntwo\nthree\nfour\nfive\nsix\n")
	out, err := runTool(t, PatchFile{WS: ws}, `{"path":"a.txt","old_content":"three","new_content":"THREE"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "-three") || !strings.Contains(out, "+THREE") || !strings.Contains(out, "@@") {
		t.Fatalf("expected unified diff, got %q", out)
	}
}

func TestPatchFile_CRLFPreserved(t *testing.T) {
	ws, dir := testWS(t)
	writeTmp(t, dir, "a.txt", "one\r\ntwo\r\nthree\r\nfour\r\nfive\r\nsix\r\n")
	_, err := runTool(t, PatchFile{WS: ws}, `{"path":"a.txt","old_content":"three","new_content":"THREE"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if !strings.Contains(string(data), "\r\n") || !strings.Contains(string(data), "THREE") {
		t.Fatalf("CRLF not preserved: %q", data)
	}
}

func TestPatchLines_IdenticalRejected(t *testing.T) {
	ws, dir := testWS(t)
	writeTmp(t, dir, "a.txt", "one\ntwo\nthree\n")
	_, err := runTool(t, PatchLines{WS: ws}, `{"path":"a.txt","start_line":2,"end_line":2,"new_content":"two"}`)
	if err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("expected identical rejection, got %v", err)
	}
}

func TestPatchLines_ReturnsDiff(t *testing.T) {
	ws, dir := testWS(t)
	writeTmp(t, dir, "a.txt", "one\ntwo\nthree\nfour\n")
	out, err := runTool(t, PatchLines{WS: ws}, `{"path":"a.txt","start_line":2,"end_line":3,"new_content":"TWO"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "@@") || !strings.Contains(out, "+TWO") {
		t.Fatalf("expected diff, got %q", out)
	}
}

func TestWriteFile_RejectsUnknownFields(t *testing.T) {
	ws, _ := testWS(t)
	_, err := runTool(t, WriteFile{WS: ws}, `{"path":"a.txt","content":"x","command":"y"}`)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field rejection, got %v", err)
	}
}

func TestMoveFile_RefusesOverwrite(t *testing.T) {
	ws, dir := testWS(t)
	writeTmp(t, dir, "a.txt", "a")
	writeTmp(t, dir, "b.txt", "b")
	_, err := runTool(t, MoveFile{WS: ws}, `{"from":"a.txt","to":"b.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected overwrite refusal, got %v", err)
	}
}

func TestPathLocks_Serialize(t *testing.T) {
	var mu sync.Mutex
	order := []int{}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = withPathLock("same.txt", func() (string, error) {
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
				return "ok", nil
			})
		}(i)
	}
	wg.Wait()
	if len(order) != 10 {
		t.Fatalf("order=%v", order)
	}
}
