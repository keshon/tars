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
)

const checkURLBodyMax = 512

// CheckURL makes a real HTTP request instead of trusting a dev server's
// startup banner — "Local: http://localhost:5173/" printed to stdout
// doesn't mean the port is actually reachable. A failed connection is a
// normal, useful result here, not a tool error: "connection refused" is
// exactly the answer the model needs to see.
type CheckURL struct{}

func (CheckURL) Name() string         { return "check_url" }
func (CheckURL) Mode() agent.ToolMode { return agent.Concurrent }
func (CheckURL) Description() string {
	return "Make an HTTP GET request to a URL and report the status code, or the connection " +
		"error if it's not reachable. Use this to verify a server is actually up instead of " +
		"trusting its startup output."
}
func (CheckURL) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"url": {"type": "string"}},
		"required": ["url"]
	}`)
}

func (CheckURL) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	clean, err := normalizeLoopbackURL(in.URL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clean, nil)
	if err != nil {
		return "", fmt.Errorf("bad url: %w", err)
	}

	resp, err := NetworkClient(true).Do(req)
	if err != nil {
		// Not a tool failure — this IS the diagnostic the model asked for.
		return fmt.Sprintf("HTTP\nurl: %s\nerror: %v", in.URL, err), nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, checkURLBodyMax))
	return formatHTTPResult(in.URL, resp.StatusCode, resp.Status, body), nil
}

func formatHTTPResult(url string, code int, status string, body []byte) string {
	var b strings.Builder
	b.WriteString("HTTP\n")
	fmt.Fprintf(&b, "url: %s\n", url)
	fmt.Fprintf(&b, "status: %d %s\n", code, status)
	b.WriteString("body-prefix:\n")
	b.Write(body)
	if len(body) == checkURLBodyMax {
		b.WriteString("\n...(body-prefix truncated)")
	}
	return b.String()
}
