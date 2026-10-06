// Command agent runs a single task against a local LLM backend.
//
//	agent "create a snake game in plain JS under ./game"
//	agent -debug "..."   # also writes agent-debug.log with raw request/response JSON
package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/audit"
	"github.com/keshon/tars/internal/events"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/mission"
	"github.com/keshon/tars/internal/permission"
	"github.com/keshon/tars/internal/prompts"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/session"
	"github.com/keshon/tars/internal/snapshot"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/tui"
	"github.com/keshon/tars/internal/workspace"
)

func main() {
	if err := runMain(); err != nil {
		log.Print(err)
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		os.Exit(1)
	}
}

func runMain() error {
	backend := flag.String("backend", "http://localhost:5001", "backend base URL: bare origin for a local server ("+
		"http://localhost:5001) or API root for a hosted OpenAI-compatible provider "+
		"(https://api.openai.com/v1, https://openrouter.ai/api/v1)")
	model := flag.String("model", "local", "model name: ignored by most local servers (they serve "+
		"whatever weights they started with), required for remote providers")
	root := flag.String("workspace", ".", "workspace root the agent may read/write")
	debug := flag.Bool("debug", false, "log raw request/response JSON to agent-debug.log")
	grammar := flag.Bool("grammar", true, "apply the backend's own content grammar. On koboldcpp "+
		"that blocks a model writing its native tool-call tags as plain text; on llama-server there "+
		"is none, because those tags are the protocol there and constraining them fights the "+
		"grammar the server builds from the tool schemas")
	maxTokens := flag.Int("max-tokens", 8192, "generation budget per response — too low truncates "+
		"large outputs (e.g. a full HTML+CSS+JS file) mid-JSON")
	maxSteps := flag.Int("max-steps", 0, "step budget per run — 0 takes the agent default (25). "+
		"Applies to direct runs; mission workers and subagents keep fixed budgets")
	thinkBudget := flag.Int("reasoning-budget", 0, "characters of <think> deliberation allowed per response "+
		"before a wrap-up round demands commitment; 0 takes the agent default (6000), negative disables wrapping")
	resume := flag.String("resume", "", "path to a .tars/tasks/.../state.json snapshot to resume "+
		"an interrupted run from, instead of starting a new task")
	answer := flag.String("answer", "", "answer to supply when resuming a run paused on ask_user "+
		"(use with -resume when the agent stopped to ask a clarifying question)")
	verifyCmd := flag.String("verify-cmd", "", "command to run at the self-check checkpoint "+
		"(e.g. \"go build ./... && go vet ./... && go test ./...\") — real output gets fed back "+
		"as fact instead of trusting the model's own claim that the code works. Empty disables this.")
	logMax := flag.Int("log-max", 300, "max characters per line in step console output; "+
		"long text is truncated in the middle (head ... tail). 0 = no limit")
	missionMode := flag.Bool("mission", false, "run the task as a mission: an upfront model-generated "+
		"plan (approved by you), then one fresh-context worker per subtask, each verified mechanically. "+
		"For complex multi-file tasks a weak model can't hold in its head; simple tasks are better off "+
		"without it. Multi-file tasks receive a recommendation; mission mode is explicit")
	direct := flag.Bool("direct", false, "suppress the multi-file mission recommendation (direct mode is the default)")
	yes := flag.Bool("yes", false, "skip the mission plan approval gate and run the plan as generated")
	auditPath := flag.String("audit", "", "append gate decisions as JSONL to this file (off when empty)")
	backendKind := flag.String("backend-kind", "kobold", "which server: "+
		strings.Join(llm.Kinds(), ", ")+". kobold/llama are local; openai is any hosted "+
		"OpenAI-compatible API (OpenAI, OpenRouter, DeepSeek, Groq, Together, Mistral, xAI). "+
		"A wrong value is caught at startup rather "+
		"than run: the dialects disagree about grammar, sampler fields and structured "+
		"output, so a mismatched run completes and measures nothing")
	apiKey := flag.String("api-key", "", "bearer token for a remote provider; prefer the "+
		"TARS_API_KEY / OPENAI_API_KEY / OPENROUTER_API_KEY env vars so the secret never lands in shell history")
	apiKeyEnv := flag.String("api-key-env", "", "name of the env var holding the bearer token, "+
		"e.g. OPENROUTER_API_KEY — overrides the built-in lookup order")
	contextLimitFlag := flag.Int("context-limit", 0, "context window in tokens, overriding the backend probe. "+
		"Required knowledge for -backend-kind openai, which has no probe endpoint: defaults to a "+
		"per-model estimate when 0")
	allowFlag := flag.String("allow", "", "comma-separated tool=pattern rules to allow, e.g. \"run_shell=go *,read_file=*.go\" (wins over defaults)")
	denyFlag := flag.String("deny", "", "comma-separated tool=pattern rules to deny, e.g. \"run_shell=rm *,read_file=.env\" (wins over -allow)")
	pureFlag := flag.Bool("pure", false, "ignore project config for permissions; use built-in defaults plus -allow/-deny only")
	streamFlag := flag.Bool("stream", false, "stream response tokens live (openai/llama backends only; koboldcpp falls back to unary)")
	mcpFlag := flag.String("mcp", "", "MCP servers: \"name=cmd args...;name2=https://host/mcp\" (stdio JSON-RPC or Streamable HTTP, tools appear as mcp__name__tool)")
	forkFlag := flag.String("fork", "", "history file to branch from: loads its transcript but writes to a fresh task id")
	revertFlag := flag.Bool("revert", false, "restore the pre-run workspace checkpoint and Git index, then exit")
	revertPreview := flag.Bool("revert-preview", false, "list checkpoint restore actions without changing files")
	revertFrom := flag.String("revert-from", "", "select a checkpoint archive for -revert or -revert-preview; default is the latest")
	planFlag := flag.Bool("plan", false, "plan mode: read-only tools, propose a plan and change nothing")
	modeFlag := flag.String("mode", "print", "output mode: print (human-readable) or json (one JSON object per line)")
	serveFlag := flag.Bool("serve", false, "serve JSON-RPC over stdio instead of running one task: methods run/respond/cancel, events on stdout. See docs/rpc.md")
	tuiFlag := flag.Bool("tui", false, "fullscreen terminal UI instead of print mode: live transcript, status, and gate prompts")
	noVerifyFlag := flag.Bool("no-verify", false, "skip the self-check verify round (finish accepted without the extra verification turn). Saves 1-2 model calls; strong models only")
	imageFlag := flag.String("image", "", "attach pictures to the task (comma-separated paths): the model sees them alongside the text. Needs a vision-capable backend (llama-server with --mmproj); koboldcpp refuses loudly")
	flag.Parse()

	task := strings.Join(flag.Args(), " ")
	if task == "" && *resume == "" && *forkFlag == "" && !*serveFlag && !*tuiFlag && !*revertFlag && !*revertPreview {
		return fmt.Errorf(`usage: agent [flags] "task description"  (or  agent -resume <state.json> [-answer "..."])`)
	}

	ws, wsErr := workspace.New(*root)
	if wsErr != nil {
		return fmt.Errorf("workspace: %w", wsErr)
	}

	// noteW is where human-readable chatter goes. In -mode json it is
	// stderr, so stdout carries nothing but JSONL and a caller can pipe
	// it straight into a parser; in print mode it is stdout as before.
	// -serve always uses stderr: stdout is the protocol.
	noteW := io.Writer(os.Stdout)
	if *modeFlag == "json" || *serveFlag {
		noteW = os.Stderr
	}
	note := func(format string, args ...any) {
		fmt.Fprintf(noteW, format+"\n", args...)
	}

	// The event emitter must exist before either mode branches: mission
	// output below needs it, and run_end must fire for both modes.
	var emitter *events.Emitter
	if *modeFlag == "json" {
		emitter = events.New(os.Stdout)
		emitter.Emit("run_start", map[string]any{
			"task": task, "backend": *backendKind, "model": *model,
			"context_limit": *contextLimitFlag, "workspace": *root,
		})
		defer emitter.Emit("run_end", nil)
	}

	// One suspender for every gate in the run: ask_user questions,
	// permission prompts, and the mission plan approval below all ask
	// through it. A single shared stdin reader matters — three buffered
	// readers on one stdin would eat each other's input.
	suspend := stdioSuspender(noteW, emitter)

	if !*missionMode && !*direct && task != "" && mission.SuggestMission(task) {
		note("task spans multiple files; use -mission for a reviewed execution plan")
	}

	// A -resume target whose directory holds mission.json is a mission
	// resume, whatever the path points at (the dir itself, mission.json,
	// or a worker state file) — direct-mode resume stays the fallback.
	missionDir := ""
	if *resume != "" {
		cand := *resume
		if info, err := os.Stat(cand); err != nil || !info.IsDir() {
			cand = filepath.Dir(cand)
		}
		if mission.Exists(cand) {
			missionDir = cand
		}
	}

	// Every run snapshots its history to disk after each step, so a crash
	// or hitting MaxSteps doesn't lose everything — see -resume. -fork
	// loads a prior transcript but writes to a fresh id (a branch).
	var stateFile string
	var forkHistory []llm.Message
	if *resume != "" {
		stateFile = *resume
		if info, err := os.Stat(stateFile); err == nil && info.IsDir() {
			stateFile = filepath.Join(stateFile, "state.json")
		}
	} else if !*revertFlag && !*revertPreview && !*serveFlag {
		sum := sha1.Sum([]byte(task + time.Now().String()))
		taskID := hex.EncodeToString(sum[:])[:8]
		taskDir := filepath.Join(ws.Root(), workspace.TaskDir(taskID))
		stateFile = filepath.Join(taskDir, "state.json")
		// The session title lives with the session (pi's session_info):
		// written once here, rewritten on TUI rename, derived from
		// history when absent. Display metadata, never load-bearing.
		_ = workspace.WriteSessionTitle(taskDir, workspace.TitleLine(task))
		if *forkFlag != "" {
			var err error
			forkHistory, err = loadHistory(*forkFlag)
			if err != nil {
				return fmt.Errorf("fork: %w", err)
			}
			note("forked from %s (%d messages)", *forkFlag, len(forkHistory))
		}
		if *missionMode {
			missionDir = taskDir
			note("mission id: %s (resume with -resume %s)", taskID, taskDir)
		} else {
			note("task id: %s (resume with -resume %s)", taskID, stateFile)
		}
	}

	if *revertFlag || *revertPreview {
		actions, err := snapshot.Preview(ws.Root(), *revertFrom)
		if err != nil {
			return fmt.Errorf("checkpoint: %w", err)
		}
		for _, action := range actions {
			fmt.Fprintln(noteW, action)
		}
		if *revertPreview {
			return nil
		}
		if *revertFrom != "" {
			err = snapshot.Restore(ws.Root(), *revertFrom)
		} else {
			err = snapshot.Revert(ws.Root())
		}
		if err != nil {
			return fmt.Errorf("revert: %w", err)
		}
		note("workspace restored to the pre-run checkpoint")
		return nil
	}

	client, err := llm.ClientFor(*backendKind, *backend, *model)
	if err != nil {
		return fmt.Errorf("%w", err)
	}
	resolveAPIKey(*apiKey, *apiKeyEnv, client)
	if hdrs := extraHeadersFromEnv(); len(hdrs) > 0 {
		if client.ExtraHeaders == nil {
			client.ExtraHeaders = hdrs
		} else {
			for k, v := range hdrs {
				client.ExtraHeaders[k] = v
			}
		}
	}
	client.NoGrammar = !*grammar
	if *debug {
		f, err := os.Create("agent-debug.log")
		if err != nil {
			return fmt.Errorf("open debug log: %w", err)
		}
		defer f.Close()
		client.Debug = f
		note("debug: logging raw request/response JSON to agent-debug.log")
	}

	// Ask the backend for its real context window instead of guessing.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var contextLimit int
	if *backendKind == "openai" {
		// Hosted gateways expose no probe endpoint, so the window comes
		// from the flag or the per-model estimate. A local server answering
		// here still means the kind is wrong, so keep that tripwire.
		if *contextLimitFlag > 0 {
			contextLimit = *contextLimitFlag
		} else {
			contextLimit = llm.OpenAIContextLimit(*model)
		}
		note("context window: %d tokens (estimated for %s; override with -context-limit)",
			contextLimit, *model)
		if actual := llm.DetectKind(ctx, *backend); actual != "" && actual != *backendKind {
			return fmt.Errorf("-backend-kind is %q but %s is answering at %s.\n"+
				"Re-run with -backend-kind %s. Continuing would send %s's grammar and\n"+
				"sampler fields to a server that ignores both, and the run would look fine.",
				*backendKind, actual, *backend, actual, *backendKind)
		}
	} else {
		var err error
		contextLimit, err = client.MaxContextLength(ctx)
		if err != nil {
			// The probe failing is evidence, not noise: each dialect asks a
			// different endpoint, so if the OTHER one answers, -backend-kind
			// is wrong. Continuing is worse than stopping, because the
			// dialects disagree about the grammar field, the sampler field
			// names and the structured-output mechanism: the run completes
			// having measured nothing. Nine mission runs did exactly that
			// before this check existed.
			if actual := llm.DetectKind(ctx, *backend); actual != "" && actual != *backendKind {
				return fmt.Errorf("-backend-kind is %q but %s is answering at %s.\n"+
					"Re-run with -backend-kind %s. Continuing would send %s's grammar and\n"+
					"sampler fields to a server that ignores both, and the run would look fine.",
					*backendKind, actual, *backend, actual, *backendKind)
			}
			// Otherwise the backend is simply not reporting: keep going
			// without budget tracking rather than failing the whole run.
			contextLimit = 0
		} else {
			note("context window: %d tokens", contextLimit)
		}
	}
	emitter.Emit("context", map[string]any{"context_limit": contextLimit})

	// Reuses mission.RunShellCommand for the same OS-aware shell choice as
	// tools.RunShell and mission shell checks — one exec shape everywhere.
	var verify func(ctx context.Context) (string, bool)
	if *verifyCmd != "" {
		verify = func(ctx context.Context) (string, bool) {
			out, err := mission.RunShellCommand(ctx, *verifyCmd, ws.Root())
			status := "PASSED"
			if err != nil {
				status = "FAILED"
			}
			return fmt.Sprintf("%s\ncommand: %s\n%s", status, *verifyCmd, out), true
		}
	}

	// Shared across both tool sets so a subagent's start_background and
	// the parent's check_background/stop_background see the same
	// processes — a process started by one half of the conversation
	// should be checkable/stoppable from the other.
	bgProcs := tools.NewBackgroundProcesses()
	defer bgProcs.StopAll()

	// Ctrl+C previously killed the agent and left whatever it had started
	// running: a dev server, a watcher, a test process. The eval harness
	// deferred StopAll and the binary people actually run did not, which
	// is also why the process-tree kill exists on Windows and was never
	// reached from here.
	//

	mcpTools, mcpClients := discoverMCP(ctx, *mcpFlag)
	defer func() {
		for _, c := range mcpClients {
			c.Close()
		}
	}()

	// ask_user blocks on stdin inside the tool. State is snapshotted by
	// the agent loop after the assistant message (with the unanswered tool
	// call) and before tool execution — so Ctrl+C while waiting leaves a
	// resumable state.json. Use -resume with -answer to continue.
	const maxClarifyingQuestions = 3
	questionCount := 0
	askFn := func(question string) (string, error) {
		questionCount++
		if questionCount > maxClarifyingQuestions {
			return "", fmt.Errorf("ask_user: clarifying question limit (%d) reached — "+
				"make a decision and proceed with a stated assumption", maxClarifyingQuestions)
		}
		rep, err := suspend(ctx, agent.SuspendRequest{
			Kind: agent.SuspendAsk,
			Prompt: fmt.Sprintf("\n[paused — state saved at %s]\n"+
				"[resume: agent -resume %s -answer \"your answer\"]\n"+
				"\n[agent asks] %s", stateFile, stateFile, question),
		})
		if err != nil {
			return "", err
		}
		return rep.Answer, nil
	}

	// Git snapshot before any work, so the run is reviewable/revertible.
	// Skipped in serve mode: each served run snapshots into its own
	// task dir instead, and this stateFile belongs to no run.
	if !*serveFlag {
		if snap := snapshot.Track(ws.Root(), filepath.Join(filepath.Dir(stateFile), "snapshots")); snap.Path != "" {
			note("snapshot: %s", snap.Path)
		} else {
			note("pre-run checkpoint unavailable; this run cannot be undone with -revert")
		}
	}

	if *serveFlag {
		// stdout is the protocol in serve mode regardless of -mode:
		// human chatter always goes to stderr.
		serveMain(ctx, serveDeps{
			client:       client,
			ws:           ws,
			procs:        bgProcs,
			maxTokens:    *maxTokens,
			contextLimit: contextLimit,
			logMax:       *logMax,
			verifyCmd:    *verifyCmd,
			autoApprove:  *yes,
			autoDeny:     *yes,
			auditPath:    *auditPath,
			pure:         *pureFlag,
			allow:        *allowFlag,
			deny:         *denyFlag,
			backendKind:  *backendKind,
			stream:       *streamFlag,
			thinkBudget:  *thinkBudget,
			skipVerify:   *noVerifyFlag,
			mcpTools:     mcpTools,
		}, os.Stdin, os.Stdout, os.Stderr)
		return ctx.Err()
	}

	env := roles.Env{
		Client:          client,
		WS:              ws,
		Procs:           bgProcs,
		MaxTokens:       *maxTokens,
		ContextLimit:    contextLimit,
		MaxSteps:        *maxSteps,
		Policy:          buildPolicy(*pureFlag, *allowFlag, *denyFlag),
		GateContext:     audit.ContextHook(*auditPath, "cli", permissionGateContext(*yes, suspend, ws)),
		BackendKind:     *backendKind,
		Model:           *model,
		Stream:          streamEnabled(*streamFlag, *modeFlag, *tuiFlag),
		MCPTools:        mcpTools,
		ReasoningBudget: *thinkBudget,
		// Direct runs fund acknowledged todo work with extra steps;
		// -serve leaves this off until its budget story is decided.
		TodoFunding: true,
		SkipVerify:  *noVerifyFlag,
		OnDelta:     func(chunk string) { fmt.Print(chunk) },
		OnStep:      stepPrinter(*modeFlag, emitter, *logMax),
		OnResult: func(result agent.ToolResult) {
			if *modeFlag == "json" {
				api.EmitToolResult(emitter, result.CallID, result.Output, result.Failed)
			}
		},
		OnUsage:   usagePrinter(*modeFlag, emitter),
		OnFinding: findingPrinter(*modeFlag),
		OnNudge:   nudgePrinter(*modeFlag),
	}
	if missionDir != "" {
		params := missionParams{
			env:          env,
			client:       client,
			ws:           ws,
			procs:        bgProcs,
			dir:          missionDir,
			task:         task,
			resuming:     *resume != "",
			contextLimit: contextLimit,
			maxTokens:    *maxTokens,
			logMax:       *logMax,
			verifyCmd:    *verifyCmd,
			autoApprove:  *yes,
			noteW:        noteW,
			emitter:      emitter,
			suspend:      suspend,
		}
		if *tuiFlag {
			_, err := tui.Run(ctx, tui.Config{Env: env, Task: task, StateFile: filepath.Join(missionDir, "mission.json"), AuditPath: *auditPath, Mission: func(runCtx context.Context, runEnv roles.Env, answer agent.Suspender, emitter *events.Emitter) error {
				p := params
				p.env = runEnv
				p.suspend = answer
				p.emitter = emitter
				p.noteW = io.Discard
				return runMission(runCtx, p)
			}})
			if err != nil {
				return fmt.Errorf("mission: %w", err)
			}
		} else if err := runMission(ctx, params); err != nil {
			return fmt.Errorf("mission: %w", err)
		}
		return nil
	}

	if *planFlag {
		emitter.Emit("plan_mode", map[string]any{"read_only": true})
	}
	if *streamFlag && *backendKind == "kobold" {
		note("note: -stream is unsupported on koboldcpp, using unary requests")
	}

	// Images resolve against the workspace (escape-checked like every
	// file tool path) into absolute paths the backend layer reads.
	var images []string
	if strings.TrimSpace(*imageFlag) != "" {
		for _, p := range strings.Split(*imageFlag, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			full, err := ws.Resolve(p)
			if err != nil {
				return fmt.Errorf("image: %w", err)
			}
			images = append(images, full)
		}
	}

	// All terminal modes share the configured environment and saved history.
	if *tuiFlag {
		history := forkHistory
		if *resume != "" {
			var err error
			history, err = loadHistory(*resume)
			if err != nil {
				return fmt.Errorf("resume: %w", err)
			}
		}

		answer, err := tui.Run(ctx, tui.Config{
			Plan:         *planFlag,
			History:      history,
			ResumeAnswer: *answer,
			AuditPath:    *auditPath,
			Env:          env,
			Task:         task,
			StateFile:    stateFile,
			Verify:       verify,
			Images:       images,
		})
		if err != nil {
			return fmt.Errorf("agent failed: %w", err)
		}
		fmt.Println("\n=== result ===")
		fmt.Println(answer)
		return nil
	}

	var a *agent.Agent
	if *planFlag {
		a = roles.Planner(env, "plan", stateFile)
	} else {
		a = roles.Interactive(env, "", stateFile, askFn, verify)
	}

	defer func() {
		r := a.Report()
		api.EmitOutcome(emitter, r, stateFile)
		note("outcome: %s; changed files: %s", r.Outcome, strings.Join(r.MutatedPaths, ", "))
		if r.Verification != "" {
			note("checks: %s", r.Verification)
		} else {
			note("checks: no current command verdict")
		}
		for _, warning := range r.Warnings {
			note("%s", warning)
		}
		note("resume: agent -resume %s", stateFile)
	}()

	var result string
	if *resume != "" {
		history, err := loadHistory(*resume)
		if err != nil {
			return fmt.Errorf("resume: %w", err)
		}
		note("resuming from %s (%d messages)", *resume, len(history))

		if callID, question, paused := agent.PausedOnQuestion(history); paused {
			ans := *answer
			if ans == "" {
				var err error
				ans, err = askFn(question)
				if err != nil {
					return fmt.Errorf("reading answer: %w", err)
				}
			}
			result, err = a.ResumeWithAnswer(ctx, history, callID, ans)
		} else {
			result, err = a.Resume(ctx, history, prompts.Resume)
		}
		if err != nil {
			return fmt.Errorf("agent failed: %w", err)
		}
	} else if len(forkHistory) > 0 {
		var err error
		result, err = a.Resume(ctx, forkHistory, prompts.Resume)
		if err != nil {
			return fmt.Errorf("agent failed: %w", err)
		}
	} else {
		var err error
		result, err = a.Run(ctx, task, images...)
		if err != nil {
			return fmt.Errorf("agent failed: %w", err)
		}
	}
	// Printed whole. -log-max caps each *step* line so a run stays
	// readable while it scrolls past; the final answer is the thing the
	// run was for, and middle-truncating it to 300 characters cut the
	// deliverable out of its own report. Mission mode already printed its
	// report in full, so this also makes the two modes agree.
	note("\n=== result ===")
	fmt.Fprintln(noteW, result)
	return nil
}

