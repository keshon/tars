package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/keshon/tars/internal/agent"
)

// httpMCPTimeout bounds one HTTP exchange. Streamable HTTP is
// request/response per call (no persistent stream), so this is a plain
// per-call deadline, not a session lifetime.
const httpMCPTimeout = 60 * time.Second

// httpMCPClient speaks JSON-RPC 2.0 to one MCP server over Streamable
// HTTP: each call is a POST; the answer is a single JSON object or an
// SSE stream of `data:` lines. No subprocess, so no restart: every call
// is already independent, and an expired session is renewed inline.
type httpMCPClient struct {
	server  string
	url     string
	http    *http.Client
	mu      sync.Mutex
	seq     int
	session string
}

func (c *httpMCPClient) nextID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	return c.seq
}

func (c *httpMCPClient) sessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

func (c *httpMCPClient) setSession(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == "" {
		c.session = id
	}
}

// StartMCPHTTP runs initialize + tools/list against a Streamable HTTP
// endpoint and returns one agent.Tool per remote tool, named exactly like
// the stdio ones (mcp__server__tool, deduped and capped). rawURL must be
// absolute http(s); loopback is permitted (local dev servers), everything
// else follows the public-URL rules.
func StartMCPHTTP(ctx context.Context, server, rawURL string) ([]agent.Tool, *httpMCPClient, error) {
	clean, err := normalizeLoopbackURL(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp %s: %w", server, err)
	}
	ctx, cancel := context.WithTimeout(ctx, mcpTimeout)
	defer cancel()

	c := &httpMCPClient{
		server: server,
		url:    clean,
		http:   &http.Client{Timeout: httpMCPTimeout},
	}
	var initResult struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tars", "version": "1.0"},
	}, &initResult); err != nil {
		return nil, nil, fmt.Errorf("mcp %s: initialize: %w", server, err)
	}
	_ = c.notify(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})

	var listResult struct {
		Tools []MCPToolDef `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", map[string]any{}, &listResult); err != nil {
		return nil, nil, fmt.Errorf("mcp %s: tools/list: %w", server, err)
	}
	names := assignToolNames(server, listResult.Tools)
	tools := make([]agent.Tool, 0, len(listResult.Tools))
	for i, td := range listResult.Tools {
		tools = append(tools, &MCPTool{client: c, server: server, def: td, toolName: names[i]})
	}
	return tools, c, nil
}

// Close ends the session server-side when the protocol supports it.
// Best-effort: a missing endpoint is normal, not an error.
func (c *httpMCPClient) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session := c.sessionID()
	if session == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", session)
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func (c *httpMCPClient) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: c.nextID(), Method: method, Params: params})
	if err != nil {
		return err
	}
	raw, session, err := c.post(ctx, body, true)
	if err != nil {
		return err
	}
	c.setSession(session)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

// post sends one message. allowRenew permits a single retry without the
// session id when the server answers 404 (session expired): the retry
// carries no session, the server starts a fresh one, and its id is
// captured from the answer like any other.
func (c *httpMCPClient) post(ctx context.Context, body []byte, allowRenew bool) (json.RawMessage, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session := c.sessionID(); session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && allowRenew {
		c.mu.Lock()
		c.session = ""
		c.mu.Unlock()
		return c.post(ctx, body, false)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, "", fmt.Errorf("mcp %s: HTTP %s: %s", c.server, resp.Status, strings.TrimSpace(string(raw)))
	}
	session := resp.Header.Get("Mcp-Session-Id")
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		raw, err := scanSSE(resp.Body)
		if err != nil {
			return nil, "", err
		}
		var rpc rpcResponse
		if err := json.Unmarshal(raw, &rpc); err != nil {
			return nil, "", fmt.Errorf("decode SSE response: %w", err)
		}
		if rpc.Error != nil {
			return nil, "", fmt.Errorf("%s", rpc.Error.Message)
		}
		return rpc.Result, session, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, mcpWireMax))
	if err != nil {
		return nil, "", err
	}
	var rpc rpcResponse
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return nil, "", fmt.Errorf("decode response: %w", err)
	}
	if rpc.Error != nil {
		return nil, "", fmt.Errorf("%s", rpc.Error.Message)
	}
	return rpc.Result, session, nil
}

func (c *httpMCPClient) notify(msg map[string]any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, err = c.post(ctx, body, false)
	return err
}

// scanSSE reads the last data: payload of an SSE stream: the final JSON-RPC
// message. Earlier chunks are progress the caller cannot use.
func scanSSE(r io.Reader) (json.RawMessage, error) {
	var last json.RawMessage
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), mcpWireMax)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		last = json.RawMessage(payload)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if last == nil {
		return nil, fmt.Errorf("empty SSE stream")
	}
	return last, nil
}

// IsMCPURL reports whether s is an http(s) URL (an MCP server address)
// rather than a command line.
func IsMCPURL(s string) bool {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return false
	}
	lower := strings.ToLower(fields[0])
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}
