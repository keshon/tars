// Package llm speaks to a local model server over the OpenAI-compatible
// /v1/chat/completions endpoint.
//
// Everything shared between backends lives here: the wire format, the
// argument mangling weak models produce, sampling defaults, and the
// agent-facing Client. What differs — where to ask which model is
// loaded, how large its context is, how the repetition penalty is
// spelled, and whether an outside grammar helps or fights the server's
// own — is a dialect. See dialect.go, koboldcpp.go and llamacpp.go.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type Server struct {
	baseURL string
	model   string
	dialect dialect
	http    *http.Client

	// Debug, if set, receives the raw JSON request and response bodies
	// for every call — the fastest way to find out whether something like
	// koboldcpp's own "[TOOLCALL REASONING]" console output actually rides
	// along in the HTTP response, or only ever exists in the server's own
	// terminal.
	Debug io.Writer

	// DRY enables the DRY sampler on every request. Off by default; see
	// the sampling defaults for why.
	DRY bool

	// NoGrammar disables the backend's own content grammar. Set it to
	// find out whether that grammar is helping or getting in the way;
	// per-request structured grammars are unaffected.
	NoGrammar bool

	// Grammar, if set, is sent as a GBNF constraint on every response.
	// koboldcpp already grammar-constrains the arguments of a tool call it
	// decides to make; it does NOT constrain the plain-text path taken
	// when it decides not to call one — which is exactly where a model's
	// own native tool-call template tokens (e.g. "<|tool_call>...") can
	// leak into what should be a normal answer. See DefaultGrammar.
	Grammar string

	// APIKey, when set, is sent as a bearer token on every request. Local
	// servers ignore it; hosted OpenAI-compatible APIs require it. Prefer
	// WithAPIKey or APIKeyFromEnv over embedding a secret in a flag.
	APIKey string

	// ExtraHeaders are sent on every chat request alongside auth, e.g.
	// the referrer headers OpenRouter asks for. Empty by default.
	ExtraHeaders map[string]string

	mu           sync.Mutex
	totalPrompt  int
	totalGen     int
	totalCalls   int
	totalCached  int
	totalRetries int
}

// UsageTotals returns cumulative tokens across all Chat/Stream calls.
func (c *Server) UsageTotals() (prompt, generated, calls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalPrompt, c.totalGen, c.totalCalls
}

// CachedTotal returns cumulative prefix-cached prompt tokens. Zero on
// backends that do not report them.
func (c *Server) CachedTotal() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalCached
}

func (c *Server) recordUsage(promptTokens, completionTokens, cachedTokens int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalPrompt += promptTokens
	c.totalGen += completionTokens
	c.totalCalls++
	c.totalCached += cachedTokens
}

// Default sampling. These are sent on every request so that nothing is
// left to whatever preset the server happened to start with: a local
// backend silently supplies its own defaults for any field omitted, and
// a score measured under unknown sampling cannot be compared to the next
// one. Values here are deliberate and measurable, not inherited.
//
// top_p and top_k are here for a sharper version of that reason. They
// were omitted, so each backend applied its own — and koboldcpp and
// llama-server do not agree on them. A comparison between the two
// backends would have been partly a comparison of their sampler presets,
// which is exactly the confound the temperature default was added to
// remove.
//
// DRY is off by default, and that is a measured decision rather than a
// preference. It was switched on to stop a collapse — a run whose context
// filled with high-entropy base64 degenerated into one token repeated for
// hundreds of lines — and the next baseline showed every probe that
// patches a file getting slower and less reliable while every probe that
// does not stayed byte-identical.
//
// The mechanism fits: DRY penalizes verbatim repetition of sequences, and
// patch_file requires reproducing old_content exactly. Repetition is
// degeneration in prose and correctness in code, so a sampler that cannot
// tell them apart should not be on by default in a coding agent.
// KoboldClient.DRY re-enables it for anyone who wants to re-test.
const (
	defaultTemperature = 0.4
	defaultTopP        = 0.95
	defaultTopK        = 40
	defaultRepPen      = 1.05
	defaultRepPenRange = 1024
	defaultDRYMult     = 0.8
	defaultDRYBase     = 1.75
	defaultDRYAllowed  = 2
)

func newServer(baseURL, model string, d dialect) *Server {
	return &Server{
		baseURL: baseURL,
		model:   model,
		dialect: d,
		// Long: a full context on a local model can take minutes to
		// process before a single token is generated.
		http: &http.Client{Timeout: 120 * time.Minute},
	}
}

// Backend names the server this client talks to, for recorded results.
func (c *Server) Backend() string { return c.dialect.name() }

// WithAPIKey attaches a bearer token to every chat request and returns
// the same client for chaining.
func (c *Server) WithAPIKey(key string) *Server {
	c.APIKey = key
	return c
}