type missionParams struct {
	env          roles.Env
	client       llm.Client
	ws           *workspace.Workspace
	procs        *tools.BackgroundProcesses
	dir          string
	task         string
	resuming     bool
	contextLimit int
	maxTokens    int
	logMax       int
	verifyCmd    string
	autoApprove  bool
	// noteW carries human-readable output (stderr in -mode json, so
	// stdout stays pure JSONL). emitter is nil unless -mode json.
	noteW   io.Writer
	emitter *events.Emitter
	// suspend answers the plan approval gate. Same shared suspender as
	// the direct-mode gates: one stdin reader per run.
	suspend agent.Suspender
}

// runMission is the -mission entry point: harness-owned plan → execute →
// verify instead of one long reactive conversation. The interaction
// point with the human is the plan approval gate; workers themselves
// never ask questions.
func runMission(ctx context.Context, p missionParams) error {
	var m *mission.Mission
	if p.resuming {
		var err error
		m, err = mission.Load(p.dir)
		if err != nil {
			return fmt.Errorf("resume mission: %w", err)
		}
		progress := ""
		if len(m.Subtasks) > 0 {
			progress = fmt.Sprintf(", subtask %d/%d", m.Cursor+1, len(m.Subtasks))
		}
		fmt.Fprintf(p.noteW, "resuming mission %s (phase: %s%s)\n", m.ID, m.Phase, progress)
	} else {
		m = &mission.Mission{ID: filepath.Base(p.dir), Task: p.task, Phase: mission.PhaseExplore}
		if err := m.Save(p.dir); err != nil {
			return fmt.Errorf("create mission: %w", err)
		}
	}

	// The approval gate is the cheapest, strongest defense against a
	// weak model's garbage plans: a human reads it before anything runs.
	var approve func(string) (bool, string)
	if !p.autoApprove {
		approve = func(rendered string) (bool, string) {
			rep, err := p.suspend(ctx, agent.SuspendRequest{
				Kind:   agent.SuspendPlan,
				Prompt: "=== proposed plan ===\n" + rendered + "\n\napprove? [y]es / [n]o / or type a revision note",
			})
			if err != nil {
				return false, ""
			}
			line := strings.TrimSpace(rep.Answer)
			switch strings.ToLower(line) {
			case "y", "yes":
				return true, ""
			case "n", "no":
				return false, ""
			default:
				return false, line
			}
		}
	}

	runner := &mission.Runner{
		Env:          p.env,
		Client:       p.client,
		WS:           p.ws,
		Dir:          p.dir,
		Procs:        p.procs,
		ContextLimit: p.contextLimit,
		MaxTokens:    p.maxTokens,
		ApprovePlan:  approve,
		VerifyCmd:    p.verifyCmd,
		OnStep: func(subID string, step int, msg llm.Message) {
			api.EmitStep(p.emitter, subID, step, msg, false, 0)
			if msg.Content != "" {
				fmt.Fprintf(p.noteW, "[%s step %d] %s\n", subID, step, agent.TruncateMiddle(msg.Content, p.logMax))
			}
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(p.noteW, "[%s step %d] -> %s(%s)\n", subID, step, tc.Name,
					agent.TruncateMiddle(string(tc.Arguments), p.logMax))
			}
		},
		OnEvent: func(format string, args ...any) {
			if p.emitter != nil {
				p.emitter.Emit("mission", map[string]any{"text": fmt.Sprintf(format, args...)})
			}
			fmt.Fprintf(p.noteW, "[mission] "+format+"\n", args...)
		},
		OnResult: func(result agent.ToolResult) {
			api.EmitToolResult(p.emitter, result.CallID, result.Output, result.Failed)
		},
		OnUsage: func(step int, usage llm.Usage) {
			api.EmitUsage(p.emitter, step, usage)
		},
	}

	report, err := runner.Run(ctx, m)
	if p.emitter != nil {
		p.emitter.Emit("result", map[string]any{"report": events.Message(report)})
	}
	fmt.Fprintln(p.noteW, "\n=== mission report ===")
	fmt.Fprintln(p.noteW, report)
	return err
}

