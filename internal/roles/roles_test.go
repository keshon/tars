package roles

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/keshon/tars/internal/agent"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

type stubClient struct{}

func (stubClient) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{}, nil
}

func testEnv(t *testing.T) Env {
	t.Helper()
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return Env{Client: stubClient{}, WS: ws, Procs: tools.NewBackgroundProcesses()}
}

func hasTool(a interface{ ToolNames() []string }, name string) bool {
	return slices.Contains(a.ToolNames(), name)
}

// The bug this package exists to prevent (2026-09-04): the eval harness
// built its agent by hand and ended up without ask_user or delegate_task,
// while the shipping CLI had both — so the instrument was scoring a
// different agent than the one under test, and two probes are
// specifically about those two tools.
func TestInteractive_HasTheToolsThatShip(t *testing.T) {
	a := Interactive(testEnv(t), "", "", func(string) (string, error) { return "", nil }, nil)
	for _, want := range []string{"ask_user", "delegate_task", "write_file", "run_shell"} {
		if !hasTool(a, want) {
			t.Errorf("interactive agent is missing %s — got %v", want, a.ToolNames())
		}
	}
}

// Passing no responder removes the tool rather than offering one that
// cannot be answered.
func TestInteractive_WithoutResponder_HasNoAskUser(t *testing.T) {
	a := Interactive(testEnv(t), "", "", nil, nil)
	if hasTool(a, "ask_user") {
		t.Error("ask_user offered with no responder to answer it")
	}
	if !hasTool(a, "delegate_task") {
		t.Error("delegate_task should not depend on the ask responder")
	}
}

// A subagent that could delegate could spawn subagents without bound, and
// one that could ask would block the process on a human watching the
// parent.
func TestSubagent_CannotDelegateOrAsk(t *testing.T) {
	a := Subagent(testEnv(t), "reviewer")
	for _, forbidden := range []string{"delegate_task", "ask_user"} {
		if hasTool(a, forbidden) {
			t.Errorf("subagent can call %s — got %v", forbidden, a.ToolNames())
		}
	}
	if !hasTool(a, "write_file") {
		t.Error("subagent should still have the working tool set")
	}
}

// Taking the mutating tools away beats asking a weak model not to use
// them, which is the whole reason inspectors get a different registry.
func TestInspector_CannotChangeAnything(t *testing.T) {
	a := Inspector(testEnv(t), "review", "sys", "")
	for _, forbidden := range []string{"write_file", "patch_file", "patch_lines", "move_file", "run_shell"} {
		if hasTool(a, forbidden) {
			t.Errorf("inspector can call %s — got %v", forbidden, a.ToolNames())
		}
	}
	if !hasTool(a, "read_file") {
		t.Error("inspector must still be able to read")
	}
}

func TestWorker_HasWorkingToolsButNoDelegation(t *testing.T) {
	a := Worker(testEnv(t), "s1", "sys", "", 15, true)
	if !hasTool(a, "write_file") {
		t.Error("worker must be able to write")
	}
	if hasTool(a, "delegate_task") {
		t.Error("a mission worker delegating means the plan already decomposed the task")
	}
}

// seqStub replays scripted responses in order.
type seqStub struct {
	calls int
	steps []llm.ChatResponse
}

func (s *seqStub) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	r := s.steps[s.calls]
	s.calls++
	return r, nil
}

func missingRead(id string) llm.ChatResponse {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: id, Name: "read_file", Arguments: json.RawMessage(`{"path":"missing.go"}`)}}}}
}

