package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestChat_StringEncodedArguments reproduces the exact payload from the
// real koboldcpp run: function.arguments comes back as a JSON-encoded
// string, not a raw object. Decoding it must yield a usable object, and
// re-encoding it for the next request must round-trip to the same string
// form the backend sent in the first place.
func TestChat_StringEncodedArguments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "",
					"tool_calls": [{
						"id": "call_75132",
						"type": "function",
						"function": {
							"name": "run_shell",
							"arguments": "{\"command\": \"mkdir sandbox\"}"
						}
					}]
				}
			}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1}
		}`))
	}))
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	resp, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "make a dir called sandbox"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}

	got := resp.Message.ToolCalls[0]

	// This is the actual regression check: Arguments must unmarshal as a
	// real object with a "command" field, not fail with "cannot unmarshal
	// string into Go value of type struct{...}".
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(got.Arguments, &args); err != nil {
		t.Fatalf("Arguments did not decode as an object: %v (raw: %s)", err, got.Arguments)
	}
	if args.Command != "mkdir sandbox" {
		t.Fatalf("Command = %q, want %q", args.Command, "mkdir sandbox")
	}

	// Round-trip: encodeArguments must produce exactly the string form the
	// backend originally sent, since that's the format it expects back.
	encoded := encodeArguments(got.Arguments)
	want := `"{\"command\": \"mkdir sandbox\"}"`
	if string(encoded) != want {
		t.Fatalf("encodeArguments = %s, want %s", encoded, want)
	}
}

// TestNormalizeArguments_AcceptsObjectForm covers backends that send the
// arguments object directly instead of string-encoding it — both shapes
// must work, since not every local server follows the spec the same way.
// TestChat_CachedTokensParsed covers the llama.cpp usage shape
// (prompt_tokens_details.cached_tokens), which koboldcpp omits —
// verified absent on the wire. A backend that does not report it must
// decode to zero, not to an error.
func TestChat_CachedTokensParsed(t *testing.T) {
	serve := func(usage string) ChatResponse {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "hi"}}],
				"usage": {` + usage + `}}`))
		}))
		defer srv.Close()
		c := NewKoboldClient(srv.URL, "local")
		resp, err := c.Chat(context.Background(), ChatRequest{
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		return resp
	}
	resp := serve(`"prompt_tokens": 44, "completion_tokens": 48,
		"prompt_tokens_details": {"cached_tokens": 40}`)
	if resp.Usage.CachedTokens != 40 {
		t.Fatalf("cached = %d, want 40", resp.Usage.CachedTokens)
	}
	resp = serve(`"prompt_tokens": 15, "completion_tokens": 10`)
	if resp.Usage.CachedTokens != 0 {
		t.Fatalf("absent details must decode to zero, got %d", resp.Usage.CachedTokens)
	}
}

func TestRepairArguments_PassesThroughWithoutInputWrapper(t *testing.T) {
	raw := json.RawMessage(`not-json-at-all`)
	got := repairArguments(raw)
	if string(got) != string(raw) {
		t.Fatalf("repairArguments = %s, want passthrough %s", got, raw)
	}

	// Bare JSON string should also pass through — tools fail fast on schema.
	bare := json.RawMessage(`"abc"`)
	got = repairArguments(bare)
	if string(got) != `"abc"` {
		t.Fatalf("repairArguments = %s, want bare string passthrough", got)
	}
}

func TestNormalizeArguments_AcceptsObjectForm(t *testing.T) {
	raw := json.RawMessage(`{"command": "ls"}`)
	got := normalizeArguments(raw)
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(got, &args); err != nil {
		t.Fatalf("object-form arguments did not decode: %v", err)
	}
	if args.Command != "ls" {
		t.Fatalf("Command = %q, want %q", args.Command, "ls")
	}
}

func TestChat_DebugWriterCapturesRawBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{}}`))
	}))
	defer srv.Close()

	var log bytes.Buffer
	c := NewKoboldClient(srv.URL, "local")
	c.Debug = &log

	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	out := log.String()
	if !strings.Contains(out, "--- request ---") || !strings.Contains(out, `"hello"`) {
		t.Fatalf("debug log missing request body: %s", out)
	}
	if !strings.Contains(out, "--- response") || !strings.Contains(out, `"hi"`) {
		t.Fatalf("debug log missing response body: %s", out)
	}
}

func TestChat_GrammarFieldSentWhenSet(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	c.Grammar = DefaultGrammar

	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var sent struct {
		Grammar string `json:"grammar"`
	}
	if err := json.Unmarshal(captured, &sent); err != nil {
		t.Fatalf("decode sent request: %v", err)
	}
	if sent.Grammar != DefaultGrammar {
		t.Fatalf("grammar field = %q, want the configured DefaultGrammar", sent.Grammar)
	}
}

// The content grammar is a backend property, not a caller setting: see
// dialect.contentGrammar. NoGrammar is how a caller opts out, and this
// checks the opt-out actually reaches the wire.
func TestChat_NoGrammarSuppressesTheBackendGrammar(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	c.NoGrammar = true

	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if strings.Contains(string(captured), `"grammar"`) {
		t.Fatalf("expected no grammar field in request, got: %s", captured)
	}
}

func TestChat_MaxTokensSentWhenSet(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages:  []Message{{Role: RoleUser, Content: "hello"}},
		MaxTokens: 8192,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var sent struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(captured, &sent); err != nil {
		t.Fatalf("decode sent request: %v", err)
	}
	if sent.MaxTokens != 8192 {
		t.Fatalf("max_tokens = %d, want 8192", sent.MaxTokens)
	}
}

func TestMaxContextLength_ParsesValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/extra/true_max_context_length" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"value": 20480}`))
	}))
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	got, err := c.MaxContextLength(context.Background())
	if err != nil {
		t.Fatalf("MaxContextLength: %v", err)
	}
	if got != 20480 {
		t.Fatalf("got %d, want 20480", got)
	}
}

func TestMaxContextLength_ErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	if _, err := c.MaxContextLength(context.Background()); err == nil {
		t.Fatal("expected an error for a 404 response, got nil")
	}
}
