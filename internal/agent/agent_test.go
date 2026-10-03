package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
)

// stubClient returns canned responses in sequence, one per Chat call.
type stubClient struct {
	responses   []llm.ChatResponse
	calls       int
	lastHistory []llm.Message
}

func (s *stubClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	s.lastHistory = req.Messages
	resp := s.responses[s.calls]
	s.calls++
	return resp, nil
}

func TestAgent_EmptyFinish_RetriesOnceBeforeReturning(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: ""}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "actual answer"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2 (empty finish retried once)", client.calls)
	}
	if out != "actual answer" {
		t.Fatalf("result = %q, want %q", out, "actual answer")
	}
}

func TestAgent_DelegateMutations_CountTowardVerifyZeroWrites(t *testing.T) {
	delegateArgs, _ := json.Marshal(map[string]string{"task": "write file"})
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "d", Name: "delegate_task", Arguments: delegateArgs},
		}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "delegated"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "confirmed"}},
	}}
	delegateTool := delegateStub{mutations: 1, result: "sub done"}
	a := New(Config{
		Client: client,
		Tools:  NewRegistry(delegateTool),
		System: "sys",
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range client.lastHistory {
		if strings.Contains(m.Content, "Concrete fact: 0 file-writing") {
			t.Fatalf("delegate mutations should prevent zero-writes fact: %q", m.Content)
		}
	}
}

type delegateStub struct {
	mutations int
	result    string
}

func (d delegateStub) Name() string            { return "delegate_task" }
func (d delegateStub) Description() string     { return "delegate" }
func (d delegateStub) Mode() ToolMode          { return Concurrent }
func (d delegateStub) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (d delegateStub) Run(context.Context, json.RawMessage) (string, error) {
	return fmt.Sprintf("DELEGATE\nmutations: %d\n----\n%s", d.mutations, d.result), nil
}

func TestParseDelegateMutations(t *testing.T) {
	got := parseDelegateMutations("DELEGATE\nmutations: 2\n----\nsub result")
	if got != 2 {
		t.Fatalf("parseDelegateMutations = %d, want 2", got)
	}
	if parseDelegateMutations("plain text") != 0 {
		t.Fatal("expected 0 for unstructured content")
	}
}

func TestAgent_VerifyOnFinish_AddsOneRoundTrip(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "looks done"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "confirmed correct"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys"})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2 (initial answer + verify pass)", client.calls)
	}
	if out != "confirmed correct" {
		t.Fatalf("result = %q, want %q", out, "confirmed correct")
	}
}

func TestAgent_SkipVerify_ReturnsImmediately(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("calls = %d, want 1", client.calls)
	}
	if out != "done" {
		t.Fatalf("result = %q, want %q", out, "done")
	}
}

func TestAgent_VerifyOnFinish_OnlyHappensOnce(t *testing.T) {
	// Even if the model keeps stalling with empty tool-call responses
	// after the verify nudge, we must not loop forever asking it to verify
	// again and again — verifiedOnce should gate this to a single pass.
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "looks done"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "still looks done"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys"})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want exactly 2", client.calls)
	}
	if out != "still looks done" {
		t.Fatalf("result = %q, want %q", out, "still looks done")
	}
}

func TestAgent_RepeatedIdenticalCall_TriggersNudgeEvenWithoutErrors(t *testing.T) {
	// Reproduces the real failure: list_files(".") called identically many
	// times, every call succeeding (no error) — which "did it error or
	// not" alone treats as fine. After MaxStuckSteps identical repeats, a
	// repeat-specific nudge must land in the conversation.
	listArgs := json.RawMessage(`{"path": "."}`)
	client := &repeatingClient{
		toolCall:     llm.ToolCall{ID: "c", Name: "echo", Arguments: listArgs},
		maxRepeats:   5, // keep repeating for this many requests, then finish
		finalContent: "done",
	}
	a := New(Config{
		Client:        client,
		Tools:         NewRegistry(echoToolStub{}),
		System:        "sys",
		MaxStuckSteps: 2,
		SkipVerify:    true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	nudges := 0
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "exact same") {
			nudges++
		}
	}
	if nudges == 0 {
		t.Fatal("expected at least one repeat-specific nudge in history, got none")
	}
}

