package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

// seqClient replays scripted responses in order.
type seqClient struct {
	calls int
	steps []llm.ChatResponse
}

func (s *seqClient) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	r := s.steps[s.calls]
	s.calls++
	return r, nil
}

func budgetTodo(id, items string) llm.ChatResponse {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: id, Name: "todo", Arguments: json.RawMessage(`{"items":` + items + `}`)}}}}
}

func budgetRead(id string) llm.ChatResponse {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: id, Name: "read_file", Arguments: json.RawMessage(`{"path":"a.go"}`)}}}}
}

func budgetSay(text string) llm.ChatResponse {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: text}}
}

// gateAnswer scripts the operator side of the budget gate.
type gateAnswer struct {
	calls  int
	kinds  []agent.SuspendKind
	prompt string
	reply  string
	err    error
}

func (g *gateAnswer) answer(_ context.Context, req agent.SuspendRequest) (agent.SuspendReply, error) {
	g.calls++
	g.kinds = append(g.kinds, req.Kind)
	g.prompt = req.Prompt
	if g.err != nil {
		return agent.SuspendReply{}, g.err
	}
	return agent.SuspendReply{Answer: g.reply}, nil
}

func budgetEnv(t *testing.T, client llm.Client) (roles.Env, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return roles.Env{
		Client: client, WS: ws, Procs: tools.NewBackgroundProcesses(),
	}, dir
}

func stateFileIn(dir string) string {
	return filepath.Join(dir, "state.json")
}

// deathScript burns a full 25-step budget: one todo creating two open
// items, then 24 identical reads (first executes, rest refuse from
// cache — every step still costs a round trip).
func deathScript(extra ...llm.ChatResponse) []llm.ChatResponse {
	steps := []llm.ChatResponse{
		budgetTodo("t1", `[{"id":1,"state":"pending","text":"Alpha"},{"id":2,"state":"pending","text":"Beta"}]`),
	}
	for i := 0; i < 24; i++ {
		steps = append(steps, budgetRead("r"))
	}
	return append(steps, extra...)
}

func TestSession_BudgetGate_ApproveContinues(t *testing.T) {
	client := &seqClient{}
	env, dir := budgetEnv(t, client)
	ga := &gateAnswer{reply: "y"}
	sess, err := New(Config{
		Task: "audit", Env: env, StateFile: stateFileIn(dir), Answer: ga.answer,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.steps = append(deathScript(),
		budgetSay("one"), budgetSay("two"), budgetSay("three"), budgetSay("done"))

	out, err := sess.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	if ga.calls != 1 {
		t.Fatalf("gate asked %d times, want 1", ga.calls)
	}
	if len(ga.kinds) != 1 || ga.kinds[0] != agent.SuspendBudget {
		t.Fatalf("kinds = %v, want [budget]", ga.kinds)
	}
	for _, want := range []string{"Alpha", "25"} {
		if !strings.Contains(ga.prompt, want) {
			t.Fatalf("gate prompt missing %q:\n%s", want, ga.prompt)
		}
	}
	if client.calls != 29 {
		t.Fatalf("calls = %d, want 29 (25 to die, 4 to finish)", client.calls)
	}
}

func TestSession_BudgetGate_DenyKeepsError(t *testing.T) {
	client := &seqClient{steps: deathScript()}
	env, dir := budgetEnv(t, client)
	ga := &gateAnswer{reply: "n"}
	sess, err := New(Config{
		Task: "audit", Env: env, StateFile: stateFileIn(dir), Answer: ga.answer,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = sess.Run(context.Background())
	if err == nil || !errors.Is(err, agent.ErrMaxSteps) {
		t.Fatalf("expected max-steps error, got %v", err)
	}
	if ga.calls != 1 {
		t.Fatalf("gate asked %d times, want 1", ga.calls)
	}
}

func TestSession_BudgetGate_SkipsWithoutStateFile(t *testing.T) {
	client := &seqClient{steps: deathScript()}
	env, _ := budgetEnv(t, client)
	ga := &gateAnswer{reply: "y"}
	sess, err := New(Config{Task: "audit", Env: env, Answer: ga.answer})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = sess.Run(context.Background())
	if err == nil || !errors.Is(err, agent.ErrMaxSteps) {
		t.Fatalf("expected max-steps error, got %v", err)
	}
	if ga.calls != 0 {
		t.Fatal("gate must not ask when resume is impossible")
	}
}

func TestSession_BudgetGate_AnswerErrorDeclines(t *testing.T) {
	client := &seqClient{steps: deathScript()}
	env, dir := budgetEnv(t, client)
	ga := &gateAnswer{err: errors.New("no operator")}
	sess, err := New(Config{
		Task: "audit", Env: env, StateFile: stateFileIn(dir), Answer: ga.answer,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = sess.Run(context.Background())
	if err == nil || !errors.Is(err, agent.ErrMaxSteps) {
		t.Fatalf("expected original max-steps error, got %v", err)
	}
}

func TestSession_BudgetGate_SkipsWithoutOpenTodos(t *testing.T) {
	steps := make([]llm.ChatResponse, 0, 25)
	for i := 0; i < 25; i++ {
		steps = append(steps, budgetRead("r"))
	}
	client := &seqClient{steps: steps}
	env, dir := budgetEnv(t, client)
	ga := &gateAnswer{reply: "y"}
	sess, err := New(Config{
		Task: "audit", Env: env, StateFile: stateFileIn(dir), Answer: ga.answer,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = sess.Run(context.Background())
	if err == nil || !errors.Is(err, agent.ErrMaxSteps) {
		t.Fatalf("expected max-steps error, got %v", err)
	}
	if ga.calls != 0 {
		t.Fatal("gate must not ask with nothing acknowledged")
	}
}
