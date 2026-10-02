// Remote OpenAI-compatible backend.
//
// One dialect covers every hosted provider that speaks the OpenAI chat
// format: OpenAI itself, OpenRouter, DeepSeek, Groq, Together, Mistral,
// xAI, Ollama/LM Studio reached over a network, and similar gateways.
// The wire shape is already what Server sends; what differs from the
// local dialects is what must NOT be sent: no GBNF grammar, no
// koboldcpp/llama-server sampler spellings, no top_k (not an OpenAI
// field). Authentication is a bearer token on every request.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// NewOpenAIClient talks to any OpenAI-compatible /chat/completions endpoint.
// baseURL is the API root with or without a trailing /v1, e.g.
// "https://api.openai.com/v1" or "https://openrouter.ai/api/v1".
// Use WithAPIKey to attach the bearer token.
func NewOpenAIClient(baseURL, model string) *Server {
	return newServer(baseURL, model, openaiDialect{})
}

type openaiDialect struct{}

func (openaiDialect) name() string { return "openai-compatible" }

// Remote providers constrain tool calls from the tool schemas themselves.
// Sending the local content guard would constrain nothing they understand
// and risks fighting the provider's own template.
func (openaiDialect) contentGrammar() string { return "" }

// Structured output on the chat endpoint is response_format. With a schema
// the strict json_schema form names the schema so the provider validates
// against it; without one json_object still forces JSON rather than prose.
func (openaiDialect) applyStructured(req *wireRequest, gbnf, schema string) {
	if schema == "" {
		req.ResponseFormat = &responseFormat{Type: "json_object"}
		return
	}
	req.ResponseFormat = &responseFormat{
		Type: "json_schema",
		JSONSchema: &jsonSchemaDef{
			Name:   "response",
			Strict: true,
			Schema: json.RawMessage(schema),
		},
	}
}

// The shared Chat path sets the local sampler defaults before this runs.
// OpenAI-compatible APIs take temperature and top_p; top_k, rep_pen and
// the DRY fields are local inventions a hosted API rejects or ignores,
// so clear them here rather than teaching Chat about every dialect.
func (openaiDialect) applySampling(req *wireRequest, dry bool) {
	req.TopK = 0
	req.RepPen = 0
	req.RepPenRange = 0
	req.RepeatPenalty = 0
	req.RepeatLastN = 0
	req.DRYMult = 0
	req.DRYBase = 0
	req.DRYAllowed = 0
}

// A hosted gateway has no probe endpoint for the context window. Report
// that plainly so the caller falls back to its configured model table or
// an explicit override instead of silently running with no budget.
func (openaiDialect) contextLimit(ctx context.Context, hc *http.Client, baseURL string) (int, error) {
	return 0, fmt.Errorf("openai-compatible backends do not report a context window; pass it explicitly")
}

func (openaiDialect) modelName(ctx context.Context, hc *http.Client, baseURL string) (string, error) {
	return "", fmt.Errorf("openai-compatible backends serve the requested model; use the configured name")
}

// APIKeyFromEnv returns the first bearer token found under the usual
// names, or "". Flag values win over this; this is the unattended path.
func APIKeyFromEnv() string {
	for _, name := range []string{
		"TARS_API_KEY",
		"OPENAI_API_KEY",
		"OPENROUTER_API_KEY",
		"DEEPSEEK_API_KEY",
		"GROQ_API_KEY",
		"TOGETHER_API_KEY",
		"ANTHROPIC_API_KEY",
	} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// OpenAIContextLimit is the best-known window for a model name, matched
// case-insensitively on substring. Unknown names get the conservative
// 128k most current hosted models meet or exceed; callers with an exact
// number should pass it explicitly instead.
func OpenAIContextLimit(model string) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude"):
		return 200000
	case strings.Contains(m, "grok"):
		return 131072
	case strings.Contains(m, "deepseek"):
		return 128000
	case strings.Contains(m, "llama-4"), strings.Contains(m, "llama4"):
		return 1000000
	case strings.Contains(m, "gpt-4.1"), strings.Contains(m, "gpt-4o"), strings.Contains(m, "gpt-4 "),
		strings.Contains(m, "gpt-5"), strings.Contains(m, "o1"), strings.Contains(m, "o3"), strings.Contains(m, "o4"):
		return 128000
	case strings.Contains(m, "gemini"):
		return 1000000
	case strings.Contains(m, "mistral"), strings.Contains(m, "mixtral"), strings.Contains(m, "qwen"):
		return 128000
	default:
		return 128000
	}
}
