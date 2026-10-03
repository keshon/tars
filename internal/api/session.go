// Package api is the programmatic way to run the agent: a Session that
// emits the same events -mode json prints, and answers gates through a
// Suspender instead of stdin. The TUI (P9) and the stdio JSON-RPC server
// both build on it; neither touches the loop directly.
//
// Schema parity is structural, not promised: Session reuses the events
// package to serialize, and parses the JSONL back, so -mode json output
// and Session events cannot drift apart without a test failing.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/checks"
	"github.com/keshon/tars/internal/events"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/workspace"
)

// Event is one decoded JSONL event: the name plus its fields, including
// the emitter-assigned seq.
type Event struct {
	Name   string
	Seq    int
	Fields map[string]any
}

// Config wires a direct-mode run. Everything the harness needs comes
// from Env (client, workspace, tools, policy, budgets); Session adds
// the task, the event sink, and the gate transport.
type Config struct {
	Env roles.Env

	Task      string
	StateFile string
	Verify    func(ctx context.Context) (output string, ok bool)

	// Answer transports blocked gates. Nil means ClosedSuspender:
	// headless runs fail closed instead of hanging on stdin.
	Answer agent.Suspender

	// OnEvent receives every event in order. Nil drops them.
	OnEvent func(Event)

	// MaxQuestions bounds ask_user rounds like the CLI does. Zero takes
	// the CLI default.
	MaxQuestions int
}

// Session runs one task. Create per run; it holds no global state.
type Session struct {
	cfg       Config
	questions int
}

const defaultMaxQuestions = 3

// New validates the config without running anything.
func New(cfg Config) (*Session, error) {
	if strings.TrimSpace(cfg.Task) == "" {
		return nil, fmt.Errorf("api: task is empty")
	}
	if cfg.Env.Client == nil {
		return nil, fmt.Errorf("api: Env.Client is nil")
	}
	if cfg.Env.WS == nil {
		return nil, fmt.Errorf("api: Env.WS is nil")
	}
	if cfg.MaxQuestions == 0 {
		cfg.MaxQuestions = defaultMaxQuestions
	}
	if cfg.Answer == nil {
		cfg.Answer = agent.ClosedSuspender("api session without operator")
	}
	return &Session{cfg: cfg}, nil
}

// Run executes the task to completion and returns the final answer.
// Framing (run start/end) is the caller's job: in-process callers have
// call boundaries, and wire transports add their own. Session streams
// step events only.
func (s *Session) Run(ctx context.Context) (string, error) {
	emitter := events.New(&callbackWriter{onEvent: s.cfg.OnEvent})

	askFn := s.asker(ctx, s.cfg.MaxQuestions)

	env := s.cfg.Env
	prevOnStep := env.OnStep
	prevOnResult := env.OnToolResult
	prevOnUsage := env.OnUsage
	// expectReply marks the next text-only step as harness-caused: a
	// nudge was just delivered, so the following bare answer reacts to
	// the harness, not the operator. One-shot and text-only: tool calls
	// are work, whatever prompted them.
	expectReply := false
	env.OnStep = func(label string, step int, msg llm.Message) {
		hr := expectReply && len(msg.ToolCalls) == 0
		expectReply = false
		EmitStep(emitter, label, step, msg, hr)
		if prevOnStep != nil {
			prevOnStep(label, step, msg)
		}
	}
	env.OnToolResult = func(callID, result string) {
		EmitToolResult(emitter, callID, result)
		if prevOnResult != nil {
			prevOnResult(callID, result)
		}
	}
	env.OnUsage = func(step int, usage llm.Usage) {
		EmitUsage(emitter, step, usage)
		if prevOnUsage != nil {
			prevOnUsage(step, usage)
		}
	}
	prevOnNudge := env.OnNudge
	env.OnNudge = func(kind, text string) {
		EmitNudge(emitter, kind, text)
		expectReply = true
		if prevOnNudge != nil {
			prevOnNudge(kind, text)
		}
	}
	prevOnDelta := env.OnDelta
	env.OnDelta = func(chunk string) {
		EmitDelta(emitter, chunk)
		if prevOnDelta != nil {
			prevOnDelta(chunk)
		}
	}
	drain := newFindingDrain(emitter, env.OnFinding)
	env.OnFinding = drain.reportPerEdit
	a := roles.Interactive(env, "", s.cfg.StateFile, askFn, s.cfg.Verify)
	ans, err := a.Run(ctx, s.cfg.Task)
	drain.sweep(s.cfg.Env.WS, a.MutatedPaths())
	return ans, err
}

