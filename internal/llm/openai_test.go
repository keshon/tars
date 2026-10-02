package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIClient_RegisteredAsKind(t *testing.T) {
	c, err := ClientFor("openai", "https://api.openai.com/v1", "gpt-4o-mini")
	if err != nil {
		t.Fatalf("ClientFor openai: %v", err)
	}
	if c.Backend() != "openai-compatible" {
		t.Fatalf("Backend() = %q", c.Backend())
	}
	found := false
	for _, k := range Kinds() {
		if k == "openai" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Kinds() = %v, missing openai", Kinds())
	}
}

func TestOpenAIChat_SendsAuthAndOmitsLocalFields(t *testing.T) {
	var captured []byte
	var auth, ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		auth = r.Header.Get("Authorization")
		ctype = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "gpt-4o-mini").WithAPIKey("sk-test")
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want bearer token", auth)
	}
	if ctype != "application/json" {
		t.Errorf("Content-Type = %q", ctype)
	}
	body := string(captured)
	for _, local := range []string{`"rep_pen"`, `"repeat_penalty"`, `"top_k"`, `"grammar"`, `"dry_multiplier"`} {
		if strings.Contains(body, local) {
			t.Errorf("remote request must not contain %s: %s", local, body)
		}
	}
	for _, want := range []string{`"temperature"`, `"top_p"`, `"model":"gpt-4o-mini"`} {
		if !strings.Contains(body, want) {
			t.Errorf("remote request missing %s: %s", want, body)
		}
	}
}

func TestOpenAIChat_StructuredUsesResponseFormat(t *testing.T) {
	var captured []byte
	srv := captureServer(t, &captured)
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "gpt-4o-mini")
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages:   []Message{{Role: RoleUser, Content: "plan"}},
		Grammar:    `root ::= "{}"`,
		JSONSchema: `{"type":"object"}`,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var sent struct {
		Grammar        string `json:"grammar"`
		ResponseFormat *struct {
			Type       string `json:"type"`
			JSONSchema *struct {
				Name string `json:"name"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal(captured, &sent); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sent.Grammar != "" {
		t.Errorf("grammar sent to remote: %q", sent.Grammar)
	}
	if sent.ResponseFormat == nil || sent.ResponseFormat.Type != "json_schema" {
		t.Fatalf("want json_schema response_format, got %+v", sent.ResponseFormat)
	}
	if sent.ResponseFormat.JSONSchema == nil || sent.ResponseFormat.JSONSchema.Name == "" {
		t.Errorf("json_schema wrapper missing name: %+v", sent.ResponseFormat)
	}
}

func TestOpenAIChat_StructuredWithoutSchemaUsesJSONObject(t *testing.T) {
	var captured []byte
	srv := captureServer(t, &captured)
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "gpt-4o-mini")
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Grammar:  `root ::= "{}"`,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(string(captured), `"json_object"`) {
		t.Errorf("want json_object response_format, got %s", captured)
	}
}

func TestOpenAIChat_ErrorBodySurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Incorrect API key","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	c := NewOpenAIClient(srv.URL, "gpt-4o-mini").WithAPIKey("bad")
	_, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("want error for 401, got nil")
	}
	if !strings.Contains(err.Error(), "Incorrect API key") {
		t.Errorf("error should carry provider message, got: %v", err)
	}
}

func TestOpenAIChatURL_Joining(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.openai.com/v1":          "https://api.openai.com/v1/chat/completions",
		"https://api.openai.com/v1/":         "https://api.openai.com/v1/chat/completions",
		"https://openrouter.ai/api/v1":       "https://openrouter.ai/api/v1/chat/completions",
		"http://localhost:5001":              "http://localhost:5001/v1/chat/completions",
		"http://localhost:5001/":             "http://localhost:5001/v1/chat/completions",
		"https://gateway/x/chat/completions": "https://gateway/x/chat/completions",
	} {
		c := NewOpenAIClient(base, "m")
		if got := c.chatURL(); got != want {
			t.Errorf("chatURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestOpenAIModelName_ReturnsConfigured(t *testing.T) {
	c := NewOpenAIClient("http://127.0.0.1:1", "deepseek-chat")
	name, err := c.ModelName(context.Background())
	if err != nil || name != "deepseek-chat" {
		t.Fatalf("ModelName = %q, %v", name, err)
	}
}

func TestOpenAIContextLimit_Table(t *testing.T) {
	if got := OpenAIContextLimit("gpt-4o-mini"); got != 128000 {
		t.Errorf("gpt-4o-mini = %d", got)
	}
	if got := OpenAIContextLimit("anthropic/claude-sonnet-4"); got != 200000 {
		t.Errorf("claude = %d", got)
	}
	if OpenAIContextLimit("something-brand-new") <= 0 {
		t.Error("unknown model should get a safe default, not zero")
	}
}