// chatURL joins the configured base with the completions path. Local
// servers are configured as a bare origin ("http://localhost:5001") while
// hosted APIs hand out a root that already ends in /v1, so accept both
// plus a fully qualified path for gateways with deeper prefixes.
func (c *Server) chatURL() string {
	base := c.baseURL
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	if len(base) >= len("/chat/completions") && base[len(base)-len("/chat/completions"):] == "/chat/completions" {
		return base
	}
	if len(base) >= 3 && base[len(base)-3:] == "/v1" {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// --- wire format for the OpenAI-compatible endpoint ---

type wireFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type wireRequest struct {
	Model          string          `json:"model"`
	Messages       []wireMessage   `json:"messages"`
	Tools          []wireTool      `json:"tools,omitempty"`
	Grammar        string          `json:"grammar,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    float64         `json:"temperature,omitempty"`
	TopP           float64         `json:"top_p,omitempty"`
	TopK           int             `json:"top_k,omitempty"`

	// Repetition controls. Both backends pass unknown fields through to
	// the sampler, the same route the grammar field takes, and each
	// dialect fills only its own spelling — koboldcpp's rep_pen or
	// llama-server's repeat_penalty. The DRY names happen to agree.
	RepPen        float64 `json:"rep_pen,omitempty"`
	RepPenRange   int     `json:"rep_pen_range,omitempty"`
	RepeatPenalty float64 `json:"repeat_penalty,omitempty"`
	RepeatLastN   int     `json:"repeat_last_n,omitempty"`
	DRYMult       float64 `json:"dry_multiplier,omitempty"`
	DRYBase       float64 `json:"dry_base,omitempty"`
	DRYAllowed    int     `json:"dry_allowed_length,omitempty"`
}

// responseFormat is the structured-output request carried on the chat
// endpoint. llama-server takes the schema inline; OpenAI takes the
// strict json_schema form. Each dialect fills only its own shape.
type responseFormat struct {
	Type       string          `json:"type"`
	Schema     json.RawMessage `json:"schema,omitempty"`
	JSONSchema *jsonSchemaDef  `json:"json_schema,omitempty"`
}

// jsonSchemaDef is OpenAI's named-schema wrapper for strict structured
// output.
type jsonSchemaDef struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		// llama.cpp reports prefix-cache hits here; koboldcpp omits it.
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// debugSyncer lets logDebug flush *os.File-backed Debug writers
// immediately, so agent-debug.log is readable while the agent is still
// running instead of only after it exits.
type debugSyncer interface{ Sync() error }

func (c *Server) logDebug(format string, args ...any) {
	if c.Debug == nil {
		return
	}
	fmt.Fprintf(c.Debug, format, args...)
	if f, ok := c.Debug.(debugSyncer); ok {
		_ = f.Sync()
	}
}

func (c *Server) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// Precedence: a per-request grammar (the structured plan and verdict
	// calls, which are schema constraints and correct on either backend)
	// wins, then an explicit client override, then whatever this backend
	// wants for ordinary responses.
	structured := req.Grammar != "" || req.JSONSchema != ""
	grammar := c.Grammar
	if grammar == "" && !c.NoGrammar {
		grammar = c.dialect.contentGrammar()
	}
	temperature := req.Temperature
	if temperature <= 0 {
		temperature = defaultTemperature
	}
	wreq := wireRequest{
		Model:       c.model,
		MaxTokens:   req.MaxTokens,
		Temperature: temperature,
		TopP:        defaultTopP,
		TopK:        defaultTopK,
	}
	if structured {
		c.dialect.applyStructured(&wreq, req.Grammar, req.JSONSchema)
	} else if grammar != "" {
		c.dialect.applyStructured(&wreq, grammar, "")
	}
	c.dialect.applySampling(&wreq, c.DRY)

	for _, m := range req.Messages {
		wm := wireMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			// Per the OpenAI tool-call wire format, function.arguments is a
			// JSON-encoded *string*, not a raw object — encodeArguments
			// re-wraps our internal object form before it goes back out.
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireFunction{
					Name:      tc.Name,
					Arguments: encodeArguments(tc.Arguments),
				},
			})
		}
		wreq.Messages = append(wreq.Messages, wm)
	}

	for _, t := range req.Tools {
		wt := wireTool{Type: "function"}
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Parameters
		wreq.Tools = append(wreq.Tools, wt)
	}

	body, err := json.Marshal(wreq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}
	c.logDebug("[%s] --- request ---\n%s\n\n", time.Now().Format(time.RFC3339), body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.chatURL(), bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for k, v := range c.ExtraHeaders {
		if k != "" && v != "" {
			httpReq.Header.Set(k, v)
		}
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call backend: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		snippet := string(raw)
		if len(snippet) > 500 {
			snippet = snippet[:500] + "...(truncated)"
		}
		api := &APIError{Status: resp.StatusCode, StatusText: resp.Status, Body: snippet}
		if v := resp.Header.Get("Retry-After"); v != "" {
			api.RetryAfter = parseRetryAfter(v)
		}
		if v := resp.Header.Get("retry-after-ms"); v != "" {
			if d := parseRetryAfterMs(v); d > 0 {
				api.RetryAfter = d
			}
		}
		if resp.StatusCode == http.StatusBadRequest && isOverflowText(fmt.Errorf("%s", snippet)) {
			api.Overflow = true
		}
		return ChatResponse{}, api
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read response body: %w", err)
	}
	c.logDebug("[%s] --- response (status %s) ---\n%s\n\n", time.Now().Format(time.RFC3339), resp.Status, bodyBytes)

	var wresp wireResponse
	if err := json.Unmarshal(bodyBytes, &wresp); err != nil {
		snippet := string(bodyBytes)
		if len(snippet) > 500 {
			snippet = snippet[:500] + "...(truncated)"
		}
		return ChatResponse{}, fmt.Errorf("decode response: %w — backend said: %s", err, snippet)
	}
	if len(wresp.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("backend returned no choices")
	}

	wm := wresp.Choices[0].Message
	out := Message{
		Role:    Role(wm.Role),
		Content: wm.Content,
	}
	for _, tc := range wm.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: repairArguments(normalizeArguments(tc.Function.Arguments)),
		})
	}

	usage := Usage{
		PromptTokens:     wresp.Usage.PromptTokens,
		CompletionTokens: wresp.Usage.CompletionTokens,
		CachedTokens:     wresp.Usage.PromptTokensDetails.CachedTokens,
	}
	c.recordUsage(usage.PromptTokens, usage.CompletionTokens, usage.CachedTokens)
	p, g, n := c.UsageTotals()
	c.logDebug("[%s] --- usage (this call: %d prompt (%d cached) + %d generated; run totals: %d prompt + %d generated over %d calls) ---\n\n",
		time.Now().Format(time.RFC3339), usage.PromptTokens, usage.CachedTokens, usage.CompletionTokens, p, g, n)

	return ChatResponse{
		Message:      out,
		FinishReason: wresp.Choices[0].FinishReason,
		Usage:        usage,
	}, nil
}

// MaxContextLength reports the context window the server was actually
// started with, so budget tracking uses a fact rather than a guess. Call
// once at startup and feed the result into agent.Config.ContextLimit.
//
// Each dialect asks over its own endpoint, so an error here is ambiguous:
// the backend may be down, or it may be the other backend answering. Do
// not treat it as "budget tracking unavailable" and continue — call
// DetectKind to tell those apart first.
func (c *Server) MaxContextLength(ctx context.Context) (int, error) {
	return c.dialect.contextLimit(ctx, c.http, c.baseURL)
}

// ModelName asks the backend which model is actually loaded, which is
// rarely what the -model flag says: local servers usually ignore it and
// serve whatever weights they were started with. Worth recording next to
// any measurement — a pass rate compared against one from a different
// model, or the same model at a different quantization, is worse than no
// number at all.
//
// A hosted gateway serves the model named in each request, so the
// configured name is the answer and no probe is needed.
func (c *Server) ModelName(ctx context.Context) (string, error) {
	if _, ok := c.dialect.(openaiDialect); ok {
		if c.model != "" {
			return c.model, nil
		}
	}
	return c.dialect.modelName(ctx, c.http, c.baseURL)
}

// normalizeArguments handles the real shape of this wire format: per spec,
// function.arguments is a JSON-encoded *string*, e.g. the bytes
// `"{\"command\": \"ls\"}"` rather than `{"command": "ls"}` directly. Some
// local servers send the object form anyway, so accept either: if the raw
// bytes are themselves a JSON string, unwrap it once; otherwise pass
// through unchanged.
func normalizeArguments(raw json.RawMessage) json.RawMessage {
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return json.RawMessage(asString)
	}
	return raw
}

// encodeArguments is normalizeArguments' mirror image for outgoing
// requests: our internal ToolCall.Arguments is always the plain object
// form, so re-wrap it as a JSON string before it goes on the wire,
// matching what this backend actually expects.
func encodeArguments(args json.RawMessage) json.RawMessage {
	wrapped, err := json.Marshal(string(args))
	if err != nil {
		return json.RawMessage(`"{}"`)
	}
	return json.RawMessage(wrapped)
}

// repairArguments passes wire-format arguments through unchanged. Valid
// JSON (including a bare string instead of an object) reaches tools as-is
// so they can fail fast with a clear schema error. Invalid bytes also pass
// through — tools report bad arguments rather than masking mistakes behind
// an {"input":"..."} wrapper.
func repairArguments(raw json.RawMessage) json.RawMessage {
	return raw
}
