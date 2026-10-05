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

// parseDuckDuckGo parses the duckduckgo.com/html/ endpoint. Its real
// structure (verified 2026-10): result links are
// <a class="result__a" href="//duckduckgo.com/l/?uddg=<real-url>...>,
// snippets are <a class="result__snippet">. The /l/ wrapper must be
// unwrapped, not filtered — dropping duckduckgo.com links drops every
// result, which is how this parser shipped returning nothing live.
func (t SearchWeb) parseDuckDuckGo(content string) []SearchResult {
	linkRe := regexp.MustCompile(`<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	snipRe := regexp.MustCompile(`<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)

	linkMatches := linkRe.FindAllStringSubmatch(content, -1)
	snipMatches := snipRe.FindAllStringSubmatch(content, -1)

	var results []SearchResult
	for i, m := range linkMatches {
		urlStr := unwrapDDGLink(m[1])
		if urlStr == "" {
			continue
		}
		// stripHTML emits a newline at every tag boundary, so inline
		// markup ("Title <b>1</b>") comes out multiline — collapse to
		// one line for result display.
		title := collapseSpace(stripHTML(m[2]))
		if title == "" {
			continue
		}
		var snippet string
		if i < len(snipMatches) {
			snippet = collapseSpace(stripHTML(snipMatches[i][1]))
		}
		duplicate := false
		for _, r := range results {
			if r.URL == urlStr {
				duplicate = true
				break
			}
		}
		if !duplicate {
			results = append(results, SearchResult{Title: title, URL: urlStr, Snippet: snippet})
		}
		if len(results) >= 10 {
			break
		}
	}

	return results
}

// collapseSpace trims and collapses all whitespace runs to single
// spaces: extracted result fields must render on one line.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// unwrapDDGLink resolves a result href to the target URL: /l/?uddg=...
// wrappers decode to the real page, bare http(s) links pass through,
// anything else (internal DDG pages, nav) is rejected with "".
func unwrapDDGLink(href string) string {
	href = strings.ReplaceAll(href, "&amp;", "&")
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	if !strings.HasPrefix(href, "http") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.Contains(u.Host, "duckduckgo.com") {
		real := u.Query().Get("uddg")
		if real == "" || (!strings.HasPrefix(real, "http://") && !strings.HasPrefix(real, "https://")) {
			return ""
		}
		return real
	}
	return href
}