func TestAgent_DistinctSuccessfulCalls_NeverNudged(t *testing.T) {
	// Control case: genuinely different successful calls each step must
	// never be mistaken for a stuck loop.
	client := &varyingArgsClient{steps: 5, finalContent: "done"}
	a := New(Config{
		Client:        client,
		Tools:         NewRegistry(echoToolStub{}),
		System:        "sys",
		MaxStuckSteps: 2,
		SkipVerify:    true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("result = %q, want %q", out, "done")
	}
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "Stop repeating") {
			t.Fatalf("unexpected nudge for genuinely distinct calls: %q", m.Content)
		}
	}
}

// echoToolStub is a no-op tool that always succeeds — stands in for
// list_files's "never errors, just returns the same thing" behavior.
type echoToolStub struct{ name string }

func (e echoToolStub) Name() string {
	if e.name == "" {
		return "echo"
	}
	return e.name
}
func (echoToolStub) Description() string     { return "echo" }
func (echoToolStub) Mode() ToolMode          { return Concurrent }
func (echoToolStub) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (echoToolStub) Run(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// repeatingClient returns the same tool call for a fixed number of
// requests, then a final no-tool-call answer. It records the history it
// was given on its last call so the test can inspect what got nudged in.
type repeatingClient struct {
	toolCall     llm.ToolCall
	maxRepeats   int
	finalContent string
	calls        int
	lastHistory  []llm.Message
}

func (c *repeatingClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.lastHistory = req.Messages
	c.calls++
	if c.calls > c.maxRepeats {
		return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: c.finalContent}}, nil
	}
	return llm.ChatResponse{Message: llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{c.toolCall},
	}}, nil
}

// varyingArgsClient issues a different tool call (different args) on
// every step, then finishes — confirms distinct calls are never flagged
// as a repeat.
type varyingArgsClient struct {
	steps        int
	finalContent string
	calls        int
	lastHistory  []llm.Message
}

func (c *varyingArgsClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.lastHistory = req.Messages
	c.calls++
	if c.calls > c.steps {
		return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: c.finalContent}}, nil
	}
	args, _ := json.Marshal(map[string]int{"n": c.calls})
	return llm.ChatResponse{Message: llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "c", Name: "echo", Arguments: args}},
	}}, nil
}

func TestAgent_BudgetWarning_FiresOncePerThreshold(t *testing.T) {
	// Usage climbs past 75% then 90% of a 1000-token ContextLimit across
	// three steps, then stays high. Each threshold should produce exactly
	// one nudge, not one per step once crossed.
	client := &usageClient{
		usages:       []int{600, 800, 950, 960},
		finalContent: "done",
	}
	a := New(Config{
		Client:       client,
		Tools:        NewRegistry(echoToolStub{}),
		System:       "sys",
		ContextLimit: 1000,
		SkipVerify:   true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	notices, warnings := 0, 0
	for _, m := range client.lastHistory {
		if m.Role != llm.RoleUser {
			continue
		}
		if strings.Contains(m.Content, "Context budget notice") {
			notices++
		}
		if strings.Contains(m.Content, "Context budget warning") {
			warnings++
		}
	}
	if notices != 1 {
		t.Fatalf("75%% notice fired %d times, want exactly 1", notices)
	}
	if warnings != 1 {
		t.Fatalf("90%% warning fired %d times, want exactly 1", warnings)
	}
}

func TestAgent_BudgetWarning_DisabledByDefault(t *testing.T) {
	client := &usageClient{usages: []int{999999}, finalContent: "done"}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(echoToolStub{}),
		System:     "sys",
		SkipVerify: true, // ContextLimit left at zero: tracking disabled
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range client.lastHistory {
		if strings.Contains(m.Content, "Context budget") {
			t.Fatalf("unexpected budget nudge with ContextLimit unset: %q", m.Content)
		}
	}
}

func TestAgent_ParallelToolCalls_PreserveOrderRegardlessOfFinishTime(t *testing.T) {
	// Two calls in one step; the FIRST one issued sleeps longer than the
	// second. If execution were sequential, the whole step would take at
	// least the sum of both delays; if parallel, roughly the max of them.
	// Delays are large enough (hundreds of ms) that runner scheduling
	// noise stays far below the 50ms gap between parallel (~400ms) and
	// sequential (~450ms): a 45ms total once failed on a loaded Windows
	// runner with 52ms of pure scheduling overhead and no signal at all.
	slow := slowToolStub{name: "slow_a", delay: 400 * time.Millisecond}
	fast := slowToolStub{name: "slow_b", delay: 50 * time.Millisecond}

	client := &twoCallClient{
		callA:        llm.ToolCall{ID: "call_a", Name: "slow_a", Arguments: json.RawMessage(`{}`)},
		callB:        llm.ToolCall{ID: "call_b", Name: "slow_b", Arguments: json.RawMessage(`{}`)},
		finalContent: "done",
	}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(slow, fast),
		System:     "sys",
		SkipVerify: true,
	})

	start := time.Now()
	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed >= slow.delay+fast.delay {
		t.Fatalf("took %v, expected well under the sum of delays (%v) if calls ran in parallel",
			elapsed, slow.delay+fast.delay)
	}

	// Order in history must match call order (call_a's result before
	// call_b's), even though call_b's tool finishes first.
	var order []string
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleTool {
			order = append(order, m.ToolCallID)
		}
	}
	if len(order) != 2 || order[0] != "call_a" || order[1] != "call_b" {
		t.Fatalf("tool result order = %v, want [call_a call_b]", order)
	}
}

