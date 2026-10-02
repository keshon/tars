// koboldcpp-specific behaviour: where it reports the loaded model and
// context window, how it spells the repetition penalty, and the grammar
// that stops a model writing its native tool-call tags into content.
package llm

import (
	"context"
	"net/http"
)

// NewKoboldClient talks to a koboldcpp server.
func NewKoboldClient(baseURL, model string) *Server {
	return newServer(baseURL, model, koboldDialect{})
}

// DefaultGrammar forbids a response from starting with '<' or '[' —
// EXCEPT for a literal leading "<think>", which thinking models (Qwen3
// family) must be allowed to emit or their generation degrades at the
// first token. Otherwise it allows anything — plain prose, Unicode text,
// or a real tool-call JSON object (which always starts with '{'). It
// blocks the two leak shapes actually observed: a model's native
// "<tool_call>..." tags, and a model writing out a whole tool-call
// envelope as a JSON *array* in plain content instead of using the
// structured tool_calls field. This stays because it costs one grammar
// rule at the cheapest position (first token) and stops the leak before
// generation spends tokens on it. Mid-message leaks (including anything
// after a think block) are recovered in Go by ExtractLeakedCalls, which
// executes well-formed leaked calls instead of nudging for a retry.
//
// This is fundamentally reactive — it's not a general solution to "models
// sometimes leak structured output as text," just a growing blocklist of
// the specific shapes we've actually hit. If a third shape shows up,
// don't reach for a third character to ban; that's the signal to stop
// patching this grammar and instead define one strict response envelope
// of our own (via koboldcpp's JSON-schema-as-grammar support) that the
// model is constrained to use for every turn, not just tool calls.
//
// Depends on live backend behavior, not just Go: its docs say arbitrary
// extra fields (including "grammar") pass through /v1/chat/completions,
// and that grammar can coexist with "tools", but the exact interaction
// between an externally supplied grammar and koboldcpp's own internal
// tool-call grammar pass isn't something testable without a live server.
// Verify with -debug: confirm "grammar" appears in the logged request,
// and that move_file/list_files calls still work normally.
const DefaultGrammar = `root ::= first rest
first ::= [^<\[` + "\\x00" + `] | "<think>"
rest ::= [^` + "\\x00" + `]*
`

type koboldDialect struct{}

func (koboldDialect) name() string { return "koboldcpp" }

// koboldcpp does not constrain the plain-text path it takes when it
// decides not to call a tool, which is exactly where a model's own
// tool-call template can leak into what should be an answer.
func (koboldDialect) contentGrammar() string { return DefaultGrammar }

func (koboldDialect) applyStructured(req *wireRequest, gbnf, schema string) {
	req.Grammar = gbnf
}

func (koboldDialect) applySampling(req *wireRequest, dry bool) {
	req.RepPen = defaultRepPen
	req.RepPenRange = defaultRepPenRange
	if dry {
		req.DRYMult = defaultDRYMult
		req.DRYBase = defaultDRYBase
		req.DRYAllowed = defaultDRYAllowed
	}
}

func (koboldDialect) contextLimit(ctx context.Context, hc *http.Client, baseURL string) (int, error) {
	var out struct {
		Value int `json:"value"`
	}
	err := getJSON(ctx, hc, baseURL+"/api/extra/true_max_context_length", &out)
	return out.Value, err
}

func (koboldDialect) modelName(ctx context.Context, hc *http.Client, baseURL string) (string, error) {
	var out struct {
		Result string `json:"result"`
	}
	err := getJSON(ctx, hc, baseURL+"/api/v1/model", &out)
	return out.Result, err
}
