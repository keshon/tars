package main

// The stdio JSON-RPC server behind -serve: methods run/respond/cancel on
// stdin, event lines plus id-responses on stdout. The protocol is
// documented in docs/rpc.md; the shape below is the whole of it.
//
// Concurrency is deliberately single-flight: one run at a time, one
// pending gate at a time. The agent loop is single-threaded per run, so
// a second gate cannot exist while one is open — the invariant is
// asserted, not assumed, and violations report errors instead of
// deadlocking.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/audit"
	"github.com/keshon/tars/internal/events"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/mission"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/snapshot"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

// serveDeps is everything a run needs, assembled once in main. Per-run
// values (task dir, cancellation) are created per request.
type serveDeps struct {
	client       llm.Client
	ws           *workspace.Workspace
	procs        *tools.BackgroundProcesses
	maxTokens    int
	contextLimit int
	logMax       int
	verifyCmd    string
	autoApprove  bool
	autoDeny     bool
	auditPath    string
	pure         bool
	allow        string
	deny         string
	backendKind  string
	stream       bool
	thinkBudget  int
	skipVerify   bool
	mcpTools     []agent.Tool
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type rpcRunParams struct {
	Task    string   `json:"task"`
	Mission bool     `json:"mission"`
	Images  []string `json:"images,omitempty"`
}

type rpcRespondParams struct {
	Answer string `json:"answer"`
}

// serveMain runs the request loop until stdin closes. out carries events
// and id-responses; notes (human chatter) go to noteW, never to out.
func serveMain(ctx context.Context, d serveDeps, in io.Reader, out io.Writer, noteW io.Writer) {
	var wmu sync.Mutex
	write := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		wmu.Lock()
		defer wmu.Unlock()
		fmt.Fprintf(out, "%s\n", data)
		if f, ok := out.(interface{ Sync() error }); ok {
			_ = f.Sync()
		}
	}
	respond := func(id json.RawMessage, result any, err error) {
		if id == nil {
			id = json.RawMessage("null")
		}
		// Struct, not a map: key order on the wire is id first, stable
		// for readers and log greppers alike.
		rec := struct {
			ID     json.RawMessage `json:"id"`
			Result any             `json:"result,omitempty"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}{ID: id}
		if err != nil {
			rec.Error = &struct {
				Message string `json:"message"`
			}{Message: err.Error()}
		} else {
			rec.Result = result
		}
		write(rec)
	}

	emitter := events.New(&lockedWriter{mu: &wmu, w: out})
	hub := api.NewGateHub(func(name string, fields map[string]any) {
		emitter.Emit(name, fields)
	})

	var runMu sync.Mutex
	var cancel context.CancelFunc
	var wg sync.WaitGroup
	busy := func() bool {
		runMu.Lock()
		defer runMu.Unlock()
		return cancel != nil
	}
	setCancel := func(fn context.CancelFunc) {
		runMu.Lock()
		defer runMu.Unlock()
		cancel = fn
	}

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			respond(nil, nil, fmt.Errorf("bad request: %w", err))
			continue
		}
		switch req.Method {
		case "run":
			var params rpcRunParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				respond(req.ID, nil, fmt.Errorf("bad run params: %w", err))
				continue
			}
			if busy() {
				respond(req.ID, nil, fmt.Errorf("run already in progress"))
				continue
			}
			runCtx, cancelFn := context.WithCancel(ctx)
			setCancel(cancelFn)
			wg.Add(1)
			go func(id json.RawMessage, params rpcRunParams) {
				defer wg.Done()
				defer setCancel(nil)
				answer, err := serveRun(runCtx, d, hub, emitter, noteW, func(ev api.Event) {
					rec := map[string]any{"seq": ev.Seq, "event": ev.Name}
					for k, v := range ev.Fields {
						rec[k] = v
					}
					write(rec)
				}, params.Task, params.Mission, params.Images)
				if err != nil {
					respond(id, nil, err)
					return
				}
				respond(id, map[string]any{"answer": answer}, nil)
			}(req.ID, params)
		case "respond":
			var params rpcRespondParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				respond(req.ID, nil, fmt.Errorf("bad respond params: %w", err))
				continue
			}
			if err := hub.Respond(params.Answer); err != nil {
				respond(req.ID, nil, err)
				continue
			}
			respond(req.ID, map[string]any{"ok": true}, nil)
		case "cancel":
			runMu.Lock()
			fn := cancel
			runMu.Unlock()
			if fn == nil {
				respond(req.ID, nil, fmt.Errorf("no run in progress"))
				continue
			}
			fn()
			respond(req.ID, map[string]any{"ok": true}, nil)
		default:
			respond(req.ID, nil, fmt.Errorf("unknown method %q (want run, respond or cancel)", req.Method))
		}
	}
	// Stdin closed: no more requests will arrive, but an in-flight run
	// still owns its answer — drain it instead of abandoning it. This is
	// what makes piped one-shots (`printf ... | agent -serve`) work
	// instead of racing startup against EOF. Exception: a run suspended
	// on a gate can never proceed — its answer was going to arrive on
	// the stdin that just closed — so cancel it instead of hanging.
	// Ctrl+C aborts everything immediately via the signal handler.
	if hub.HasPending() {
		runMu.Lock()
		if cancel != nil {
			cancel()
		}
		runMu.Unlock()
	}
	wg.Wait()
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// serveRun executes one request: fresh task dir, snapshot, then direct
// session or mission pipeline, both gated through the hub suspender.
// onEvent carries session step events to the client.
func serveRun(ctx context.Context, d serveDeps, hub *api.GateHub, emitter *events.Emitter, noteW io.Writer, onEvent func(api.Event), task string, isMission bool, imagePaths []string) (string, error) {
	if task == "" {
		return "", fmt.Errorf("run needs a task")
	}
	// RPC image paths resolve against the workspace like -image does:
	// a bad path fails the run here, not mid-turn at the backend.
	var images []string
	for _, p := range imagePaths {
		full, err := d.ws.Resolve(p)
		if err != nil {
			return "", fmt.Errorf("image: %w", err)
		}
		images = append(images, full)
	}
	sum := sha1.Sum([]byte(task + time.Now().String()))
	taskID := hex.EncodeToString(sum[:])[:8]
	taskDir := workspace.TaskDir(taskID)
	stateFile := filepath.Join(taskDir, "state.json")
	_ = workspace.WriteSessionTitle(taskDir, workspace.TitleLine(task))

	if snap := snapshot.Track(d.ws.Root(), filepath.Join(taskDir, "snapshots")); snap.Path != "" {
		fmt.Fprintf(noteW, "snapshot: %s\n", snap.Path)
	}

	suspend := hub.Suspender()
	env := roles.Env{
		Client:          d.client,
		WS:              d.ws,
		Procs:           d.procs,
		MaxTokens:       d.maxTokens,
		ContextLimit:    d.contextLimit,
		Policy:          buildPolicy(d.pure, d.allow, d.deny),
		Gate:            audit.Hook(d.auditPath, "serve", permissionGate(d.autoDeny, suspend, d.ws)),
		BackendKind:     d.backendKind,
		Stream:          d.stream,
		SkipVerify:      d.skipVerify,
		MCPTools:        d.mcpTools,
		ReasoningBudget: d.thinkBudget,
	}
	emitter.Emit("run_start", map[string]any{"task": task, "mission": isMission})
	if isMission {
		err := runMission(ctx, missionParams{
			client:       d.client,
			ws:           d.ws,
			procs:        d.procs,
			dir:          taskDir,
			task:         task,
			contextLimit: d.contextLimit,
			maxTokens:    d.maxTokens,
			logMax:       d.logMax,
			verifyCmd:    d.verifyCmd,
			autoApprove:  d.autoApprove,
			noteW:        noteW,
			emitter:      emitter,
			suspend:      suspend,
		})
		if err != nil {
			return "", err
		}
		return "mission complete", nil
	}
	verify := func(ctx context.Context) (string, bool) {
		if d.verifyCmd == "" {
			return "", false
		}
		out, err := mission.RunShellCommand(ctx, d.verifyCmd, d.ws.Root())
		status := "PASSED"
		if err != nil {
			status = "FAILED"
		}
		return fmt.Sprintf("%s\n%s", status, out), true
	}
	sess, err := api.New(api.Config{
		Env:       env,
		Task:      task,
		StateFile: stateFile,
		Verify:    verify,
		Images:    images,
		Answer:    suspend,
		OnEvent:   onEvent,
	})
	if err != nil {
		return "", err
	}
	return sess.Run(ctx)
}