// asker builds the ask_user implementation over the session suspender,
// sharing one question budget across Run and Resume calls. Resume runs
// used to refuse all questions; a follow-up that needs clarification
// should ask like any other turn.
func (s *Session) asker(ctx context.Context, maxQ int) func(string) (string, error) {
	return func(question string) (string, error) {
		s.questions++
		if s.questions > maxQ {
			return "", fmt.Errorf("ask_user: clarifying question limit (%d) reached — "+
				"make a decision and proceed with a stated assumption", maxQ)
		}
		rep, err := s.cfg.Answer(ctx, agent.SuspendRequest{
			Kind:   agent.SuspendAsk,
			Prompt: question,
		})
		if err != nil {
			return "", err
		}
		return rep.Answer, nil
	}
}

// Resume continues from saved history with a note, mirroring Agent.Resume.
func (s *Session) Resume(ctx context.Context, history []llm.Message, note string) (string, error) {
	emitter := events.New(&callbackWriter{onEvent: s.cfg.OnEvent})
	env := s.cfg.Env
	prevOnStep := env.OnStep
	prevOnResult := env.OnToolResult
	prevOnUsage := env.OnUsage
	// expectReply marks the next text-only step as harness-caused: a
	// nudge was just delivered, so the following bare answer reacts to
	// the harness, not the operator. One-shot and text-only: tool calls
	// are work, whatever prompted them.
	expectReply := false
	env.OnStep = func(label string, step int, msg llm.Message) {
		hr := expectReply && len(msg.ToolCalls) == 0
		expectReply = false
		EmitStep(emitter, label, step, msg, hr)
		if prevOnStep != nil {
			prevOnStep(label, step, msg)
		}
	}
	env.OnToolResult = func(callID, result string) {
		EmitToolResult(emitter, callID, result)
		if prevOnResult != nil {
			prevOnResult(callID, result)
		}
	}
	env.OnUsage = func(step int, usage llm.Usage) {
		EmitUsage(emitter, step, usage)
		if prevOnUsage != nil {
			prevOnUsage(step, usage)
		}
	}
	prevOnNudge := env.OnNudge
	env.OnNudge = func(kind, text string) {
		EmitNudge(emitter, kind, text)
		expectReply = true
		if prevOnNudge != nil {
			prevOnNudge(kind, text)
		}
	}
	prevOnDelta := env.OnDelta
	env.OnDelta = func(chunk string) {
		EmitDelta(emitter, chunk)
		if prevOnDelta != nil {
			prevOnDelta(chunk)
		}
	}
	drain := newFindingDrain(emitter, env.OnFinding)
	env.OnFinding = drain.reportPerEdit
	a := roles.Interactive(env, "", s.cfg.StateFile, s.asker(ctx, s.cfg.MaxQuestions), s.cfg.Verify)
	ans, err := a.Resume(ctx, history, note)
	drain.sweep(s.cfg.Env.WS, a.MutatedPaths())
	return ans, err
}

// callbackWriter decodes each JSONL line back into an OnEvent call,
// preserving the emitter-assigned sequence numbers. Serializing through
// the events encoding keeps -mode json output and Session events on one
// schema by construction: they cannot drift apart.
type callbackWriter struct {
	buf     bytes.Buffer
	onEvent func(Event)
}

func (w *callbackWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if err != nil {
		return n, err
	}
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			break
		}
		var rec map[string]any
		if jerr := json.Unmarshal([]byte(line), &rec); jerr != nil {
			continue
		}
		name, _ := rec["event"].(string)
		ev := Event{Name: name, Fields: map[string]any{}}
		for k, v := range rec {
			switch k {
			case "event":
			case "seq":
				if f, ok := v.(float64); ok {
					ev.Seq = int(f)
				}
			default:
				ev.Fields[k] = v
			}
		}
		if w.onEvent != nil {
			w.onEvent(ev)
		}
	}
	return n, nil
}

// EmitStep writes one step as a JSONL event on emitter. Shared by the
// direct loop, mission workers, and Session so all three speak the same
// schema. events.Message caps each text field. harnessReply marks a
// text-only answer to a harness nudge (first reply is enough); the
// field is omitted when false so old consumers see no change.
func EmitStep(emitter *events.Emitter, label string, step int, msg llm.Message, harnessReply bool) {
	if emitter == nil {
		return
	}
	ev := map[string]any{"step": step, "label": label}
	if harnessReply {
		ev["harness_reply"] = true
	}
	if msg.Content != "" {
		ev["text"] = events.Message(msg.Content)
	}
	// Deliberation travels with the step that produced it, capped like
	// every other field. Observers render it dimmed or not at all; the
	// budget in the loop is what constrains it, not display.
	if msg.Reasoning != "" {
		ev["reasoning"] = events.Message(msg.Reasoning)
	}
	calls := make([]any, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		calls = append(calls, map[string]any{
			// The id pairs results to calls (duplicate ids across
			// steps match the most recent open card; see attachResult).
			"id": tc.ID, "name": tc.Name, "args": events.Message(string(tc.Arguments)),
		})
	}
	if len(calls) > 0 {
		ev["tool_calls"] = calls
	}
	emitter.Emit("step", ev)
}

