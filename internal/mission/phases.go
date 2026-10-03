package mission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

// Runner drives a mission through its phases deterministically. The
// model never decides what phase comes next — it fills in a plan when
// asked and executes one subtask at a time in a fresh, compiled context.
// Every transition and every status change persists mission.json first,
// so resume is always "load, switch on phase, continue".
type Runner struct {
	Client llm.Client
	WS     *workspace.Workspace
	Dir    string // .agent/tasks/<id> — the mission's state directory
	Procs  *tools.BackgroundProcesses

	ContextLimit int
	MaxTokens    int

	// MaxWorkerSteps bounds each subtask worker. Deliberately small: a
	// subtask that needs 30 steps is a planning failure, not a worker
	// failure. Zero means 15.
	MaxWorkerSteps int

	// MaxPlanRevisions bounds how many times the human approval gate can
	// send the plan back with an edit note. Zero means 2.
	MaxPlanRevisions int

	// MaxFixAttempts is how many fresh fix workers may follow a failed
	// check before the subtask is declared failed (total worker runs per
	// subtask = 1 + MaxFixAttempts). Zero means 2; negative disables the
	// fix loop entirely.
	MaxFixAttempts int

	// PlanCandidates is how many plans are generated and scored before one
	// is picked. Zero uses the package default; 1 keeps the first valid
	// plan, which is what an A/B comparison of the search needs.
	PlanCandidates int

	// MaxReplans bounds how many times a stopped plan may be replaced by
	// a new one for the remaining work. Zero means 1; negative disables
	// replanning. Together with MaxFixAttempts this bounds total worker
	// runs by construction: subtasks × (1+fixes) × (1+replans).
	MaxReplans int

	// ApprovePlan, if set, gates execution on a human reading the
	// rendered plan. Return (true, "") to run it, (false, "note") to
	// regenerate with the note, (false, "") to abort the mission. Nil
	// auto-approves — the -yes path.
	ApprovePlan func(rendered string) (approved bool, editNote string)

	// VerifyCmd, if non-empty, runs as a mission-level shell check
	// during the verify phase, independent of whatever checks the
	// planner declared — the harness's own ground truth.
	VerifyCmd string

	// SkipMapNotes disables the read-only annotation worker in the
	// explore phase; the mechanical map alone is used. The mechanical map
	// never depends on the model either way.
	SkipMapNotes bool

	// SkipReview disables the final read-only review worker. When it
	// runs, its verdict is decision-narrowed to ok/gaps by grammar; gaps
	// can spend remaining replan budget, but a mission whose checks all
	// passed never fails because of the review alone.
	SkipReview bool

	// OnStep mirrors agent.Config.OnStep for worker steps, tagged with
	// the subtask id. OnEvent narrates phase-level progress. Both
	// optional.
	OnStep  func(subID string, step int, msg llm.Message)
	OnEvent func(format string, args ...any)

	// OnToolResult mirrors agent.Config.OnToolResult for worker tool
	// results. Optional; nil keeps workers silent as before.
	OnToolResult func(callID, result string)

	// OnUsage mirrors agent.Config.OnUsage for worker token counts.
	// Optional.
	OnUsage func(step int, usage llm.Usage)
}

func (r *Runner) event(format string, args ...any) {
	if r.OnEvent != nil {
		r.OnEvent(format, args...)
	}
}

func (r *Runner) maxWorkerSteps() int {
	if r.MaxWorkerSteps > 0 {
		return r.MaxWorkerSteps
	}
	return 15
}

func (r *Runner) maxPlanRevisions() int {
	if r.MaxPlanRevisions > 0 {
		return r.MaxPlanRevisions
	}
	return 2
}

func (r *Runner) maxFixAttempts() int {
	if r.MaxFixAttempts != 0 {
		return max(r.MaxFixAttempts, 0)
	}
	return 2
}

func (r *Runner) maxReplans() int {
	if r.MaxReplans != 0 {
		return max(r.MaxReplans, 0)
	}
	return 1
}

