package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/workspace"
)

type mockTransport struct {
	body   string
	status int
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: m.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(m.body)),
	}, nil
}

// Helper to create a workspace
func newTestWS() *workspace.Workspace {
	return &workspace.Workspace{}
}

func TestFetchRaw(t *testing.T) {
	ws := newTestWS()
	fetcher := FetchRaw{WS: ws}
	ctx := context.Background()

	cannedHTML := "<html><body>Hello World</body></html>"
	fetchRawTransport = &mockTransport{
		body:   cannedHTML,
		status: http.StatusOK,
	}
	defer func() { fetchRawTransport = nil }()

	args, _ := json.Marshal(map[string]string{"url": "https://example.com"})
	result, err := fetcher.Run(ctx, args)
	if err != nil {
		t.Fatalf("FetchRaw failed: %v", err)
	}

	if !strings.Contains(result, "Hello World") {
		t.Errorf("Expected content to contain 'Hello World', got: %s", result)
	}
}

func TestSearchWeb_DuckDuckGo(t *testing.T) {
	ws := newTestWS()
	searcher := SearchWeb{WS: ws}
	ctx := context.Background()

	// Real duckduckgo.com/html/ structure: result links carry
	// class="result__a" with an /l/?uddg= wrapper href, snippets are
	// class="result__snippet" anchors. A parser tested against anything
	// else passes its test and returns nothing live.
	cannedDDG := `
		<html>
			<body>
				<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1&amp;rut=abc">Title <b>1</b></a>
				<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1">This is snippet 1</a>

				<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage2">Title 2</a>
				<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage2">This is snippet 2</a>

				<a class="result__a" href="https://duckduckgo.com/settings">Settings</a>
			</body>
		</html>
	`
	fetchRawTransport = &mockTransport{
		body:   cannedDDG,
		status: http.StatusOK,
	}
	defer func() { fetchRawTransport = nil }()

	args, _ := json.Marshal(map[string]string{
		"query":  "test query",
		"engine": "duckduckgo",
	})

	result, err := searcher.Run(ctx, args)
	if err != nil {
		t.Fatalf("SearchWeb DuckDuckGo failed: %v", err)
	}

	if !strings.Contains(result, "Title 1") || !strings.Contains(result, "This is snippet 1") {
		t.Errorf("Result missing Title 1 or Snippet 1. Got: %s", result)
	}
	if !strings.Contains(result, "https://example.com/page2") || !strings.Contains(result, "This is snippet 2") {
		t.Errorf("Result missing unwrapped page2 URL or Snippet 2. Got: %s", result)
	}
	// The /l/ wrapper must be unwrapped, never leaked; internal DDG
	// links (settings, nav) must not appear as results.
	if strings.Contains(result, "uddg=") || strings.Contains(result, "duckduckgo.com/settings") {
		t.Errorf("Result leaks wrapper or internal links. Got: %s", result)
	}
}

// No reddit engine: old.reddit.com redirects to the www.reddit.com
// login wall (verified live 2026-10-05 — "Welcome to Reddit" JS shell,
// zero server-rendered results), so there is nothing to parse. If
// Reddit becomes scrapable again, add the engine with a canned test in
// the real response shape, not an invented one.
func TestSearchWeb_UnsupportedEngineRejected(t *testing.T) {
	ws := newTestWS()
	searcher := SearchWeb{WS: ws}

	args, _ := json.Marshal(map[string]string{
		"query":  "reddit search",
		"engine": "reddit",
	})

	_, err := searcher.Run(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "unsupported engine") {
		t.Fatalf("engine reddit: err = %v, want unsupported-engine rejection", err)
	}
}
