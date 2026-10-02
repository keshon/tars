package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// A dialect is the small part of a local model server that is not
// OpenAI-compatible.
//
// koboldcpp and llama-server both serve /v1/chat/completions and both
// accept extra sampler fields alongside it, so almost everything is
// shared. What differs is where you ask what model is loaded and how
// large its context is, and how each spells its repetition penalty. That
// is the whole of it, which is why this interface is three methods rather
// than a second client.
type dialect interface {
	// name identifies the backend in errors and recorded results.
	name() string

	// contextLimit reports the window the server was started with, and
	// modelName the weights it actually loaded. Separate because they are
	// asked separately: a caller that only wants the window should not
	// need an unrelated endpoint to exist.
	contextLimit(ctx context.Context, hc *http.Client, baseURL string) (int, error)
	modelName(ctx context.Context, hc *http.Client, baseURL string) (string, error)

	// applySampling writes this backend's spelling of the sampler
	// settings into an outgoing request.
	applySampling(req *wireRequest, dry bool)

	// contentGrammar constrains an ordinary, non-structured response, or
	// is empty for no constraint. It is a backend decision because the
	// same tokens mean opposite things on the two servers: koboldcpp
	// parses tool calls itself, so a model writing its native tool-call
	// tags into content is a leak worth blocking, while on llama-server
	// with --jinja those tags are the protocol.
	contentGrammar() string

	// applyStructured asks for a constrained response in the form this
	// backend actually accepts. koboldcpp takes a GBNF grammar as a
	// pass-through field on the chat endpoint; llama-server takes
	// response_format there and documents grammar only on /completion,
	// where a grammar field is accepted and ignored without complaint.
	applyStructured(req *wireRequest, gbnf, schema string)
}

func getJSON(ctx context.Context, hc *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("call backend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// kinds maps a -backend-kind flag value to its dialect. One table, so the
// flag documentation, the client constructor and the mismatch probe
// cannot drift apart about what "llama" means.
var kinds = []struct {
	kind    string
	dialect dialect
}{
	{"kobold", koboldDialect{}},
	{"llama", llamaDialect{}},
	{"openai", openaiDialect{}},
}

// Kinds lists the accepted -backend-kind values, for flag help and error
// messages that stay correct when a backend is added.
func Kinds() []string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, k.kind)
	}
	return out
}

// ClientFor builds the client for a -backend-kind value.
func ClientFor(kind, baseURL, model string) (*Server, error) {
	for _, k := range kinds {
		if k.kind == kind {
			return newServer(baseURL, model, k.dialect), nil
		}
	}
	return nil, fmt.Errorf("unknown backend kind %q, want one of %s",
		kind, strings.Join(Kinds(), " or "))
}

// DetectKind reports which backend is actually answering at baseURL, or
// "" if neither identifies itself.
//
// Each dialect asks a different server for its context window over a
// different endpoint, so the probe that succeeds names the server that is
// really there. That makes a wrong -backend-kind detectable rather than
// merely survivable, which matters because surviving it is worse: the
// dialects disagree about the grammar field, the sampler field names and
// the structured-output mechanism, so a mismatched run completes and
// silently measures nothing. Nine mission runs did exactly that.
//
// Best-effort and quick. A backend that is simply down probes as "" and
// the caller reports that as the missing information it is.
func DetectKind(ctx context.Context, baseURL string) string {
	hc := &http.Client{Timeout: 5 * time.Second}
	for _, k := range kinds {
		if n, err := k.dialect.contextLimit(ctx, hc, baseURL); err == nil && n > 0 {
			return k.kind
		}
	}
	return ""
}