// Run drives m from its current phase to a terminal one and returns the
// final report. The error is non-nil when the mission FAILED — the
// report still describes everything that was measured, because failing
// loudly with facts is a feature, not an afterthought.
func (r *Runner) Run(ctx context.Context, m *Mission) (string, error) {
	for {
		switch m.Phase {
		case PhaseExplore:
			r.runExplore(ctx, m)
			m.Phase = PhasePlan
			if err := m.Save(r.Dir); err != nil {
				return "", err
			}

		case PhasePlan:
			if err := r.runPlan(ctx, m); err != nil {
				if errors.Is(err, errInfra) {
					return m.RenderReport(), err // phase unchanged; resume re-plans
				}
				return r.fail(m, "planning: %v", err)
			}

		case PhaseExecute:
			sub := m.Current()
			if sub == nil {
				m.Phase = PhaseVerify
				if err := m.Save(r.Dir); err != nil {
					return "", err
				}
				continue
			}
			// Failed subtasks are skipped too: a failed subtask still at
			// the cursor after a resume means it was already either
			// replaced by a replan or is about to fail the mission below —
			// never silently re-run.
			if sub.Status == StatusDone || sub.Status == StatusSkipped || sub.Status == StatusFailed {
				m.Cursor++
				if err := m.Save(r.Dir); err != nil {
					return "", err
				}
				continue
			}
			err := r.runSubtask(ctx, m, sub)
			if err == nil {
				continue
			}
			if errors.Is(err, errInfra) {
				r.event("mission interrupted: %v", err)
				return m.RenderReport(), err // phase stays execute; resume continues here
			}
			if !errors.Is(err, errSubtaskFailed) {
				return r.fail(m, "subtask %s: %v", sub.ID, err)
			}
			// Check failed and fix attempts are exhausted — the one
			// remaining lever is a new plan for the remaining work.
			if rerr := r.tryReplan(ctx, m, err.Error()); rerr != nil {
				if errors.Is(rerr, errInfra) {
					return m.RenderReport(), rerr
				}
				return r.fail(m, "subtask %s: %v; %v", sub.ID, err, rerr)
			}

		case PhaseVerify:
			regressions := r.runVerifyChecks(ctx, m)
			if len(regressions) > 0 {
				reason := "final verification failed:\n" + strings.Join(regressions, "\n")
				if rerr := r.tryReplan(ctx, m, reason); rerr != nil {
					if errors.Is(rerr, errInfra) {
						return m.RenderReport(), rerr
					}
					return r.fail(m, "%s; %v", reason, rerr)
				}
				continue
			}
			r.event("all checks passed")

			// The review net catches whole-task gaps the per-subtask
			// checks can't see. Gaps spend the replan budget when there is
			// any; otherwise they're recorded honestly and the mission
			// still finishes — its declared checks did pass.
			if !r.SkipReview {
				if gaps, ok := r.runReview(ctx, m); !ok {
					if m.Replans < r.maxReplans() {
						if rerr := r.tryReplan(ctx, m, "review found gaps in the finished work:\n"+gaps); rerr == nil {
							continue
						}
						// A failed replan generation is not worth failing a
						// mission whose checks all passed — record and finish.
					}
					m.Notes = append(m.Notes, "review flagged unresolved gaps: "+agent.TruncateMiddle(gaps, 500))
				} else {
					m.Notes = append(m.Notes, "review: ok")
				}
			}

			m.Phase = PhaseDone
			if err := m.Save(r.Dir); err != nil {
				return "", err
			}

		case PhaseDone:
			return m.RenderReport(), nil

		case PhaseFailed:
			return m.RenderReport(), fmt.Errorf("mission failed")

		default:
			return r.fail(m, "unknown phase %q", m.Phase)
		}
	}
}

func (r *Runner) fail(m *Mission, format string, args ...any) (string, error) {
	reason := fmt.Sprintf(format, args...)
	r.event("mission FAILED: %s", reason)
	m.Phase = PhaseFailed
	_ = m.Save(r.Dir)
	return m.RenderReport(), fmt.Errorf("mission failed: %s", reason)
}

// --- explore phase ---

// greenFieldThreshold is the file count under which exploration is
// pointless — the plain listing already says everything.
const greenFieldThreshold = 3