// usageClient returns escalating Usage.PromptTokens values, one per call,
// then a final no-tool-call answer once the list is exhausted.
type usageClient struct {
	usages       []int
	finalContent string
	calls        int
	lastHistory  []llm.Message
}

func (c *usageClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.lastHistory = req.Messages
	if c.calls >= len(c.usages) {
		return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: c.finalContent}}, nil
	}
	usage := c.usages[c.calls]
	c.calls++
	args, _ := json.Marshal(map[string]int{"n": c.calls})
	return llm.ChatResponse{
		Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c", Name: "echo", Arguments: args}}},
		Usage:   llm.Usage{PromptTokens: usage},
	}, nil
}

// slowToolStub sleeps for delay then succeeds — used to prove concurrent
// execution of multiple tool calls within one step.
type slowToolStub struct {
	name  string
	delay time.Duration
}

func (s slowToolStub) Name() string            { return s.name }
func (s slowToolStub) Description() string     { return "slow" }
func (s slowToolStub) Mode() ToolMode          { return Concurrent }
func (s slowToolStub) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (s slowToolStub) Run(_ context.Context, _ json.RawMessage) (string, error) {
	time.Sleep(s.delay)
	return "ok", nil
}

// twoCallClient issues exactly two tool calls in its first response, then
// finishes on the second call.
type twoCallClient struct {
	callA, callB llm.ToolCall
	finalContent string
	calls        int
	lastHistory  []llm.Message
	mu           sync.Mutex
}

func (c *twoCallClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastHistory = req.Messages
	c.calls++
	if c.calls == 1 {
		return llm.ChatResponse{Message: llm.Message{
			Role:      llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{c.callA, c.callB},
		}}, nil
	}
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: c.finalContent}}, nil
}

func TestAgent_StateFile_SavedAndLoadable(t *testing.T) {
	dir := t.TempDir()
	statePath := dir + "/state.json"
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(),
		System:     "sys",
		SkipVerify: true,
		StateFile:  statePath,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	history, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("loaded history is empty")
	}
	if history[len(history)-1].Content != "done" {
		t.Fatalf("last message content = %q, want %q", history[len(history)-1].Content, "done")
	}
}

func TestAgent_Resume_ContinuesFromLoadedHistory(t *testing.T) {
	saved := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "original task"},
		{Role: llm.RoleAssistant, Content: "partial progress"},
	}
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "finished now"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true})

	out, err := a.Resume(context.Background(), saved, "continue please")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out != "finished now" {
		t.Fatalf("result = %q, want %q", out, "finished now")
	}

	found := false
	for _, m := range client.lastHistory {
		if m.Content == "continue please" {
			found = true
		}
	}
	if !found {
		t.Fatal("resume note not found in sent history")
	}
	if client.lastHistory[1].Content != "original task" {
		t.Fatal("original task message lost on resume")
	}
}

func TestEffectiveMaxTokens_CapsAgainstRemainingRoom(t *testing.T) {
	a := New(Config{
		Client:       &stubClient{},
		Tools:        NewRegistry(),
		ContextLimit: 32768,
		MaxTokens:    32768,
	})
	// prompt already used 30000 of 32768 — only ~2512 actually free.
	got := a.effectiveMaxTokens(30000)
	if got >= 32768 {
		t.Fatalf("effectiveMaxTokens = %d, want it capped well below MaxTokens", got)
	}
	if got < 256 {
		t.Fatalf("effectiveMaxTokens = %d, want at least the floor of 256", got)
	}
}

