package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/permission"
)

type auditTool struct{ ran *bool }

func (auditTool) Name() string            { return "audit_tool" }
func (auditTool) Description() string     { return "audit" }
func (auditTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (auditTool) Mode() ToolMode          { return Exclusive }
func (t auditTool) Run(context.Context, json.RawMessage) (string, error) {
	*t.ran = true
	return "ok", nil
}

func TestPolicyCannotBeBypassed(t *testing.T) {
	for _, hook := range []bool{false, true} {
		ran := false
		cfg := Config{Tools: NewRegistry(auditTool{&ran}), Policy: permission.Policy{Rules: []permission.Rule{{Tool: "*", Pattern: "*", Effect: permission.Deny}}}}
		if hook {
			cfg.BeforeToolCall = func(context.Context, string, string, json.RawMessage) (permission.Effect, error) {
				return permission.Allow, nil
			}
		}
		a := New(cfg)
		a.executeToolCalls(context.Background(), &runState{}, 0, "stop", []llm.ToolCall{{ID: "x", Name: "audit_tool", Arguments: json.RawMessage(`{}`)}}, nil)
		if ran {
			t.Fatal("denied tool executed")
		}

	}
}

func TestFalseCompletionIsRejected(t *testing.T) {
	c := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "I created notes.txt"}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "I created notes.txt"}},
	}}
	c.responses = append(c.responses, llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "incomplete"}})
	a := New(Config{Client: c, Tools: NewRegistry(), MaxSteps: 2})
	out, err := a.Run(context.Background(), "Create notes.txt")
	if err == nil || out != "" || a.LastRunMutations != 0 {
		t.Fatalf("repro failed %q %v", out, err)
	}

}

func TestVerificationAfterLastMutation(t *testing.T) {
	ran := false
	checks := 0
	c := &stubClient{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "1", Name: "audit_tool", Arguments: json.RawMessage(`{}`)}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "2", Name: "audit_tool", Arguments: json.RawMessage(`{"edit":2}`)}}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	c.responses = append(c.responses, llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "confirmed"}})
	a := New(Config{Client: c, Tools: NewRegistry(auditTool{&ran}), MutatingTools: []string{"audit_tool"}, Verify: func(context.Context) (string, bool) { checks++; return "PASSED", true }})
	_, err := a.Run(context.Background(), "edit and verify")
	if err != nil || checks != 2 || a.LastRunMutations != 2 {
		t.Fatalf("repro failed checks=%d mutations=%d err=%v", checks, a.LastRunMutations, err)
	}

}

func TestAskWithoutGateDenies(t *testing.T) {
	ran := false
	a := New(Config{Tools: NewRegistry(auditTool{&ran}), Policy: permission.Policy{Rules: []permission.Rule{{Tool: "*", Pattern: "*", Effect: permission.Ask}}}})
	a.executeToolCalls(context.Background(), &runState{}, 0, "stop", []llm.ToolCall{{ID: "1", Name: "audit_tool", Arguments: json.RawMessage(`{}`)}}, nil)
	if ran {
		t.Fatal("Ask executed without a gate")
	}
}

type orderedTool struct {
	name  string
	mode  ToolMode
	order *[]string
}

func (t orderedTool) Name() string            { return t.name }
func (t orderedTool) Description() string     { return t.name }
func (t orderedTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t orderedTool) Mode() ToolMode          { return t.mode }
func (t orderedTool) Run(context.Context, json.RawMessage) (string, error) {
	*t.order = append(*t.order, t.name)
	return "ok", nil
}
func TestMutationIsBarrierInModelOrder(t *testing.T) {
	var order []string
	a := New(Config{Tools: NewRegistry(orderedTool{"before", Concurrent, &order}, orderedTool{"write", Exclusive, &order}, orderedTool{"after", Concurrent, &order})})
	var calls []llm.ToolCall
	for _, name := range []string{"before", "write", "after"} {
		calls = append(calls, llm.ToolCall{ID: name, Name: name, Arguments: json.RawMessage(`{}`)})
	}
	a.executeToolCalls(context.Background(), &runState{}, 0, "stop", calls, nil)
	if strings.Join(order, ",") != "before,write,after" {
		t.Fatalf("order=%v", order)
	}
}