// runExplore builds the mechanical codebase map and, when possible,
// enriches it with a bounded read-only annotation worker. Deliberately
// infallible: any part of it failing just means less map — planning
// proceeds either way, so this phase can never wedge a mission.
func (r *Runner) runExplore(ctx context.Context, m *Mission) {
	_, existing := WorkspaceListing(r.WS)
	if len(existing) < greenFieldThreshold {
		r.event("explore: %d files in workspace — skipping map generation", len(existing))
		return
	}

	r.event("explore: building codebase map (%d files)", len(existing))
	m.Map = BuildMap(r.WS)
	if m.Map == "" {
		return
	}

	if !r.SkipMapNotes {
		annotator := roles.Inspector(r.env(), "explore",
			prompts.MissionMapAnnotate, "explore.json")
		notes, err := annotator.Run(ctx, fmt.Sprintf(prompts.MissionMapAnnotateTask, m.Task, m.Map))
		if err != nil {
			r.event("explore: annotation worker failed (%v) — using mechanical map only", err)
		} else if notes = strings.TrimSpace(notes); notes != "" {
			m.Map += "\n\n## Notes (model-generated, verify before trusting)\n" +
				agent.TruncateMiddle(notes, 1500)
		}
	}

	// Best-effort artifact for the human; the ledger carries the real copy.
	_ = os.WriteFile(filepath.Join(r.Dir, "map.md"), []byte(m.Map), 0o644)
}

// --- plan phase ---

func (r *Runner) runPlan(ctx context.Context, m *Mission) error {
	listing, existing := WorkspaceListing(r.WS)
	// The map, when explore produced one, is a strictly richer view of the
	// same workspace — hand the planner that instead of the bare listing.
	// ExistingFiles stays sourced from the real walk either way.
	if m.Map != "" {
		listing = m.Map
	}
	if brief := SpecBrief(r.WS, m.Task, existing); brief != "" {
		listing = listing + "\n\n## Spec files (verbatim — plan FROM these, do not invent a different stack)\n" + brief
	}
	editNote := ""

	for revision := 0; ; revision++ {
		r.event("planning (%s)…", planAttemptLabel(revision))
		subtasks, warnings, err := GeneratePlan(ctx, r.Client, PlanRequest{
			Task:          m.Task,
			FileListing:   listing,
			ExistingFiles: existing,
			EditNote:      editNote,
			MaxTokens:     r.MaxTokens,
			Candidates:    r.PlanCandidates,
			OnEvent:       r.event,
		})
		if err != nil {
			return err
		}
		m.Subtasks = subtasks

		if r.ApprovePlan == nil {
			break
		}
		rendered := m.RenderPlan()
		if len(warnings) > 0 {
			rendered += "\n\nNew files the plan intends to create:\n  " + strings.Join(warnings, "\n  ")
		}
		approved, note := r.ApprovePlan(rendered)
		if approved {
			break
		}
		if note == "" {
			return fmt.Errorf("plan rejected by user")
		}
		if revision+1 >= r.maxPlanRevisions() {
			return fmt.Errorf("plan still rejected after %d revisions", r.maxPlanRevisions())
		}
		editNote = note
	}

	m.Phase = PhaseExecute
	m.Cursor = 0
	return m.Save(r.Dir)
}

func planAttemptLabel(revision int) string {
	if revision == 0 {
		return "initial"
	}
	return fmt.Sprintf("revision %d", revision)
}

// --- execute phase ---

// errSubtaskFailed marks "the check still fails after all fix attempts" —
// the one failure the Runner can answer with a replan instead of
// terminating. Wrapped errors carry the failing check output as evidence.
var errSubtaskFailed = errors.New("subtask failed its check")

// errInfra marks failures of the machinery itself — a dead backend, a
// network error — as opposed to failures of the work. Infra errors stop
// the mission WITHOUT moving it to PhaseFailed and without consuming fix
// or replan budgets: the ledger stays exactly where it was, and -resume
// continues from that point once the backend is back.
var errInfra = errors.New("backend failure")

