package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/keshon/tars/internal/agent"
)

// mcpTimeout bounds a full discovery (initialize + tools/list) per server.
const mcpTimeout = 20 * time.Second

// mcpRestartBackoff caps the wait before a crashed stdio server is
// restarted. A variable (not a const) so tests do not sleep through it.
var mcpRestartBackoff = 10 * time.Second

// mcpMaxToolName caps an exposed tool name. Model tool namespaces are
// small, and a 200-byte server tool name would eat context on every step.
const mcpMaxToolName = 64

// mcpResultMax caps one MCP tool result in context; the wire may carry up
// to mcpWireMax before truncation.
const (
	mcpWireMax   = 10 * 1024 * 1024
	mcpResultMax = 48 * 1024
)

// MCPClient speaks JSON-RPC 2.0 to one MCP server over stdio.
type MCPClient struct {
	server  string
	cmd     *exec.Cmd
	stdin   *bufio.Writer
	stdout  *bufio.Reader
	mu      sync.Mutex
	seq     int
	pending map[int]chan json.RawMessage
	scanErr error

	// restarter rebuilds a dead transport: new command, pipes and
	// handshake, reusing this client's identity and sequence. Nil means
	// no restart (a dead server stays dead and calls fail). Set by
	// StartMCP; tests inject fakes.
	restarter func(ctx context.Context) (stdin io.Writer, stdout io.Reader, cmd *exec.Cmd, err error)
}

// mcpCaller is what an exposed tool needs: one JSON-RPC call. Both the
// stdio client and the HTTP client satisfy it, so MCPTool never learns
// which transport carries it.
type mcpCaller interface {
	call(ctx context.Context, method string, params any, out any) error
}

// MCPToolDef is one remote tool advertised by tools/list.
type MCPToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// StartMCP launches command with args and runs initialize + tools/list,
// returning one agent.Tool per remote tool named mcp__server__tool.
func StartMCP(ctx context.Context, server, command string, args ...string) ([]agent.Tool, *MCPClient, error) {
	ctx, cancel := context.WithTimeout(ctx, mcpTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("mcp %s: stdin: %w", server, err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("mcp %s: stdout: %w", server, err)
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("mcp %s: start %s: %w", server, command, err)
	}
	tools, c, err := connectMCP(ctx, server, cmd, stdinPipe, stdoutPipe)
	if err != nil {
		return nil, nil, err
	}
	c.restarter = func(ctx context.Context) (io.Writer, io.Reader, *exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, command, args...)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, nil, err
		}
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			return nil, nil, nil, err
		}
		return stdin, stdout, cmd, nil
	}
	return tools, c, nil
}

