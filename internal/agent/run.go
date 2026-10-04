package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
)

// run is the single authoritative execution loop: one model turn per
// iteration, dispatched to finish handling (text answers) or tool
// execution (tool calls). Stage functions own their details — this
// function owns only the order: chat, recover, bookkeeping, dispatch,
// interject, persist. See handleFinish (finish.go) and executeToolCalls
// (tools.go).
func (a *Agent) run(ctx context.Context, history []llm.Message) (string, error) {
	var st runState
	a.LastRunMutations = 0
	a.report = RunReport{MaxSteps: a.cfg.MaxSteps}

	for step := 0; step < a.cfg.MaxSteps+st.todoFunded; step++ {
		resp, err := a.chat(ctx, llm.ChatRequest{
			Messages:  history,
			Tools:     a.cfg.Tools.Defs(),
			MaxTokens: a.effectiveMaxTokens(st.lastPromptTokens),
		})
		if err != nil {
			history, err = a.handleChatError(&st, step, err, history)
			if err != nil {
				return "", err
			}
			continue
		}
		resp, leakedText := a.recoverLeakedCalls(resp, step)
		if a.cfg.OnStep != nil {
			a.cfg.OnStep(step, resp.Message)
		}
		// Usage rides alongside OnStep rather than inside it: changing
		// OnStep's signature would break every caller (roles, mission,
		// eval, CLI) for data only some observers want.
		if a.cfg.OnUsage != nil {
			a.cfg.OnUsage(step, resp.Usage)
		}
		history = append(history, resp.Message)
		st.toolCalls += len(resp.Message.ToolCalls)
		budgetNudge := a.budgetWarning(resp.Usage, &st.warnedThreshold)
		if resp.Usage.PromptTokens > 0 {
			st.lastPromptTokens = resp.Usage.PromptTokens
		}
		a.report.Steps = step + 1
		a.report.LastPromptTokens = st.lastPromptTokens
		a.maybeCompact(&history, resp.Usage, &st)
		a.saveState(history)

		if len(resp.Message.ToolCalls) == 0 {
			out := a.handleFinish(ctx, &st, resp, leakedText, budgetNudge, history)
			history = out.history
			if out.done {
				return out.answer, out.err
			}
			continue
		}
		repeat, newHistory := a.executeToolCalls(ctx, &st, step, resp.Message.ToolCalls, history)
		history = newHistory
		if msg := a.interject(&st, stepOutcome{repeat: repeat, budget: budgetNudge}); msg != "" {
			history = append(history, llm.Message{Role: llm.RoleUser, Content: msg})
		}
		a.saveState(history)
	}

	a.LastRunMutations = st.mutatingSucceeded
	a.report.MutatedPaths = st.mutatedPaths
	return "", fmt.Errorf("%w (%d) without finishing", ErrMaxSteps, a.cfg.MaxSteps+st.todoFunded)
}

// handleChatError recovers from context overflow by compacting history
// and continuing with a nudge; anything else is infrastructure —
// mission treats it as resumable errInfra. Returns the history to
// continue with, or a fatal error.
//
// The step-0 case matters: the first call can overflow (a huge task
// plus system prompt on a small window), when history holds nothing
// compactable. Then there is nothing to drop, but the run must still
// continue with the nudge rather than die — probe 18 failed exactly
// this way before the guard was split.
func (a *Agent) handleChatError(st *runState, step int, err error, history []llm.Message) ([]llm.Message, error) {
	if !llm.IsOverflow(err) {
		return nil, fmt.Errorf("step %d: chat: %w", step, err)
	}
	// A full context is recoverable: compact aggressively and let
	// the next step continue with the summary.
	if len(history) > 2 {
		keep := a.cfg.CompactKeepSteps
		if keep <= 0 {
			keep = 8
		}
		// Caches drop only when history actually shrank: the
		// refusal texts cite content that must really be gone.
		if next := compactHistory(history, keep/2); len(next) < len(history) {
			history = next
			dropRepeatCaches(st)
		}
		a.saveState(history)
	}
	history = a.nudge(history, NudgeOverflow, prompts.OverflowRecovered)
	return history, nil
}

// recoverLeakedCalls extracts well-formed tool calls a model wrote as
// text instead of making structured ones. Recovered calls run through
// the normal tool path below - guards, permissions, repeat detection
// and all. Unparseable remainders keep the nudge path in the
// no-tool-calls branch; the returned verdict carries that so the branch
// does not re-detect. Length-truncated stumps skip recovery: a cutoff
// generation is not a hidden call.
func (a *Agent) recoverLeakedCalls(resp llm.ChatResponse, step int) (llm.ChatResponse, bool) {
	var leakedText bool
	if len(resp.Message.ToolCalls) == 0 && resp.FinishReason != "length" {
		known := map[string]bool{}
		if a.cfg.Tools != nil {
			for _, n := range a.cfg.Tools.Names() {
				known[n] = true
			}
		}
		var recovered []llm.LeakedCall
		var cleaned string
		recovered, cleaned, leakedText = llm.ExtractLeakedCalls(resp.Message.Content, known)
		for i, rc := range recovered {
			resp.Message.ToolCalls = append(resp.Message.ToolCalls, llm.ToolCall{
				ID:        llm.LeakCallID(step, i),
				Name:      rc.Name,
				Arguments: rc.Arguments,
			})
		}
		resp.Message.Content = cleaned
	}
	return resp, leakedText
}

// chat runs one model turn: streaming when configured and supported,
// otherwise ChatWithRetry. Streaming gets a single attempt (a partial
// stream cannot resume mid-turn); the unary path retries.
func (a *Agent) chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	if a.cfg.Stream {
		if s, ok := a.cfg.Client.(interface {
			Stream(context.Context, llm.ChatRequest, func(string)) (llm.ChatResponse, error)
		}); ok {
			resp, err := s.Stream(ctx, req, a.cfg.OnDelta)
			if err == nil {
				return resp, err
			}
			if !strings.Contains(strings.ToLower(err.Error()), "streaming unsupported") {
				return resp, err
			}
		}
	}
	return llm.ChatWithRetry(ctx, a.cfg.Client, req, llm.DefaultRetryPolicy())
}
