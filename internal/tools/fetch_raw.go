package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/workspace"
)

// fetchRawMaxBytes caps a fetched page: 5MB on the wire, 128KB in context.
const (
	fetchRawMaxBytes   = 5 * 1024 * 1024
	fetchRawContextMax = 128 * 1024
	fetchRawTimeout    = 30 * time.Second
)

// fetchRawTransport overrides the HTTP transport when non-nil. It is a
// test seam: unit tests serve canned bodies for public-looking URLs that
// must validate but can never be dialed in a sandbox.
var fetchRawTransport http.RoundTripper

// FetchRaw fetches a URL and returns its raw content. Unlike webfetch,
// it does not strip HTML, making it suitable for parsing page structure.
type FetchRaw struct{ WS *workspace.Workspace }

func (FetchRaw) Name() string         { return "fetch_raw" }
func (FetchRaw) Mode() agent.ToolMode { return agent.Concurrent }
func (FetchRaw) Description() string {
	return "Fetch a URL and return its raw HTML/content. Use this when you need to parse the structure of a page (e.g., for searching)."
}
func (FetchRaw) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string"},
			"max_bytes": {"type": "integer", "description": "optional cap on returned text bytes; omit for the default"}
		},
		"required": ["url"]
	}`)
}

func (t FetchRaw) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL      string `json:"url"`
		MaxBytes *int   `json:"max_bytes"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if err := rejectUnknownFields(args, "url", "max_bytes"); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, fetchRawTimeout)
	defer cancel()
	clean, err := normalizePublicHTTPURL(in.URL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clean, nil)
	if err != nil {
		return "", fmt.Errorf("bad url: %w", err)
	}
	req.Header.Set("User-Agent", "tars-agent/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/*;q=0.8,*/*;q=0.1")

	client := http.DefaultClient
	if fetchRawTransport != nil {
		client = &http.Client{Transport: fetchRawTransport}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch failed: %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchRawMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read failed: %w", err)
	}
	truncatedWire := len(body) > fetchRawMaxBytes
	if truncatedWire {
		body = body[:fetchRawMaxBytes]
	}

	text := string(body)

	limit := fetchRawContextMax
	if in.MaxBytes != nil && *in.MaxBytes > 0 && *in.MaxBytes < limit {
		limit = *in.MaxBytes
	}

	var b strings.Builder
	fmt.Fprintf(&b, "FETCH_RAW\nurl: %s\nstatus: %s\n", in.URL, resp.Status)
	if truncatedWire {
		b.WriteString("(page truncated at 5MB on the wire)\n")
	}
	b.WriteString("----\n")
	if len(text) > limit {
		b.WriteString(agent.TruncateMiddle(text, limit))
		fmt.Fprintf(&b, "\n...(text truncated at %d bytes of %d total.", limit, len(text))
		if t.WS != nil {
			if p := Spill(t.WS.Root(), "fetch_raw", text); p != "" {
				fmt.Fprintf(&b, " Full text: %s)", p)
			} else {
				b.WriteString(")")
			}
		} else {
			b.WriteString(")")
		}
	} else {
		b.WriteString(text)
	}
	return b.String(), nil
}