func TestEffectiveMaxTokens_NoLimitKnown_FallsBackToConfig(t *testing.T) {
	a := New(Config{Client: &stubClient{}, Tools: NewRegistry(), MaxTokens: 4096})
	if got := a.effectiveMaxTokens(30000); got != 4096 {
		t.Fatalf("effectiveMaxTokens = %d, want 4096 (ContextLimit unset)", got)
	}
}

func TestEffectiveMaxTokens_FirstCall_StillCapsAgainstContext(t *testing.T) {
	// ContextLimit known but no prompt measured yet (first call) — must
	// not blindly return the full ceiling if the ceiling is close to or
	// equal to the context size, since the unmeasured prompt isn't zero.
	a := New(Config{
		Client:       &stubClient{},
		Tools:        NewRegistry(),
		ContextLimit: 32768,
		MaxTokens:    32768,
	})
	got := a.effectiveMaxTokens(0)
	if got >= 32768 {
		t.Fatalf("effectiveMaxTokens = %d, want it capped below the full ceiling on an unmeasured first call", got)
	}
}

func TestAgent_LeakedToolCallText_IsNotTrustedAsFinish(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant,
			Content: "Here's the file.\n<|tool_call>call:write_file{path: \"x.txt\"}<tool_call|>"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done for real"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done for real" {
		t.Fatalf("result = %q, want %q", out, "done for real")
	}

	found := false
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "looks like an attempted tool call") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a corrective nudge about the leaked tool-call text")
	}
}

func TestAgent_GemmaStyleLeakedToolCall_IsNotTrustedAsFinish(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: `(Made a function call call_92023 to read_file with arguments={"path": "sample.go"})`}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done for real"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done for real" {
		t.Fatalf("result = %q, want %q", out, "done for real")
	}
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "looks like an attempted tool call") {
			return
		}
	}
	t.Fatal("expected leak nudge for Gemma-style pseudo tool-call text")
}

// A leaked call naming a real tool with parseable args executes instead
// of costing a nudge round-trip: recovery, not refusal.
func TestAgent_LeakedCallRecoversAndExecutes(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: `(Made a function call call_1 to echo with arguments={})`}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(echoToolStub{}), System: "sys", SkipVerify: true})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("result = %q, want done", out)
	}
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleTool && m.Content == "ok" {
			return
		}
	}
	t.Fatal("expected the recovered call to execute and record a tool result")
}

func TestAgent_OnToolResultSeesEveryCall(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)},
			{ID: "c2", Name: "echo", Arguments: json.RawMessage(`{}`)},
		}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	var mu sync.Mutex
	var got []string
	a := New(Config{Client: client, Tools: NewRegistry(echoToolStub{}), System: "sys",
		SkipVerify: true,
		OnToolResult: func(callID, result string) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, callID+":"+result)
		},
	})
	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Concurrent calls complete in goroutine order, so compare as a set.
	mu.Lock()
	defer mu.Unlock()
	sort.Strings(got)
	if len(got) != 2 || got[0] != "c1:ok" || got[1] != "c2:ok" {
		t.Fatalf("results = %v", got)
	}
}

func TestAgent_OnUsageReportsCounts(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"},
			Usage: llm.Usage{PromptTokens: 1500, CompletionTokens: 20, CachedTokens: 300}},
	}}
	var got []llm.Usage
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys",
		SkipVerify: true,
		OnUsage: func(step int, usage llm.Usage) {
			got = append(got, usage)
		},
	})
	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 || got[0].PromptTokens != 1500 || got[0].CachedTokens != 300 {
		t.Fatalf("usage = %+v", got)
	}
}

func TestAgent_ThinkOverBudgetWrapsUp(t *testing.T) {
	ramble := "<think>" + strings.Repeat("considering options. ", 400) + "</think>stuck deliberating"
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: ramble}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "decided"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "decided" {
		t.Fatalf("result = %q, want decided", out)
	}
	wraps := 0
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "Reasoning budget exceeded") {
			wraps++
		}
	}
	if wraps != 1 {
		t.Fatalf("wrap-ups = %d, want 1", wraps)
	}
}

func TestAgent_ReasoningFieldWrapsUp(t *testing.T) {
	// llama.cpp reports deliberation out-of-band in reasoning_content,
	// with no <think> tags in content. Measured live: Qwen deliberates
	// ~100-500 chars on trivial tasks, so 8000 chars here is firmly
	// over any sane budget.
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant,
			Content:   "working on it",
			Reasoning: strings.Repeat("deliberating. ", 500)}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "decided"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "decided" {
		t.Fatalf("result = %q, want decided", out)
	}
	wraps := 0
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "Reasoning budget exceeded") {
			wraps++
		}
	}
	if wraps != 1 {
		t.Fatalf("wrap-ups = %d, want 1", wraps)
	}
}