// buildPolicy combines built-in defaults with -allow/-deny overrides.
// -pure skips project config (no .tars/permissions.json loaded yet, so
// today it just means defaults only); extra rules always win by append order.
func buildPolicy(pure bool, allow, deny string) permission.Policy {
	_ = pure
	p := permission.Default()
	var extra []permission.Rule
	for _, spec := range parseRuleSpecs(allow, permission.Allow) {
		extra = append(extra, spec)
	}
	for _, spec := range parseRuleSpecs(deny, permission.Deny) {
		extra = append(extra, spec)
	}
	return p.WithExtra(extra)
}

func parseRuleSpecs(spec string, eff permission.Effect) []permission.Rule {
	var out []permission.Rule
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tool, pattern := "*", part
		if i := strings.Index(part, "="); i >= 0 {
			tool = strings.TrimSpace(part[:i])
			pattern = strings.TrimSpace(part[i+1:])
			if tool == "" {
				tool = "*"
			}
			if pattern == "" {
				pattern = "*"
			}
		}
		out = append(out, permission.Rule{Tool: tool, Pattern: pattern, Effect: eff})
	}
	return out
}

// streamEnabled decides whether model responses stream token by
// token: explicit -stream, or the fullscreen TUI (which renders live
// text and degrades to spinner+final where the backend refuses —
// koboldcpp has no Stream method, so agent.chat falls back unary).
// Never in -mode json (chunks would corrupt the JSONL stream).
func streamEnabled(streamFlag bool, modeFlag string, tuiFlag bool) bool {
	return (streamFlag || tuiFlag) && modeFlag != "json"
}