// connectMCP runs the handshake over an already-started transport. Tests
// use it with in-process pipes and a fake server instead of a subprocess.
func connectMCP(ctx context.Context, server string, cmd *exec.Cmd, stdin io.Writer, stdout io.Reader) ([]agent.Tool, *MCPClient, error) {
	c := &MCPClient{
		server:  server,
		cmd:     cmd,
		stdin:   bufio.NewWriter(stdin),
		stdout:  bufio.NewReader(stdout),
		pending: map[int]chan json.RawMessage{},
	}
	go c.readLoop()

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
		c.Close()
		return nil, nil, fmt.Errorf("mcp %s: initialize: %w", server, err)
	}
	_ = c.notify(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})

	var listResult struct {
		Tools []MCPToolDef `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", map[string]any{}, &listResult); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("mcp %s: tools/list: %w", server, err)
	}
	names := assignToolNames(server, listResult.Tools)
	tools := make([]agent.Tool, 0, len(listResult.Tools))
	for i, td := range listResult.Tools {
		tools = append(tools, &MCPTool{client: c, server: server, def: td, toolName: names[i]})
	}
	return tools, c, nil
}

// assignToolNames maps remote definitions to exposed names of the form
// mcp__server__tool, capped at mcpMaxToolName with _2/_3 dedup. A server
// advertising two "read" tools (or one 200-byte name) must not collide
// with or shadow its siblings — silent shadowing would route a call to
// the wrong remote tool with no visible trace.
func assignToolNames(server string, defs []MCPToolDef) []string {
	const prefix = "mcp__"
	seen := map[string]int{}
	out := make([]string, 0, len(defs))
	for _, td := range defs {
		base := prefix + server + "__" + td.Name
		if len(base) > mcpMaxToolName {
			keep := mcpMaxToolName - len(prefix) - len(server) - len("__")
			if keep < 1 {
				keep = 1
			}
			base = prefix + server + "__" + td.Name[:min(keep, len(td.Name))]
		}
		name := base
		if n := seen[base]; n > 0 {
			suffix := fmt.Sprintf("_%d", n+1)
			trim := len(base) + len(suffix) - mcpMaxToolName
			if trim > 0 {
				name = base[:len(base)-trim] + suffix
			} else {
				name = base + suffix
			}
		}
		seen[base]++
		out = append(out, name)
	}
	return out
}

// Close kills the server process.
func (c *MCPClient) Close() {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

func (c *MCPClient) nextID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	return c.seq
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *MCPClient) send(req rpcRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := c.stdin.Write(data); err != nil {
		return err
	}
	return c.stdin.Flush()
}

func (c *MCPClient) readLoop() {
	sc := bufio.NewScanner(c.stdout)
	sc.Buffer(make([]byte, 1024*1024), mcpWireMax)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) > mcpWireMax {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if resp.ID == 0 && resp.Result == nil {
			continue // notification or malformed; nothing waits on it
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if !ok {
			continue
		}
		if resp.Error != nil {
			ch <- json.RawMessage(fmt.Sprintf(`{"error":%q}`, resp.Error.Message))
		} else {
			ch <- resp.Result
		}
	}
	c.mu.Lock()
	err := sc.Err()
	if err == nil {
		err = fmt.Errorf("server stdout closed")
	}
	c.scanErr = err
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- json.RawMessage(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	c.mu.Unlock()
}

func (c *MCPClient) call(ctx context.Context, method string, params any, out any) error {
	req := rpcRequest{JSONRPC: "2.0", ID: c.nextID(), Method: method, Params: params}
	if err := c.callOnce(ctx, req, method, out); err != nil {
		if c.restarter == nil || !isTransportDead(err) {
			return err
		}
		if rerr := c.restartTransport(ctx); rerr != nil {
			return fmt.Errorf("%s: %v (server restart: %v)", method, err, rerr)
		}
		return c.callOnce(ctx, req, method, out)
	}
	return nil
}

// isTransportDead reports send-side failures that a restart can fix: the
// server is gone. Anything else (bad args, server-reported errors,
// timeouts) would fail identically against a fresh server.
func isTransportDead(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, m := range []string{
		"broken pipe", "closed pipe", "connection reset", "connection refused",
		"use of closed", "file already closed", "eof",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// restartTransport waits out the backoff, rebuilds a dead stdio server
// and re-runs the initialize handshake (tool definitions are cached, so
// tools/list is skipped). At most one restart per call: if the fresh
// server fails too, the error surfaces.
func (c *MCPClient) restartTransport(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(mcpRestartBackoff):
	}
	stdin, stdout, cmd, err := c.restarter(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cmd = cmd
	c.stdin = bufio.NewWriter(stdin)
	c.stdout = bufio.NewReader(stdout)
	c.mu.Unlock()
	go c.readLoop()
	var initResult struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	req := rpcRequest{JSONRPC: "2.0", ID: c.nextID(), Method: "initialize", Params: map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tars", "version": "1.0"},
	}}
	if err := c.callOnce(ctx, req, "initialize", &initResult); err != nil {
		c.Close()
		return err
	}
	_ = c.notify(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})
	return nil
}

func (c *MCPClient) callOnce(ctx context.Context, req rpcRequest, method string, out any) error {
	ch := make(chan json.RawMessage, 1)
	c.mu.Lock()
	c.pending[req.ID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
	}()
	if err := c.send(req); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case raw := <-ch:
		var probe struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &probe) == nil && probe.Error != "" {
			return fmt.Errorf("%s", probe.Error)
		}
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("decode %s result: %w", method, err)
			}
		}
		return nil
	}
}

func (c *MCPClient) notify(msg map[string]any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := c.stdin.Write(data); err != nil {
		return err
	}
	return c.stdin.Flush()
}

// MCPTool is one remote MCP tool exposed as an agent tool.
type MCPTool struct {
	client   mcpCaller
	server   string
	def      MCPToolDef
	toolName string
}

func (t *MCPTool) Name() string { return t.toolName }
func (t *MCPTool) Mode() agent.ToolMode {
	return agent.Exclusive
}
func (t *MCPTool) Description() string {
	desc := strings.TrimSpace(t.def.Description)
	if desc == "" {
		desc = "MCP tool " + t.def.Name
	}
	return fmt.Sprintf("[MCP %s] %s", t.server, desc)
}
func (t *MCPTool) Schema() json.RawMessage {
	if len(t.def.InputSchema) > 0 {
		return t.def.InputSchema
	}
	return json.RawMessage(`{"type":"object"}`)
}

func (t *MCPTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var result struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := t.client.call(ctx, "tools/call", map[string]any{
		"name": t.def.Name, "arguments": params,
	}, &result); err != nil {
		return "", fmt.Errorf("mcp %s/%s: %w", t.server, t.def.Name, err)
	}
	var b strings.Builder
	for _, c := range result.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
			b.WriteString("\n")
		} else if c.Type == "resource" || c.Type == "image" {
			if !allowedMCPMime(c.MimeType) {
				fmt.Fprintf(&b, "[omitted %s content: %s]\n", c.Type, c.MimeType)
				continue
			}
			b.WriteString("[binary content omitted: " + c.MimeType + "]\n")
		}
	}
	out := strings.TrimRight(b.String(), "\n")
	if result.IsError {
		return out, fmt.Errorf("mcp %s/%s reported an error", t.server, t.def.Name)
	}
	if len(out) > mcpResultMax {
		// Structured output reduces; prose truncates. A blind middle-cut
		// through a JSON array leaves half an element to parse, so try
		// the reducer first and fall back to truncation for non-JSON.
		if reduced := ReduceJSON([]byte(out), mcpResultMax); reduced != out {
			out = reduced + "\n(reduced from a larger MCP result — refine the query for detail)"
		} else {
			out = agent.TruncateMiddle(out, mcpResultMax) +
				fmt.Sprintf("\n...(mcp result truncated at %d bytes)", mcpResultMax)
		}
	}
	if out == "" {
		return "(empty result)", nil
	}
	return out, nil
}

func allowedMCPMime(m string) bool {
	m = strings.ToLower(m)
	return strings.HasPrefix(m, "text/") || m == "application/json"
}

// ParseMCPFlag parses -mcp "name=cmd args...;name2=cmd2" into server specs.
func ParseMCPFlag(spec string) (names, commands []string, args [][]string) {
	for _, part := range strings.Split(spec, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, cmdline := part, ""
		if i := strings.Index(part, "="); i >= 0 {
			name = strings.TrimSpace(part[:i])
			cmdline = strings.TrimSpace(part[i+1:])
		} else {
			fields := strings.Fields(part)
			if len(fields) == 0 {
				continue
			}
			name, cmdline = fields[0], part
		}
		fields := strings.Fields(cmdline)
		if len(fields) == 0 || name == "" {
			continue
		}
		names = append(names, name)
		commands = append(commands, fields[0])
		args = append(args, fields[1:])
	}
	return names, commands, args
}