func (r *Runner) runSubtask(ctx context.Context, m *Mission, sub *Subtask) error {
	// StatusRunning on entry means the process died mid-worker last time —
	// the one situation where the newest saved transcript should be
	// continued instead of starting a fresh attempt.
	resumeInterrupted := sub.Status == StatusRunning

	listing, _ := WorkspaceListing(r.WS)
	checkOut, ok, err := r.attempt(ctx, m, sub, CompileSeed(m, sub, listing), resumeInterrupted)
	if err != nil {
		return err
	}

	for !ok && sub.Attempts < 1+r.maxFixAttempts() {
		r.event("subtask %s: check FAILED — starting fix worker (%d/%d)",
			sub.ID, sub.Attempts, r.maxFixAttempts())
		listing, _ = WorkspaceListing(r.WS)
		fixSeed := CompileFixSeed(m, sub, agent.TruncateMiddle(checkOut, 2000), listing)
		checkOut, ok, err = r.attempt(ctx, m, sub, fixSeed, false)
		if err != nil {
			return err
		}
	}

	if ok {
		sub.Status = StatusDone
		m.Cursor++
		r.event("subtask %s: check PASSED", sub.ID)
		return m.Save(r.Dir)
	}

	sub.Status = StatusFailed
	_ = m.Save(r.Dir)
	r.event("subtask %s: check still FAILED after %d attempts", sub.ID, sub.Attempts)
	return fmt.Errorf("%w after %d attempts: %s",
		errSubtaskFailed, sub.Attempts, agent.TruncateMiddle(checkOut, 500))
}

// attempt runs one worker (fresh or resumed) against sub and then runs
// the subtask's check. Everything recorded is measured: which files the
// worker actually wrote (RunReport), what the check actually printed.
func (r *Runner) attempt(ctx context.Context, m *Mission, sub *Subtask, seed string, resumeInterrupted bool) (checkOut string, ok bool, err error) {
	resumeState := ""
	if resumeInterrupted {
		resumeState = r.latestWorkerState(sub.ID)
	}
	if resumeState == "" {
		sub.Attempts++
	}
	sub.Status = StatusRunning
	if err := m.Save(r.Dir); err != nil {
		return "", false, err
	}
	r.event("subtask %s (%s): worker attempt %d", sub.ID, sub.Title, sub.Attempts)

	worker := r.newWorker(sub)
	var result string
	var runErr error
	if resumeState != "" {
		if history, lerr := agent.LoadState(resumeState); lerr == nil && len(history) > 1 {
			r.event("subtask %s: resuming interrupted worker (%s)", sub.ID, filepath.Base(resumeState))
			result, runErr = worker.Resume(ctx, history, prompts.Resume)
		} else {
			result, runErr = worker.Run(ctx, seed)
		}
	} else {
		result, runErr = worker.Run(ctx, seed)
	}
	report := worker.Report()

	// Facts are measured, not claimed. Record them even when the worker
	// errored out — a max-steps death after three successful writes still
	// changed the workspace, and the check (and any human reading the
	// ledger) must see the whole truth.
	if len(report.MutatedPaths) > 0 {
		sub.AddFact("attempt %d wrote: %s", sub.Attempts, strings.Join(report.MutatedPaths, ", "))
		m.Mutated = unionPaths(m.Mutated, report.MutatedPaths)
	} else {
		sub.AddFact("attempt %d wrote: nothing", sub.Attempts)
	}
	if runErr != nil {
		sub.AddFact("attempt %d stopped early: %v", sub.Attempts, runErr)
	}
	sub.Summary = agent.TruncateMiddle(strings.TrimSpace(result), maxSummaryChars)

	// A worker error that ISN'T max-steps is infrastructure, not work: a
	// dead backend, a network failure. Fix workers and replans can't fix a
	// backend — spending their budgets on connection errors just converts
	// an outage into a fake string of "failed attempts" (live shape: a
	// koboldcpp crash mid-mission burned 2 fix attempts + the replan in
	// seconds). Stop the mission cleanly instead; it resumes exactly here.
	if runErr != nil && !errors.Is(runErr, agent.ErrMaxSteps) {
		_ = m.Save(r.Dir)
		return "", false, fmt.Errorf("%w (resume with -resume when the backend is back): %v", errInfra, runErr)
	}

	// The check is the verdict — not the worker's exit status. A worker
	// that hit MaxSteps but finished the actual work still passes; a
	// worker that returned a confident report over an empty file fails.
	checkOut, ok = RunCheck(ctx, sub.Check, r.WS)
	if ok {
		// The declared check passing only means the model verified what it
		// chose to verify. Everything files_hint promised has to be there
		// too, or a subtask can go green having produced one of the four
		// files it said it would.
		if out, passed := RunDerivedChecks(ctx, sub, r.WS); !passed {
			checkOut, ok = out, false
		}
	}
	if ok {
		sub.AddFact("check: PASSED — %s", agent.TruncateMiddle(checkOut, 200))
	} else {
		sub.AddFact("check: FAILED — %s", agent.TruncateMiddle(checkOut, 2000))
	}
	return checkOut, ok, m.Save(r.Dir)
}