// permissionGate answers Ask-gated calls through the shared suspender.
// -yes auto-denies without suspending (fail-closed for unattended runs).
// The y/a/n mapping lives here, not in the transport: a channel-backed
// run answers the same question from its own UI. Mutating calls carry a
// preview of what they would do, rendered from the call's own arguments
// against the workspace as it is right now.
func permissionGate(autoDeny bool, suspend agent.Suspender, ws *workspace.Workspace) func(tool, resource string, args json.RawMessage) (permission.Effect, error) {
	gate := permissionGateContext(autoDeny, suspend, ws)
	return func(tool, resource string, args json.RawMessage) (permission.Effect, error) {
		return gate(context.Background(), tool, resource, args)
	}
}

func permissionGateContext(autoDeny bool, suspend agent.Suspender, ws *workspace.Workspace) func(context.Context, string, string, json.RawMessage) (permission.Effect, error) {
	var mu sync.Mutex
	always := map[[2]string]bool{}
	return func(ctx context.Context, tool, resource string, args json.RawMessage) (permission.Effect, error) {
		key := [2]string{tool, resource}
		mu.Lock()
		allowed := always[key]
		mu.Unlock()
		if allowed {
			return permission.Allow, nil
		}
		if autoDeny {
			return permission.Deny, nil
		}
		prompt := fmt.Sprintf("\n[permission] %s on %q - allow? [y]es once / [a]lways / [n]o", tool, resource)
		if preview := tools.PreviewArgs(ws, tool, args); preview != "" {
			prompt += "\n" + preview
		}
		rep, err := suspend(ctx, agent.SuspendRequest{
			Kind:     agent.SuspendPermission,
			Tool:     tool,
			Resource: resource,
			Prompt:   prompt,
		})
		if err != nil {
			return permission.Deny, err
		}
		if eff, derr := permission.Decide(rep.Answer); derr != nil {
			return permission.Deny, fmt.Errorf("blocked by operator (%s on %s)", tool, resource)
		} else {
			if strings.EqualFold(strings.TrimSpace(rep.Answer), "a") || strings.EqualFold(strings.TrimSpace(rep.Answer), "always") {
				mu.Lock()
				always[key] = true
				mu.Unlock()
			}
			return eff, nil
		}
	}
}

