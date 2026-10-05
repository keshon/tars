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

	// Canned DuckDuckGo HTML structure based on our regex:
	// <a[^>]+href="([^"]+)"[^>]*>(.*?)</a>.*?<div[^>]*>(.*?)</div\s*>
	cannedDDG := `
		<html>
			<body>
				<a href="https://example.com/page1">Title 1</a>
				<div class="snippet">This is snippet 1</div>
				
				<a href="https://example.com/page2">Title 2</a>
				<div class="desc">This is snippet 2</div>
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
	if !strings.Contains(result, "Title 2") || !strings.Contains(result, "This is snippet 2") {
		t.Errorf("Result missing Title 2 or Snippet 2. Got: %s", result)
	}
}

func TestSearchWeb_Reddit(t *testing.T) {
	ws := newTestWS()
	searcher := SearchWeb{WS: ws}
	ctx := context.Background()

	// Canned Reddit HTML structure based on our regex:
	// <a[^>]+href="([^"]+/r/[^/]+/comments/[^/]+)"[^>]*>(.*?)</a>
	cannedReddit := `
		<html>
			<body>
				<a href="/r/golang/comments/12345/hello_world/">The Great Golang Discussion</a>
				<a href="/r/programming/comments/67890/test_thread/">Test Thread</a>
			</body>
		</html>
	`
	fetchRawTransport = &mockTransport{
		body:   cannedReddit,
		status: http.StatusOK,
	}
	defer func() { fetchRawTransport = nil }()

	args, _ := json.Marshal(map[string]string{
		"query":  "reddit search",
		"engine": "reddit",
	})

	result, err := searcher.Run(ctx, args)
	if err != nil {
		t.Fatalf("SearchWeb Reddit failed: %v", err)
	}

	if !strings.Contains(result, "The Great Golang Discussion") {
		t.Errorf("Result missing Reddit title. Got: %s", result)
	}
	if !strings.Contains(result, "https://www.reddit.com/r/golang/comments/12345/hello_world/") {
		t.Errorf("Result missing correct Reddit URL. Got: %s", result)
	}
}