func TestAgent_ThinkWrapsBounded(t *testing.T) {
	ramble := "<think>" + strings.Repeat("still thinking. ", 400) + "</think>more deliberation"
	responses := make([]llm.ChatResponse, 0, 5)
	for i := 0; i < 5; i++ {
		responses = append(responses, llm.ChatResponse{
			Message: llm.Message{Role: llm.RoleAssistant, Content: ramble},
		})
	}
	client := &stubClient{responses: responses}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true,
		MaxThinkWraps: 2, MaxSteps: 10})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out == "" {
		t.Fatal("run must degrade to finishing, not error")
	}
	wraps := 0
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "Reasoning budget exceeded") {
			wraps++
		}
	}
	if wraps != 2 {
		t.Fatalf("wrap-ups = %d, want exactly MaxThinkWraps (2)", wraps)
	}
}

func TestAgent_ThinkUnderBudgetFinishes(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "<think>brief</think>done"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "<think>brief</think>done" {
		t.Fatalf("result = %q", out)
	}
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "Reasoning budget exceeded") {
			t.Fatal("no wrap-up should fire under budget")
		}
	}
}

func TestAgent_ThinkBudgetDisabled(t *testing.T) {
	ramble := "<think>" + strings.Repeat("x. ", 4000) + "</think>done anyway"
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: ramble}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys",
		SkipVerify: true, ReasoningBudget: -1})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != ramble {
		t.Fatalf("disabled budget must finish as-is")
	}
}

func TestAgent_ThinkWithToolCallsNotWrapped(t *testing.T) {
	// Acting is progress: a thinking model that also calls tools is
	// converging, not rambling. Only finishes wrap.
	ramble := "<think>" + strings.Repeat("thinking hard. ", 400) + "</think>acting"
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: ramble,
			ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)},
			},
		}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(echoToolStub{}), System: "sys", SkipVerify: true})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("result = %q, want done", out)
	}
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "Reasoning budget exceeded") {
			t.Fatal("acting steps must not wrap")
		}
	}
}

func TestAgent_VerifyMessage_StatesZeroWritesAsFact(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "I created the file."}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "confirmed"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys"}) // SkipVerify left false

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	found := false
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "Concrete fact: 0 file-writing") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the verify message to state the zero-writes fact when no mutating tool ran")
	}
}

func TestAgent_VerifyMessage_OmitsFactWhenAWriteSucceeded(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"path": "x.txt", "content": "hi"})
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{ID: "c", Name: "write_file", Arguments: args}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "I created the file."}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "confirmed"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(echoToolStub{name: "write_file"}), System: "sys"})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, m := range client.lastHistory {
		if strings.Contains(m.Content, "Concrete fact: 0 file-writing") {
			t.Fatalf("unexpected zero-writes fact when write_file actually succeeded: %q", m.Content)
		}
	}
}

func TestAgent_VerifyHook_OutputFoldedIntoVerifyMessage(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done with the code change"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "confirmed"}},
	}}
	a := New(Config{
		Client: client,
		Tools:  NewRegistry(),
		System: "sys",
		Verify: func(ctx context.Context) (string, bool) {
			return "PASSED\n(no issues)", true
		},
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	found := false
	for _, m := range client.lastHistory {
		if strings.Contains(m.Content, "Build/test check result: PASSED") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected Verify output to appear in the verify message")
	}
}

func TestAgent_VerifyFailed_BlocksPrematureFinish(t *testing.T) {
	verifyCalls := 0
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done with the code change"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "confirmed correct"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "fixed now"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "all good"}},
	}}
	a := New(Config{
		Client: client,
		Tools:  NewRegistry(),
		System: "sys",
		Verify: func(ctx context.Context) (string, bool) {
			verifyCalls++
			if verifyCalls == 1 {
				return "FAILED\nundefined: foo", true
			}
			return "PASSED\n", true
		},
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.calls < 3 {
		t.Fatalf("calls = %d, want at least 3 (finish blocked after FAILED verify)", client.calls)
	}
	if out != "all good" {
		t.Fatalf("result = %q, want %q", out, "all good")
	}

	found := false
	for _, m := range client.lastHistory {
		if strings.Contains(m.Content, "Fix the issues") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected verify-failed continuation message in history")
	}
}

func TestAgent_CompactHistoryAt90Percent(t *testing.T) {
	client := &usageClient{
		usages:       []int{950, 960},
		finalContent: "done",
	}
	a := New(Config{
		Client:           client,
		Tools:            NewRegistry(echoToolStub{}),
		System:           "sys",
		ContextLimit:     1000,
		CompactKeepSteps: 2,
		SkipVerify:       true,
	})

	// Seed history with many prior steps so compaction has something to drop.
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
	}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("c%d", i)
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "echo", Arguments: json.RawMessage(`{}`)}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: "ok"},
		)
	}

	if _, err := a.Resume(context.Background(), history, "continue"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	found := false
	for _, m := range client.lastHistory {
		if len(m.Content) >= len(prompts.CompactNotice) && m.Content[:len(prompts.CompactNotice)] == prompts.CompactNotice {
			found = true
		}
	}
	if !found {
		t.Fatal("expected compact notice after crossing 90% context usage")
	}
}