// stdioSuspender answers gates from the terminal: the current behavior,
// centralized so future transports replace one constructor instead of
// three stdin blocks. Prompts go to noteW (stderr in -mode json), never
// to event stdout. Each suspension emits awaiting_input/input_answered
// events - the vocabulary a future TUI or RPC client already speaks.
func stdioSuspender(noteW io.Writer, emitter *events.Emitter) agent.Suspender {
	reader := bufio.NewReader(os.Stdin)
	type input struct {
		line string
		err  error
	}
	replies := make(chan input, 1)
	var once sync.Once
	flight := make(chan struct{}, 1)
	return func(ctx context.Context, req agent.SuspendRequest) (agent.SuspendReply, error) {
		select {
		case flight <- struct{}{}:
		case <-ctx.Done():
			return agent.SuspendReply{}, ctx.Err()
		}
		defer func() { <-flight }()
		once.Do(func() {
			go func() {
				for {
					line, err := reader.ReadString('\n')
					replies <- input{line, err}
					if err != nil {
						close(replies)
						return
					}
				}
			}()
		})
		if emitter != nil {
			emitter.Emit("awaiting_input", map[string]any{"kind": string(req.Kind), "id": req.ID})
		}
		fmt.Fprintf(noteW, "%s\n> ", req.Prompt)
		select {
		case <-ctx.Done():
			return agent.SuspendReply{}, ctx.Err()
		case rep, ok := <-replies:
			if !ok {
				return agent.SuspendReply{}, fmt.Errorf("input transport closed")
			}
			if rep.err != nil && strings.TrimSpace(rep.line) == "" {
				return agent.SuspendReply{}, fmt.Errorf("reading answer: %w", rep.err)
			}
			if emitter != nil {
				emitter.Emit("input_answered", map[string]any{"kind": string(req.Kind), "id": req.ID})
			}
			return agent.SuspendReply{Answer: strings.TrimSpace(rep.line)}, nil
		}
	}
}