// --- replan ---

// tryReplan replaces the not-yet-done tail of the plan with fresh
// subtasks for the remaining work. Executed subtasks are frozen with
// their measured history — the model plans forward only. Bounded by
// MaxReplans; over budget returns an error and the mission fails loudly.
func (r *Runner) tryReplan(ctx context.Context, m *Mission, reason string) error {
	if m.Replans >= r.maxReplans() {
		return fmt.Errorf("replan budget exhausted (%d used)", m.Replans)
	}
	r.event("replanning (%d/%d)", m.Replans+1, r.maxReplans())

	listing, existing := WorkspaceListing(r.WS)
	if brief := SpecBrief(r.WS, m.Task, existing); brief != "" {
		listing = listing + "\n\n## Spec files (verbatim — plan FROM these, do not invent a different stack)\n" + brief
	}
	editNote := ""
	for revision := 0; ; revision++ {
		newSubs, warnings, err := GenerateReplan(ctx, r.Client, ReplanRequest{
			PlanRequest: PlanRequest{
				Task:          m.Task,
				FileListing:   listing,
				ExistingFiles: existing,
				EditNote:      editNote,
				MaxTokens:     r.MaxTokens,
				Candidates:    r.PlanCandidates,
				OnEvent:       r.event,
			},
			Record: m.RenderReport(),
			Reason: reason,
		})
		if err != nil {
			return err
		}
		// Renumber after the frozen history; the failed subtask keeps its
		// place and its facts — replacement, not erasure.
		for i := range newSubs {
			newSubs[i].ID = fmt.Sprintf("s%d", len(m.Subtasks)+i+1)
		}

		if r.ApprovePlan != nil {
			rendered := renderReplan(m, newSubs, warnings)
			approved, note := r.ApprovePlan(rendered)
			if !approved && note == "" {
				return fmt.Errorf("replan rejected by user")
			}
			if !approved {
				if revision+1 >= r.maxPlanRevisions() {
					return fmt.Errorf("replan still rejected after %d revisions", r.maxPlanRevisions())
				}
				editNote = note
				continue
			}
		}

		// The budget is spent only when a new plan actually commits — a
		// replan aborted by a dead backend must not consume it.
		m.Replans++
		m.Cursor = len(m.Subtasks)
		m.Subtasks = append(m.Subtasks, newSubs...)
		m.Phase = PhaseExecute
		return m.Save(r.Dir)
	}
}