// askUserStub blocks until the test supplies an answer via the channel.
type askUserStub struct {
	questionCh chan string
	answer     string
}

func (a askUserStub) Name() string          { return "ask_user" }
func (askUserStub) Description() string     { return "ask" }
func (askUserStub) Mode() ToolMode          { return Exclusive }
func (askUserStub) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (a askUserStub) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal(args, &in)
	if a.questionCh != nil {
		a.questionCh <- in.Question
	}
	return a.answer, nil
}

func TestAgent_AskUser_PausesWithUnansweredCall(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"question": "which db?"})
	questionCh := make(chan string, 1)
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "q1", Name: "ask_user", Arguments: args},
		}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "using sqlite"}},
	}}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(askUserStub{questionCh: questionCh, answer: "sqlite"}),
		System:     "sys",
		SkipVerify: true,
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := a.Run(context.Background(), "build app"); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()

	select {
	case q := <-questionCh:
		if q != "which db?" {
			t.Errorf("question = %q", q)
		}
	case <-time.After(time.Second):
		t.Fatal("ask_user never invoked")
	}
	<-done

	if callID, _, ok := PausedOnQuestion(client.lastHistory[:len(client.lastHistory)-1]); ok {
		_ = callID // history mid-run had unanswered call before tool result appended
	}
}

func TestAgent_VerifyHook_SkippedWhenNotOK(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "confirmed"}},
	}}
	a := New(Config{
		Client: client,
		Tools:  NewRegistry(),
		System: "sys",
		Verify: func(ctx context.Context) (string, bool) { return "", false },
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range client.lastHistory {
		if strings.Contains(m.Content, "Build/test check") {
			t.Fatalf("unexpected verify output when Verify reported ok=false: %q", m.Content)
		}
	}
}

// orderTrackingExclusiveStub is Exclusive and records the order it ran in
// relative to other instances sharing the same *[]string log — used to
// prove Exclusive calls never overlap each other.
type orderTrackingExclusiveStub struct {
	name  string
	delay time.Duration
	log   *[]string
	mu    *sync.Mutex
}

func (s orderTrackingExclusiveStub) Name() string            { return s.name }
func (s orderTrackingExclusiveStub) Description() string     { return "exclusive" }
func (s orderTrackingExclusiveStub) Mode() ToolMode          { return Exclusive }
func (s orderTrackingExclusiveStub) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (s orderTrackingExclusiveStub) Run(_ context.Context, _ json.RawMessage) (string, error) {
	s.mu.Lock()
	*s.log = append(*s.log, "start:"+s.name)
	s.mu.Unlock()
	time.Sleep(s.delay)
	s.mu.Lock()
	*s.log = append(*s.log, "end:"+s.name)
	s.mu.Unlock()
	return "ok", nil
}

func TestAgent_ExclusiveToolCalls_NeverOverlap(t *testing.T) {
	var log []string
	var mu sync.Mutex
	a1 := orderTrackingExclusiveStub{name: "a", delay: 30 * time.Millisecond, log: &log, mu: &mu}
	a2 := orderTrackingExclusiveStub{name: "b", delay: 30 * time.Millisecond, log: &log, mu: &mu}

	args, _ := json.Marshal(map[string]any{})
	client := &twoCallClient{
		callA:        llm.ToolCall{ID: "1", Name: "a", Arguments: args},
		callB:        llm.ToolCall{ID: "2", Name: "b", Arguments: args},
		finalContent: "done",
	}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(a1, a2),
		System:     "sys",
		SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// If they overlapped, we'd see start:a, start:b, end:a, end:b (or
	// similar interleaving). Strictly sequential means each end comes
	// immediately after its own start, with no other start in between.
	if len(log) != 4 {
		t.Fatalf("expected 4 log entries, got %d: %v", len(log), log)
	}
	if !((log[0] == "start:a" && log[1] == "end:a" && log[2] == "start:b" && log[3] == "end:b") ||
		(log[0] == "start:b" && log[1] == "end:b" && log[2] == "start:a" && log[3] == "end:a")) {
		t.Fatalf("exclusive calls overlapped, got order: %v", log)
	}
}

func TestAgent_MixedConcurrentAndExclusive_ExclusiveRunsAfterConcurrentBatch(t *testing.T) {
	var log []string
	var mu sync.Mutex
	concurrentTool := slowToolStub{name: "fast_read", delay: 10 * time.Millisecond}
	exclusiveTool := orderTrackingExclusiveStub{name: "write", delay: 10 * time.Millisecond, log: &log, mu: &mu}

	args, _ := json.Marshal(map[string]any{})
	client := &twoCallClient{
		callA:        llm.ToolCall{ID: "1", Name: "fast_read", Arguments: args},
		callB:        llm.ToolCall{ID: "2", Name: "write", Arguments: args},
		finalContent: "done",
	}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(concurrentTool, exclusiveTool),
		System:     "sys",
		SkipVerify: true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("result = %q, want %q", out, "done")
	}
	// The Exclusive tool must have actually run (both log entries present).
	if len(log) != 2 || log[0] != "start:write" || log[1] != "end:write" {
		t.Fatalf("expected exclusive tool to run cleanly, got log: %v", log)
	}
}