func stepPrinter(mode string, emitter *events.Emitter, logMax int) func(string, int, llm.Message) {
	if mode == "json" && emitter != nil {
		return func(label string, step int, msg llm.Message) {
			api.EmitStep(emitter, label, step, msg, false, 0)
		}
	}
	return func(label string, step int, msg llm.Message) { printStep(label, step, msg, logMax) }
}

// toolResultPrinter mirrors stepPrinter for completed tool calls: JSON
// events in json mode, historical silence in print mode.
func toolResultPrinter(mode string, emitter *events.Emitter) func(string, string) {
	if mode == "json" && emitter != nil {
		return func(callID, result string) { api.EmitToolResult(emitter, callID, result) }
	}
	return nil
}

// usagePrinter mirrors stepPrinter for token counts: JSON events in
// json mode, nothing in print mode (the debug log already records them).
func usagePrinter(mode string, emitter *events.Emitter) func(int, llm.Usage) {
	if mode == "json" && emitter != nil {
		return func(step int, usage llm.Usage) { api.EmitUsage(emitter, step, usage) }
	}
	return nil
}

// findingPrinter shows post-write check findings in print mode; json
// mode carries them as finding events via the session emitter.
func findingPrinter(mode string) func(rule, path string, line int, summary string) {
	if mode == "json" {
		return nil
	}
	return func(rule, path string, line int, summary string) {
		fmt.Printf("check %s %s:%d %s\n", rule, path, line, summary)
	}
}

