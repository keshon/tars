package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/workspace"
)

// SearchResult represents a single search result found by an engine.
type SearchResult struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	URL     string `json:"url"`
}

// SearchWeb searches the web using various engines.
// Supported engines: duckduckgo.
// Default engine is duckduckgo.
type SearchWeb struct{ WS *workspace.Workspace }

func (SearchWeb) Name() string         { return "search_web" }
func (SearchWeb) Mode() agent.ToolMode { return agent.Concurrent }
func (SearchWeb) Description() string {
	return "Search the web for information. Returns a list of results with titles, snippets, and URLs. " +
		"Supported engines: duckduckgo."
}
func (SearchWeb) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string"},
			"engine": {"type": "string", "description": "search engine to use (duckduckgo). Default is duckduckgo."}
		},
		"required": ["query"]
	}`)
}

func (t SearchWeb) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Query  string `json:"query"`
		Engine string `json:"engine"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if err := rejectUnknownFields(args, "query", "engine"); err != nil {
		return "", err
	}

	engine := strings.ToLower(in.Engine)
	if engine == "" {
		engine = "duckduckgo"
	}

	searchURL, err := t.buildSearchURL(in.Query, engine)
	if err != nil {
		return "", err
	}

	// Use FetchRaw to get the HTML content.
	fetcher := FetchRaw{WS: t.WS}
	fetchArgs, _ := json.Marshal(map[string]string{"url": searchURL})
	rawContent, err := fetcher.Run(ctx, fetchArgs)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}

	// Parse the raw content based on the engine.
	results, err := t.parseResults(engine, rawContent)
	if err != nil {
		return "", fmt.Errorf("parsing failed: %w", err)
	}

	if len(results) == 0 {
		return "SEARCH_WEB\nengine: " + engine + "\nquery: " + in.Query + "\n(no results found)", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SEARCH_WEB\nengine: %s\nquery: %s\nresults: %d\n----\n", engine, in.Query, len(results))
	for i, res := range results {
		fmt.Fprintf(&b, "[%d] %s\n    URL: %s\n    Snippet: %s\n\n", i+1, res.Title, res.URL, res.Snippet)
	}

	return b.String(), nil
}

func (t SearchWeb) buildSearchURL(query, engine string) (string, error) {
	encodedQuery := url.QueryEscape(query)
	switch engine {
	case "duckduckgo":
		return fmt.Sprintf("https://duckduckgo.com/html/?q=%s", encodedQuery), nil
	default:
		return "", fmt.Errorf("unsupported engine: %s (supported: duckduckgo)", engine)
	}
}

func (t SearchWeb) parseResults(engine, content string) ([]SearchResult, error) {
	switch engine {
	case "duckduckgo":
		return t.parseDuckDuckGo(content), nil
	default:
		return nil, fmt.Errorf("parser for engine %q is not implemented yet", engine)
	}
}

// parseDuckDuckGo is a fragile regex-based parser for DuckDuckGo HTML.
func (t SearchWeb) parseDuckDuckGo(content string) []SearchResult {
	var results []SearchResult

	// DuckDuckGo results are often contained in blocks.
	// We'll look for a pattern of an <a ... href="URL" ...>TITLE</a> followed by some text (snippet).
	// This regex tries to find: <a ... href="URL" ...>TITLE</a> ... <div ...>SNIPPET</div\s*>
	re := regexp.MustCompile(`(?s)<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>.*?<div[^>]*>(.*?)</div\s*>`)
	matches := re.FindAllStringSubmatch(content, -1)

	for _, m := range matches {
		if len(m) >= 4 {
			rawURL := m[1]
			rawTitle := m[2]
			rawSnippet := m[3]

			// Clean up the extracted parts
			title := strings.TrimSpace(stripHTML(rawTitle))
			snippet := strings.TrimSpace(stripHTML(rawSnippet))

			// Basic URL cleaning
			urlStr := rawURL
			if strings.HasPrefix(urlStr, "//") {
				urlStr = "https:" + urlStr
			}

			// Filter out non-result links (internal DDG links, nav links, etc.)
			if !strings.HasPrefix(urlStr, "http") || strings.Contains(urlStr, "duckduckgo.com") {
				continue
			}

			// A valid result must have a title and a URL.
			if title != "" && urlStr != "" {
				// Avoid duplicates
				exists := false
				for _, r := range results {
					if r.URL == urlStr {
						exists = true
						break
					}
				}
				if !exists {
					results = append(results, SearchResult{
						Title:   title,
						URL:     urlStr,
						Snippet: snippet,
					})
				}
			}
		}
		if len(results) >= 10 {
			break
		}
	}

	return results
}