func TestAgent_ToolLoop_FiresWhenSameToolRepeatsWithTweakedArgs(t *testing.T) {
	client := &shellLoopClient{steps: 4, finalContent: "done"}
	a := New(Config{
		Client:     client,
		Tools:      NewRegistry(echoToolStub{name: "run_shell"}),
		System:     "sys",
		SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	fired := 0
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "same tool several steps") {
			fired++
		}
	}
	if fired != 1 {
		t.Fatalf("tool loop nudge fired %d times, want exactly 1", fired)
	}
}

func TestAgent_SearchFatigue_FiresOnceAfterManyExploratoryStepsWithoutMutation(t *testing.T) {
	// Reproduces the real failure: many varying grep_files/list_files
	// calls in a row, none of them errors, none of them exact repeats —
	// so neither "progressed" nor exact-repeat detection ever fires —
	// but also no mutating tool ever succeeds. The model is searching,
	// not converging.
	client := &exploringClient{steps: 10, finalContent: "done"}
	a := New(Config{
		Client:              client,
		Tools:               NewRegistry(echoToolStub{name: "grep_files"}),
		System:              "sys",
		MaxExploratorySteps: 4,
		SkipVerify:          true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	fired := 0
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "spent several steps searching") {
			fired++
		}
	}
	if fired != 1 {
		t.Fatalf("search fatigue nudge fired %d times, want exactly 1", fired)
	}
}

func TestAgent_SearchFatigue_ResetsOnMutatingSuccess(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"path": "x.txt", "content": "hi"})
	// 3 exploratory calls, then a successful write, then 3 more
	// exploratory calls — should never hit the threshold of 4 since the
	// write resets the counter partway through.
	client := &mixedExploreWriteClient{
		exploreBefore: 3,
		exploreAfter:  3,
		writeArgs:     args,
		finalContent:  "done",
	}
	a := New(Config{
		Client: client,
		Tools: NewRegistry(
			echoToolStub{name: "grep_files"},
			echoToolStub{name: "write_file"},
		),
		System:              "sys",
		MaxExploratorySteps: 4,
		SkipVerify:          true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, m := range client.lastHistory {
		if strings.Contains(m.Content, "spent several steps searching") {
			t.Fatalf("unexpected search fatigue nudge when a mutation reset the counter: %q", m.Content)
		}
	}
}

// shellLoopClient issues a run_shell call with varying arguments every
// step for `steps` steps, then finishes — simulates git log tweak loops.
type shellLoopClient struct {
	steps        int
	finalContent string
	calls        int
	lastHistory  []llm.Message
}

func (c *shellLoopClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.lastHistory = req.Messages
	if c.calls >= c.steps {
		return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: c.finalContent}}, nil
	}
	c.calls++
	args, _ := json.Marshal(map[string]string{"command": fmt.Sprintf("git log tweak %d", c.calls)})
	return llm.ChatResponse{Message: llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "c", Name: "run_shell", Arguments: args}},
	}}, nil
}