// nudgePrinter shows loop-generated harness notices in print mode;
// json mode carries them as nudge events via the session emitter.
func nudgePrinter(mode string) func(kind, text string) {
	if mode == "json" {
		return nil
	}
	return func(kind, text string) {
		fmt.Printf("nudge [%s] %s\n", kind, text)
	}
}

// discoverMCP starts each -mcp server and returns its tools and clients.
// A spec entry is either name=command args... (stdio) or name=https://...
// (Streamable HTTP). A server that fails to answer risks nothing: it is
// reported and skipped, and the run continues with the rest. Close every
// client when the run ends so no server process outlives it.
func discoverMCP(ctx context.Context, spec string) ([]agent.Tool, []MCPCloser) {
	names, commands, argss := tools.ParseMCPFlag(spec)
	var out []agent.Tool
	var clients []MCPCloser
	for i, name := range names {
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var mcpTools []agent.Tool
		var mcpClient MCPCloser
		var err error
		if tools.IsMCPURL(commands[i]) {
			mcpTools, mcpClient, err = tools.StartMCPHTTP(dctx, name, commands[i])
		} else {
			mcpTools, mcpClient, err = tools.StartMCP(dctx, name, commands[i], argss[i]...)
		}
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp %s: %v (skipped)\n", name, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "mcp %s: %d tools\n", name, len(mcpTools))
		out = append(out, mcpTools...)
		clients = append(clients, mcpClient)
	}
	return out, clients
}

