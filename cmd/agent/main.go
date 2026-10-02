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
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/mission"
	"github.com/keshon/tars/internal/permission"
	"github.com/keshon/tars/internal/prompts"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/session"
	"github.com/keshon/tars/internal/snapshot"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

func main() {
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
	resume := flag.String("resume", "", "path to a .agent/tasks/.../state.json snapshot to resume "+
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
		"without it. Also auto-enabled when the task names ≥2 deliverable files (see -direct)")
	direct := flag.Bool("direct", false, "force the reactive agent loop even when the task looks multi-file")
	yes := flag.Bool("yes", false, "skip the mission plan approval gate and run the plan as generated")
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
	mcpFlag := flag.String("mcp", "", "MCP servers: \"name=cmd args...;name2=cmd2\" (stdio JSON-RPC, tools appear as mcp__name__tool)")
	forkFlag := flag.String("fork", "", "history file to branch from: loads its transcript but writes to a fresh task id")
	revertFlag := flag.Bool("revert", false, "restore tracked workspace files to git HEAD and exit (untracked files are kept)")
	flag.Parse()

	task := strings.Join(flag.Args(), " ")
	if task == "" && *resume == "" && *forkFlag == "" {
		log.Fatal(`usage: agent [flags] "task description"  (or  agent -resume <state.json> [-answer "..."])`)
	}

	// Auto-mission for multi-file tasks — the cheap alternative to hoping
	// the reactive loop (or spontaneous delegate_task) holds a plan.
	if !*missionMode && !*direct && task != "" && mission.SuggestMission(task) {
		*missionMode = true
		fmt.Println("auto-mission: task names multiple deliverable files (use -direct to skip)")
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
		stateFile = *resume // keep appending to the same snapshot we resumed from
	} else {
		sum := sha1.Sum([]byte(task + time.Now().String()))
		taskID := hex.EncodeToString(sum[:])[:8]
		taskDir := filepath.Join(".agent", "tasks", taskID)
		stateFile = filepath.Join(taskDir, "state.json")
		if *forkFlag != "" {
			var err error
			forkHistory, err = loadHistory(*forkFlag)
			if err != nil {
				log.Fatalf("fork: %v", err)
			}
			fmt.Printf("forked from %s (%d messages)\n", *forkFlag, len(forkHistory))
		}
		if *missionMode {
			missionDir = taskDir
			fmt.Printf("mission id: %s (resume with -resume %s)\n", taskID, taskDir)
		} else {
			fmt.Printf("task id: %s (resume with -resume %s)\n", taskID, stateFile)
		}
	}

	ws, wsErr := workspace.New(*root)
	if wsErr != nil {
		log.Fatalf("workspace: %v", wsErr)
	}

	if *revertFlag {
		if err := snapshot.Revert(ws.Root()); err != nil {
			log.Fatalf("revert: %v", err)
		}
		fmt.Println("workspace reverted to HEAD (untracked files kept)")
		return
	}

	client, err := llm.ClientFor(*backendKind, *backend, *model)
	if err != nil {
		log.Fatalf("%v", err)
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
			log.Fatalf("open debug log: %v", err)
		}
		defer f.Close()
		client.Debug = f
		fmt.Println("debug: logging raw request/response JSON to agent-debug.log")
	}

	// Ask the backend for its real context window instead of guessing.
	ctx, stop := context.WithCancel(context.Background())
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
		fmt.Printf("context window: %d tokens (estimated for %s; override with -context-limit)\n",
			contextLimit, *model)
		if actual := llm.DetectKind(ctx, *backend); actual != "" && actual != *backendKind {
			log.Fatalf("-backend-kind is %q but %s is answering at %s.\n"+
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
			log.Fatalf("-backend-kind is %q but %s is answering at %s.\n"+
				"Re-run with -backend-kind %s. Continuing would send %s's grammar and\n"+
				"sampler fields to a server that ignores both, and the run would look fine.",
				*backendKind, actual, *backend, actual, *backendKind)
		}
		// Otherwise the backend is simply not reporting: keep going
		// without budget tracking rather than failing the whole run.
		contextLimit = 0
	} else {
		fmt.Printf("context window: %d tokens\n", contextLimit)
	}
	}

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
			return fmt.Sprintf("%s\n%s", status, out), true
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
	// The loop snapshots state after every step, so an interrupt is
	// recoverable — but only if the operator is told where the snapshot
	// is, at the moment they need it rather than in the scrollback.
	// os.Exit skips deferred calls, so this path cleans up for itself.
	resumeHint := fmt.Sprintf("agent -resume %s", stateFile)
	if missionDir != "" {
		resumeHint = fmt.Sprintf("agent -resume %s", missionDir)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Fprintln(os.Stderr, "\ninterrupted")
		stop() // aborts the in-flight request and any running tool
		bgProcs.StopAll()
		fmt.Fprintf(os.Stderr, "resume with: %s\n", resumeHint)
		os.Exit(130) // 128 + SIGINT, the shell convention
	}()

	if missionDir != "" {
		runMission(ctx, missionParams{
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
		})
		return
	}

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
		fmt.Printf("\n[paused — state saved at %s]\n", stateFile)
		fmt.Printf("[resume: agent -resume %s -answer \"your answer\"]\n", stateFile)
		fmt.Printf("\n[agent asks] %s\n> ", question)
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("reading answer: %w", err)
		}
		return strings.TrimSpace(line), nil
	}

	// Git snapshot before any work, so the run is reviewable/revertible.
	if snap := snapshot.Track(ws.Root(), filepath.Join(filepath.Dir(stateFile), "snapshots")); snap.Path != "" {
		fmt.Printf("snapshot: %s\n", snap.Path)
	}

	mcpTools, mcpClients := discoverMCP(ctx, *mcpFlag)
	defer func() {
		for _, c := range mcpClients {
			c.Close()
		}
	}()

	env := roles.Env{
		Client:       client,
		WS:           ws,
		Procs:        bgProcs,
		MaxTokens:    *maxTokens,
		ContextLimit: contextLimit,
		Policy:       buildPolicy(*pureFlag, *allowFlag, *denyFlag),
		Gate:         permissionGate(*yes),
		BackendKind:  *backendKind,
		Stream:       *streamFlag,
		MCPTools:     mcpTools,
		OnDelta:      func(chunk string) { fmt.Print(chunk) },
		OnStep:       func(l string, s int, m llm.Message) { printStep(l, s, m, *logMax) },
	}
	if *streamFlag && *backendKind == "kobold" {
		fmt.Println("note: -stream is unsupported on koboldcpp, using unary requests")
	}

	// Git snapshot before any work, so the run is reviewable/revertible.
	if snap := snapshot.Track(ws.Root(), filepath.Join(filepath.Dir(stateFile), "snapshots")); snap.Path != "" {
		fmt.Printf("snapshot: %s\n", snap.Path)
	}

	// The subagent this spawns previously also carried Verify. That was
	// dead configuration: a subagent sets SkipVerify with no
	// VerifyOnZeroWrites, so verifyWanted is never true and the hook could
	// not fire. Dropping it changes nothing at runtime.
	a := roles.Interactive(env, "", stateFile, askFn, verify)

	var result string
	if *resume != "" {
		history, err := loadHistory(*resume)
		if err != nil {
			log.Fatalf("resume: %v", err)
		}
		fmt.Printf("resuming from %s (%d messages)\n", *resume, len(history))

		if callID, question, paused := agent.PausedOnQuestion(history); paused {
			ans := *answer
			if ans == "" {
				// No -answer flag: the human is here now, just ask them
				// interactively using the same askFn path as the live run.
				fmt.Printf("\n[agent asked] %s\n> ", question)
				reader := bufio.NewReader(os.Stdin)
				line, err := reader.ReadString('\n')
				if err != nil {
					log.Fatalf("reading answer: %v", err)
				}
				ans = strings.TrimSpace(line)
			}
			result, err = a.ResumeWithAnswer(ctx, history, callID, ans)
		} else {
			result, err = a.Resume(ctx, history, prompts.Resume)
		}
		if err != nil {
			log.Fatalf("agent failed: %v", err)
		}
	} else if len(forkHistory) > 0 {
		var err error
		result, err = a.Resume(ctx, forkHistory, prompts.Resume)
		if err != nil {
			log.Fatalf("agent failed: %v", err)
		}
	} else {
		var err error
		result, err = a.Run(ctx, task)
		if err != nil {
			log.Fatalf("agent failed: %v", err)
		}
	}
	// Printed whole. -log-max caps each *step* line so a run stays
	// readable while it scrolls past; the final answer is the thing the
	// run was for, and middle-truncating it to 300 characters cut the
	// deliverable out of its own report. Mission mode already printed its
	// report in full, so this also makes the two modes agree.
	fmt.Println("\n=== result ===")
	fmt.Println(result)
}