// exploringClient issues a grep_files call with varying arguments every
// step (never an exact repeat, never an error) for `steps` steps, then
// finishes — simulates a model searching without converging.
type exploringClient struct {
	steps        int
	finalContent string
	calls        int
	lastHistory  []llm.Message
}

func (c *exploringClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.lastHistory = req.Messages
	if c.calls >= c.steps {
		return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: c.finalContent}}, nil
	}
	c.calls++
	args, _ := json.Marshal(map[string]int{"n": c.calls})
	return llm.ChatResponse{Message: llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "c", Name: "grep_files", Arguments: args}},
	}}, nil
}

// mixedExploreWriteClient issues exploreBefore varying grep calls, one
// write_file call, then exploreAfter more varying grep calls, then
// finishes.
type mixedExploreWriteClient struct {
	exploreBefore, exploreAfter int
	writeArgs                   json.RawMessage
	finalContent                string
	calls                       int
	lastHistory                 []llm.Message
}

func (c *mixedExploreWriteClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.lastHistory = req.Messages
	total := c.exploreBefore + 1 + c.exploreAfter
	if c.calls >= total {
		return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: c.finalContent}}, nil
	}
	i := c.calls
	c.calls++
	if i == c.exploreBefore {
		return llm.ChatResponse{Message: llm.Message{
			Role:      llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file", Arguments: c.writeArgs}},
		}}, nil
	}
	args, _ := json.Marshal(map[string]int{"n": i})
	return llm.ChatResponse{Message: llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "c", Name: "grep_files", Arguments: args}},
	}}, nil
}

func TestPausedOnQuestion_DetectsAskUserCall(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"question": "which database?"})
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "do the thing"},
		{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{
			{ID: "call_q1", Name: "ask_user", Arguments: args},
		}},
	}

	callID, question, ok := PausedOnQuestion(history)
	if !ok {
		t.Fatal("PausedOnQuestion returned false for a history ending with ask_user")
	}
	if callID != "call_q1" {
		t.Fatalf("callID = %q, want %q", callID, "call_q1")
	}
	if question != "which database?" {
		t.Fatalf("question = %q, want %q", question, "which database?")
	}
}

func TestPausedOnQuestion_ReturnsFalseForNormalHistory(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "I wrote the file."},
	}
	_, _, ok := PausedOnQuestion(history)
	if ok {
		t.Fatal("PausedOnQuestion returned true for a normal text response")
	}
}

func TestPausedOnQuestion_ReturnsFalseForEmptyHistory(t *testing.T) {
	_, _, ok := PausedOnQuestion(nil)
	if ok {
		t.Fatal("PausedOnQuestion returned true for empty history")
	}
}

func TestResumeWithAnswer_SuppliesWireCorrectToolResult(t *testing.T) {
	// Reproduces the "wire-invalid conversation" problem that plain Resume
	// would cause: the answer must arrive as role:tool with the matching
	// tool_call_id, not as a naked role:user message.
	args, _ := json.Marshal(map[string]string{"question": "which database?"})
	pausedHistory := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "build a thing"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "call_q1", Name: "ask_user", Arguments: args},
		}},
	}
	client := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok, using sqlite"}},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true})

	out, err := a.ResumeWithAnswer(context.Background(), pausedHistory, "call_q1", "sqlite please")
	if err != nil {
		t.Fatalf("ResumeWithAnswer: %v", err)
	}
	if out != "ok, using sqlite" {
		t.Fatalf("result = %q, want %q", out, "ok, using sqlite")
	}

	// The history the model received must contain role:tool, not role:user,
	// for the answer.
	var foundToolResult bool
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleTool && m.ToolCallID == "call_q1" && m.Content == "sqlite please" {
			foundToolResult = true
		}
		if m.Role == llm.RoleUser && m.Content == "sqlite please" {
			t.Fatal("answer was sent as role:user instead of role:tool — wire-invalid")
		}
	}
	if !foundToolResult {
		t.Fatal("no role:tool message with the answer found in sent history")
	}
}
