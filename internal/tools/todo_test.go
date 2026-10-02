package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTodo_UpdateAndRender(t *testing.T) {
	todo := NewTodo()
	out, err := todo.Run(context.Background(), json.RawMessage(
		`{"items":[{"id":1,"state":"pending","text":"read spec"},{"id":2,"state":"in_progress","text":"write code"}]}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "[ ] 1. read spec") || !strings.Contains(out, "[>] 2. write code") {
		t.Fatalf("out=%q", out)
	}
	if !strings.Contains(out, "(0/2 done)") {
		t.Fatalf("missing count: %q", out)
	}
	if len(todo.Items()) != 2 {
		t.Fatalf("items=%v", todo.Items())
	}
}

func TestTodo_RejectsBadInput(t *testing.T) {
	todo := NewTodo()
	cases := []string{
		`{"items":[{"id":1,"state":"maybe","text":"x"}]}`,
		`{"items":[{"id":1,"state":"done","text":"a"},{"id":1,"state":"done","text":"b"}]}`,
		`{"items":[{"id":1,"state":"done","text":"  "}]}`,
		`{"items":[{"id":1,"state":"done","text":"x"}],"extra":true}`,
	}
	for _, c := range cases {
		if _, err := todo.Run(context.Background(), json.RawMessage(c)); err == nil {
			t.Errorf("expected error for %s", c)
		}
	}
}

func TestTodo_ConcurrentSafe(t *testing.T) {
	todo := NewTodo()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			_, _ = todo.Run(context.Background(), json.RawMessage(
				`{"items":[{"id":1,"state":"done","text":"x"}]}`))
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