func renderReplan(m *Mission, newSubs []Subtask, warnings []string) string {
	var b strings.Builder
	b.WriteString("REPLAN — executed subtasks are kept as-is:\n")
	b.WriteString(m.RenderLedger())
	b.WriteString("\n\nNew subtasks for the remaining work:\n")
	renderSubtasks(&b, newSubs)
	if len(warnings) > 0 {
		b.WriteString("\nNew files the plan intends to create:\n  " + strings.Join(warnings, "\n  "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// newWorker builds the agent that executes one subtask attempt.
//
// ExpectsWrites is the interesting argument: the mission's checks are
// mechanical, so the general self-check round is a redundant extra call
// per subtask — EXCEPT for a worker about to finish having written
// nothing on a subtask that was supposed to write, which is the
// announce-without-write shape. A subtask that legitimately changes no
// files must not be argued with.
func (r *Runner) newWorker(sub *Subtask) *agent.Agent {
	return roles.Worker(r.env(), sub.ID, prompts.MissionWorker,
		workerStateName(sub), r.maxWorkerSteps(), sub.ExpectsWrites())
}

func (r *Runner) workerStateFile(sub *Subtask) string {
	return filepath.Join(r.Dir, "workers", workerStateName(sub))
}

// workerStateName is the transcript filename for one attempt, relative to
// the workers directory — which is what roles.Env resolves against.
func workerStateName(sub *Subtask) string {
	return fmt.Sprintf("%s-a%d.json", sub.ID, sub.Attempts)
}

// env is the shared configuration every agent this Runner builds needs.
// Before roles existed, the annotator, the worker and the reviewer each
// assembled their own agent.Config inline and each wrapped r.OnStep in a
// slightly different closure.
func (r *Runner) env() roles.Env {
	return roles.Env{
		Client:       r.Client,
		WS:           r.WS,
		Procs:        r.Procs,
		MaxTokens:    r.MaxTokens,
		ContextLimit: r.ContextLimit,
		StateDir:     filepath.Join(r.Dir, "workers"),
		OnStep:       r.OnStep,
		OnToolResult: r.OnToolResult,
		OnUsage:      r.OnUsage,
	}
}

// latestWorkerState returns the newest saved transcript for a subtask,
// or "" when this is a fresh start. Attempt files sort lexically within
// one subtask because attempts share the "sN-a" prefix.
func (r *Runner) latestWorkerState(subID string) string {
	pattern := filepath.Join(r.Dir, "workers", subID+"-a*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	newest := matches[len(matches)-1]
	if info, err := os.Stat(newest); err != nil || info.Size() == 0 {
		return ""
	}
	return newest
}

// --- review ---

// runReview inspects the finished work with a read-only worker, then
// narrows its free-text report to an ok/gaps verdict with the decision
// grammar. Infallible by policy: any error in the machinery means "ok" —
// the review is an extra net over already-passing checks, never a new way
// for a green mission to die.
func (r *Runner) runReview(ctx context.Context, m *Mission) (gaps string, ok bool) {
	r.event("review: inspecting the finished work")

	reviewer := roles.Inspector(r.env(), "review", prompts.MissionReview, "review.json")
	report, err := reviewer.Run(ctx, fmt.Sprintf(prompts.MissionReviewTask,
		m.Task, m.RenderPlan(), m.RenderReport()))
	if err != nil {
		r.event("review worker failed (%v) — skipping review", err)
		return "", true
	}

	resp, err := r.Client.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf(prompts.MissionVerdict, agent.TruncateMiddle(strings.TrimSpace(report), 3000)),
		}},
		Grammar:     DecisionGrammar,
		JSONSchema:  DecisionSchema,
		Temperature: planTemperature,
		MaxTokens:   r.MaxTokens,
	})
	if err != nil {
		r.event("review verdict call failed (%v) — skipping review", err)
		return "", true
	}
	var v struct {
		Verdict string `json:"verdict"`
		Notes   string `json:"notes"`
	}
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(resp.Message.Content)), &v); jerr != nil {
		r.event("review verdict unparseable (%v) — skipping review", jerr)
		return "", true
	}
	if v.Verdict != "gaps" {
		r.event("review: ok")
		return "", true
	}
	r.event("review: gaps — %s", agent.TruncateMiddle(v.Notes, 200))
	gaps = v.Notes
	if detail := strings.TrimSpace(report); detail != "" {
		gaps += "\n\nReviewer detail:\n" + agent.TruncateMiddle(detail, 1500)
	}
	return gaps, false
}

// --- verify phase ---

// runVerifyChecks re-runs every done subtask's check — later subtasks can
// break earlier ones — plus the mission-level VerifyCmd, and returns the
// regressions. The Run loop decides whether they trigger a replan or a
// loud failure.
func (r *Runner) runVerifyChecks(ctx context.Context, m *Mission) []string {
	var regressions []string
	for i := range m.Subtasks {
		sub := &m.Subtasks[i]
		if sub.Status != StatusDone || sub.Check.Type == "" || sub.Check.Type == "none" {
			continue
		}
		out, ok := RunCheck(ctx, sub.Check, r.WS)
		if ok {
			out, ok = RunDerivedChecks(ctx, sub, r.WS)
		}
		if !ok {
			sub.AddFact("final verify: FAILED — %s", agent.TruncateMiddle(out, 500))
			regressions = append(regressions, fmt.Sprintf("%s: %s", sub.ID, agent.TruncateMiddle(out, 200)))
		}
	}

	if r.VerifyCmd != "" {
		r.event("running mission verify command: %s", r.VerifyCmd)
		out, ok := RunCheck(ctx, Check{Type: "shell", Cmd: r.VerifyCmd}, r.WS)
		if !ok {
			regressions = append(regressions, "verify-cmd: "+agent.TruncateMiddle(out, 500))
		}
	}

	if len(regressions) > 0 {
		_ = m.Save(r.Dir)
	}
	return regressions
}