// EmitFinding writes one deterministic-check finding as a JSONL event.
// Scope is "per-edit" (post-write scan) or "session-end" (deep sweep).
func EmitFinding(emitter *events.Emitter, scope, rule, path string, line int, summary string) {
	if emitter == nil {
		return
	}
	emitter.Emit("finding", map[string]any{
		"scope": scope, "rule": rule, "path": path, "line": line, "summary": summary,
	})
}

// EmitNudge writes one loop-generated harness notice as a JSONL event:
// the same text the model saw (including its [harness] provenance
// mark), tagged by kind for display filtering.
func EmitNudge(emitter *events.Emitter, kind, text string) {
	if emitter == nil {
		return
	}
	emitter.Emit("nudge", map[string]any{"kind": kind, "text": text})
}

// EmitDelta writes one streamed content chunk as a JSONL event. Deltas
// are display-only: the step event carries the authoritative text and
// replaces whatever the live buffer showed.
func EmitDelta(emitter *events.Emitter, text string) {
	if emitter == nil {
		return
	}
	emitter.Emit("delta", map[string]any{"text": text})
}

// maxSweepFiles bounds the session-end deep pass; maxSweepFindings
// bounds its noise. Both overflow as named marker findings, never
// silent drops.
const (
	maxSweepFiles    = 64
	maxSweepFindings = 20
)

// findingDrain funnels deterministic-check findings into finding events
// with cross-tier dedupe: per-edit scans and the session-end sweep
// share one cache, so a file dirty throughout is reported once, not
// once per phase that noticed it.
type findingDrain struct {
	emit  *events.Emitter
	cache *checks.Cache
	prev  func(rule, path string, line int, summary string)
}

func newFindingDrain(emitter *events.Emitter, prev func(rule, path string, line int, summary string)) *findingDrain {
	return &findingDrain{emit: emitter, cache: &checks.Cache{}, prev: prev}
}

// reportPerEdit records one post-tool finding, emitted once.
func (d *findingDrain) reportPerEdit(rule, path string, line int, summary string) {
	d.report("per-edit", rule, path, line, summary)
}

// report emits one finding unless already reported, then chains the
// previous sink (CLI printer) for emitted findings only: suppressed
// stays silent everywhere, not just in events.
func (d *findingDrain) report(scope, rule, path string, line int, summary string) {
	kept := d.cache.Filter([]checks.Finding{{Rule: rule, Path: path, Line: line, Summary: summary}})
	for _, f := range kept {
		EmitFinding(d.emit, scope, f.Rule, f.Path, f.Line, f.Summary)
	}
	if len(kept) > 0 && d.prev != nil {
		d.prev(rule, path, line, summary)
	}
}

// sweep runs the session-end deep pass over mutated paths: files the
// run changed through any tool, including shell redirections, MCP
// writes, and background jobs, which per-edit scans never see
// (they only scan tool-written content addressed by "path").
func (d *findingDrain) sweep(ws *workspace.Workspace, paths []string) {
	if ws == nil || len(paths) == 0 {
		return
	}
	if len(paths) > maxSweepFiles {
		d.report("session-end", "sweep-skipped", "", 0, fmt.Sprintf("%d files changed; sweep bound at %d", len(paths), maxSweepFiles))
		return
	}
	var all []checks.Finding
	for _, p := range paths {
		full, err := ws.Resolve(p)
		if err != nil {
			continue // deleted or escaped since: nothing to scan
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		all = append(all, checks.ScanFile(p, data)...)
	}
	kept := d.cache.Filter(all)
	capped, dropped := checks.Clamp(kept, maxSweepFindings)
	for _, f := range capped {
		d.report("session-end", f.Rule, f.Path, f.Line, f.Summary)
	}
	if dropped > 0 {
		d.report("session-end", "sweep-truncated", "", 0, fmt.Sprintf("and %d more findings withheld", dropped))
	}
}

// EmitToolResult writes one completed tool call as a JSONL event.
// Callers pair it with the matching tool_calls entry by call ID; the
// result text is capped like every other field.
func EmitToolResult(emitter *events.Emitter, callID string, result string) {
	if emitter == nil {
		return
	}
	emitter.Emit("tool_result", map[string]any{
		"call_id": callID,
		"text":    events.Message(result),
	})
}

// EmitUsage writes backend token counts as a JSONL event. Observers that
// meter context (TUIs, JSON streams) accumulate the latest prompt count
// against the known window; the loop itself budgets off the same numbers.
func EmitUsage(emitter *events.Emitter, step int, usage llm.Usage) {
	if emitter == nil {
		return
	}
	emitter.Emit("usage", map[string]any{
		"step":       step,
		"prompt":     usage.PromptTokens,
		"completion": usage.CompletionTokens,
		"cached":     usage.CachedTokens,
		"estimated":  usage.Estimated,
	})
}
