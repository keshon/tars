// Package llm defines the wire-level contract between the agent loop and a
// chat-completion backend. Everything backend-specific (quirky JSON,
// missing fields, weak-model workarounds) lives inside a concrete Client
// implementation, never here and never in the agent loop.
package llm

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a single function call the model wants to make.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Message is one turn in the conversation. ToolCalls is only set on
// assistant messages that want to invoke tools; ToolCallID is only set on
// tool-result messages answering a specific call.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

// ToolDef is what we tell the model a tool looks like.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON schema for the arguments object
}

type ChatRequest struct {
	Messages []Message
	Tools    []ToolDef

	// MaxTokens caps how many tokens the backend may generate for this
	// response. Leaving this at the backend's own default is what causes
	// large generations (e.g. a full HTML+CSS+JS file in one write_file
	// call) to cut off mid-JSON — see Config.MaxTokens in package agent.
	MaxTokens int

	// Grammar, if set, constrains this one response with a GBNF grammar,
	// overriding any client-level default grammar. Used for tool-free
	// structured-output calls (plan generation, verdicts) where the model
	// must emit JSON matching a fixed schema — a weak model asked for
	// free-form JSON reliably drifts; a grammar makes drift impossible at
	// the token level. Only meaningful on backends that accept a "grammar"
	// field (koboldcpp/llama.cpp); others ignore it.
	Grammar string

	// JSONSchema is the same constraint expressed as JSON Schema, for
	// backends that accept structured output that way instead. Supply
	// both: which one reaches the wire is the dialect's decision, and a
	// backend silently ignoring the form it does not take is how a whole
	// mission sweep ran unconstrained.
	JSONSchema string

	// Temperature, if > 0, is sent to the backend for this call. Zero
	// means "use the client's documented default" rather than "let the
	// server decide": a local backend silently supplies its own value for
	// any field omitted, and a measurement taken under unstated sampling
	// cannot be compared with the next one.
	//
	// There is deliberately no way to request a literal 0.0 — greedy
	// sampling makes a grammar-constrained weak model loop on repeated
	// tokens. Structured calls (plan, verdict) pass a low value to keep
	// output stable without inviting that failure.
	Temperature float64
}

// Usage reports real token counts from the backend's own tokenizer — not
// an approximation like tiktoken, which would be measuring the wrong
// vocabulary entirely for a non-OpenAI local model. Agent.Run uses
// PromptTokens against Config.ContextLimit to know how full the context
// actually is.
type Usage struct {
	PromptTokens     int
	CompletionTokens int

	// CachedTokens is how many of PromptTokens the backend served from
	// prefix cache (llama.cpp reports it as
	// usage.prompt_tokens_details.cached_tokens; koboldcpp reports
	// nothing — verified on the wire — and leaves it zero). Cached
	// tokens still occupy the context window, so budgets and compaction
	// use the full PromptTokens; this split exists for accounting only
	// (cached tokens are cheaper on metered providers).
	CachedTokens int
}

type ChatResponse struct {
	Message Message
	Usage   Usage

	// FinishReason is why generation ended, as reported by the backend:
	// "stop" (natural end), "length" (hit the generation limit — the
	// response is an incomplete stump, and any tool call the model was
	// building was silently discarded), "tool_calls", or "" when the
	// backend didn't say. The agent loop must never treat a "length"
	// response without tool calls as a deliberate finish — live shape:
	// a backend cut a worker off after 38 tokens of preamble and the
	// loop accepted "Let me write the file…" as the final answer.
	FinishReason string
}

// Client talks to exactly one backend. Implementations own everything
// specific to that backend: prompt formatting, tool-call JSON quirks,
// repairing malformed output, etc. The agent loop never knows which
// backend it's talking to.
type Client interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