// MCPCloser is either MCP transport: stdio kills its process, HTTP ends
// its session. Both are best-effort by design.
type MCPCloser interface {
	Close()
}

// loadHistory reads a transcript from state.json, a .jsonl session log,
// or a directory holding either. The log is the fallback when the snapshot
// is missing or corrupt.
func loadHistory(path string) ([]llm.Message, error) {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		if h, err := agent.LoadState(filepath.Join(path, "state.json")); err == nil {
			return h, nil
		}
		return session.Load(session.LogPath(filepath.Join(path, "state.json")))
	}
	if strings.HasSuffix(path, ".jsonl") {
		return session.Load(path)
	}
	if h, err := agent.LoadState(path); err == nil {
		return h, nil
	}
	return session.Load(session.LogPath(path))
}

// resolveAPIKey attaches the bearer token to a remote client. Precedence
// is the flag, then the named env var, then the built-in lookup order.
// Local backends need no key and are left alone.
func resolveAPIKey(flagVal, envName string, client *llm.Server) {
	if strings.TrimSpace(flagVal) != "" {
		client.WithAPIKey(strings.TrimSpace(flagVal))
		return
	}
	if strings.TrimSpace(envName) != "" {
		if v := strings.TrimSpace(os.Getenv(strings.TrimSpace(envName))); v != "" {
			client.WithAPIKey(v)
			return
		}
	}
	if v := llm.APIKeyFromEnv(); v != "" {
		client.WithAPIKey(v)
	}
}

// extraHeadersFromEnv supplies gateway-specific headers without new flags:
// OpenRouter recommends identifying the app on every request.
func extraHeadersFromEnv() map[string]string {
	out := map[string]string{}
	if v := strings.TrimSpace(os.Getenv("OPENROUTER_REFERER")); v != "" {
		out["HTTP-Referer"] = v
	} else if v := strings.TrimSpace(os.Getenv("TARS_SITE_URL")); v != "" {
		out["HTTP-Referer"] = v
	}
	if v := strings.TrimSpace(os.Getenv("TARS_SITE_TITLE")); v != "" {
		out["X-Title"] = v
	}
	return out
}

// printStep renders one model step to the console. label is a subtask id
// or worker name in mission mode and empty for the top-level agent, which
// is the only difference between what the two modes used to print from
// two separate copies of this loop.
func printStep(label string, step int, msg llm.Message, logMax int) {
	prefix := fmt.Sprintf("[step %d]", step)
	if label != "" {
		prefix = fmt.Sprintf("[%s step %d]", label, step)
	}
	if msg.Content != "" {
		fmt.Printf("%s %s\n", prefix, agent.TruncateMiddle(msg.Content, logMax))
	}
	for _, tc := range msg.ToolCalls {
		fmt.Printf("%s -> %s(%s)\n", prefix, tc.Name,
			agent.TruncateMiddle(string(tc.Arguments), logMax))
	}
}