type missionParams struct {
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
}

// runMission is the -mission entry point: harness-owned plan → execute →
// verify instead of one long reactive conversation. The interaction
// point with the human is the plan approval gate; workers themselves
// never ask questions.
func runMission(ctx context.Context, p missionParams) {
	var m *mission.Mission
	if p.resuming {
		var err error
		m, err = mission.Load(p.dir)
		if err != nil {
			log.Fatalf("resume mission: %v", err)
		}
		progress := ""
		if len(m.Subtasks) > 0 {
			progress = fmt.Sprintf(", subtask %d/%d", m.Cursor+1, len(m.Subtasks))
		}
		fmt.Printf("resuming mission %s (phase: %s%s)\n", m.ID, m.Phase, progress)
	} else {
		m = &mission.Mission{ID: filepath.Base(p.dir), Task: p.task, Phase: mission.PhaseExplore}
		if err := m.Save(p.dir); err != nil {
			log.Fatalf("create mission: %v", err)
		}
	}

	// The approval gate is the cheapest, strongest defense against a
	// weak model's garbage plans: a human reads it before anything runs.
	var approve func(string) (bool, string)
	if !p.autoApprove {
		reader := bufio.NewReader(os.Stdin)
		approve = func(rendered string) (bool, string) {
			fmt.Println("\n=== proposed plan ===")
			fmt.Println(rendered)
			fmt.Print("\napprove? [y]es / [n]o / or type a revision note\n> ")
			line, err := reader.ReadString('\n')
			if err != nil {
				return false, ""
			}
			line = strings.TrimSpace(line)
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
		Client:       p.client,
		WS:           p.ws,
		Dir:          p.dir,
		Procs:        p.procs,
		ContextLimit: p.contextLimit,
		MaxTokens:    p.maxTokens,
		ApprovePlan:  approve,
		VerifyCmd:    p.verifyCmd,
		OnStep: func(subID string, step int, msg llm.Message) {
			if msg.Content != "" {
				fmt.Printf("[%s step %d] %s\n", subID, step, agent.TruncateMiddle(msg.Content, p.logMax))
			}
			for _, tc := range msg.ToolCalls {
				fmt.Printf("[%s step %d] -> %s(%s)\n", subID, step, tc.Name,
					agent.TruncateMiddle(string(tc.Arguments), p.logMax))
			}
		},
		OnEvent: func(format string, args ...any) {
			fmt.Printf("[mission] "+format+"\n", args...)
		},
	}

	report, err := runner.Run(ctx, m)
	fmt.Println("\n=== mission report ===")
	fmt.Println(report)
	if err != nil {
		log.Fatalf("%v", err)
	}
}

// buildPolicy combines built-in defaults with -allow/-deny overrides.
// -pure skips project config (no .agent/permissions.json loaded yet, so
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

// permissionGate prompts for Ask-gated calls on stdin. -yes auto-denies
// asks (fail-closed for unattended runs) instead of prompting.
func permissionGate(autoDeny bool) func(tool, resource string, args json.RawMessage) (permission.Effect, error) {
	return func(tool, resource string, _ json.RawMessage) (permission.Effect, error) {
		if autoDeny {
			return permission.Deny, nil
		}
		fmt.Printf("\n[permission] %s on %q — allow? [y]es once / [a]lways / [n]o\n> ", tool, resource)
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil {
			return permission.Deny, fmt.Errorf("blocked: no answer")
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return permission.Allow, nil
		case "a", "always":
			return permission.Allow, nil
		default:
			return permission.Deny, fmt.Errorf("blocked by operator (%s on %s)", tool, resource)
		}
	}
}

// discoverMCP starts each -mcp server and returns its tools and clients.
// A server that fails to answer risks nothing: it is reported and
// skipped, and the run continues with the rest. Close every client when
// the run ends so no server process outlives it.
func discoverMCP(ctx context.Context, spec string) ([]agent.Tool, []*tools.MCPClient) {
	names, commands, argss := tools.ParseMCPFlag(spec)
	var out []agent.Tool
	var clients []*tools.MCPClient
	for i, name := range names {
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		mcpTools, mcpClient, err := tools.StartMCP(dctx, name, commands[i], argss[i]...)
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
func printStep(label string, step int, msg llm.Message, logMax int) {	prefix := fmt.Sprintf("[step %d]", step)
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