// A subagent's blow-by-blow must not leak into parent evidence: results,
// usage, and nudges stay inside the subagent even when they fire (two
// failing reads earn a stuck nudge here). The parent learns the outcome
// from the DELEGATE envelope, never the internals.
func TestSubagent_ObserverHooksStaySilent(t *testing.T) {
	var toolResults, usages, nudges int32
	e := testEnv(t)
	e.OnToolResult = func(string, string) { atomic.AddInt32(&toolResults, 1) }
	e.OnUsage = func(int, llm.Usage) { atomic.AddInt32(&usages, 1) }
	e.OnNudge = func(string, string) { atomic.AddInt32(&nudges, 1) }
	client := &seqStub{steps: []llm.ChatResponse{
		missingRead("c1"),
		missingRead("c2"),
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	e.Client = client
	sub := Subagent(e, "")

	out, err := sub.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	if toolResults != 0 || usages != 0 || nudges != 0 {
		t.Fatalf("subagent leaked observers: results=%d usage=%d nudges=%d",
			toolResults, usages, nudges)
	}
}

// A system text that names a tool promises that tool exists: every
// static tool-name token in a role's prompt must resolve in that role's
// registry, or a weak model emits calls to tools it was never given
// ("unknown tool", burned steps). Nudge and task texts are excluded on
// purpose — they are role-agnostic by design, and the loop conditions
// their tool-specific clauses on its own registry (DelegateHint,
// AskHint, WriteHint) instead of promising statically.
func TestRoleSystemToolsResolve(t *testing.T) {
	e := testEnv(t)
	universe := map[string]bool{"ask_user": true, "delegate_task": true}
	for _, n := range tools.Base(e.WS, e.Procs, nil).Names() {
		universe[n] = true
	}
	for _, n := range tools.ReadOnly(e.WS, e.Procs).Names() {
		universe[n] = true
	}
	var alts []string
	for n := range universe {
		alts = append(alts, regexp.QuoteMeta(n))
	}
	sort.Strings(alts)
	mentioned := regexp.MustCompile(`\b(` + strings.Join(alts, "|") + `)\b`)

	interactive := Interactive(e, "", "", func(string) (string, error) { return "", nil }, nil)
	subagent := Subagent(e, "tester")
	worker := Worker(e, "w", prompts.MissionWorker, "", 15, false)
	inspector := Inspector(e, "i", prompts.MissionMapAnnotate, "")
	planner := Planner(e, "p", "")
	// System texts as their roles actually ship them: the subagent
	// composition includes its scope correction (mirrors Subagent).
	subagentSystem := prompts.WithRole("tester") + "\n\n" + prompts.SubagentScope
	cases := []struct {
		role  string
		texts []string
		names []string
	}{
		{"interactive", []string{prompts.System, prompts.SystemForBackend("kobold"), prompts.SystemForBackend("openai")}, interactive.ToolNames()},
		{"subagent", []string{subagentSystem}, subagent.ToolNames()},
		{"worker", []string{prompts.MissionWorker}, worker.ToolNames()},
		{"inspector", []string{prompts.MissionMapAnnotate, prompts.MissionReview}, inspector.ToolNames()},
		{"planner", []string{prompts.PlanMode}, planner.ToolNames()},
	}
	for _, c := range cases {
		have := map[string]bool{}
		for _, n := range c.names {
			have[n] = true
		}
		for _, text := range c.texts {
			for _, m := range mentioned.FindAllString(text, -1) {
				// A withheld tool may be named only to forbid it
				// ("no <tool>"): a promise and a prohibition read the
				// same to a regex but opposite to a model.
				if !have[m] && !strings.Contains(text, "no "+m) {
					t.Errorf("%s prompt names %q, missing from its registry", c.role, m)
				}
			}
		}
	}
}

// textStub answers every turn with fixed text and no tool calls.
type textStub struct{ text string }

func (s textStub) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: s.text}}, nil
}

// -max-steps threads Env.MaxSteps into the Interactive agent; zero keeps
// the agent default. Roles with fixed budgets never consult it.
func TestInteractive_HonorsEnvMaxSteps(t *testing.T) {
	e := testEnv(t)
	e.Client = textStub{"done"}
	e.MaxSteps = 40
	a := Interactive(e, "", "", nil, nil)
	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a.Report().MaxSteps; got != 40 {
		t.Fatalf("Report().MaxSteps = %d, want 40", got)
	}

	e2 := testEnv(t)
	e2.Client = textStub{"done"}
	a2 := Interactive(e2, "", "", nil, nil)
	if _, err := a2.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a2.Report().MaxSteps; got != agent.DefaultMaxSteps {
		t.Fatalf("Report().MaxSteps = %d, want default %d", got, agent.DefaultMaxSteps)
	}
}
