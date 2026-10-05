// Package mission is the harness-owned executive for complex tasks. The
// core loop in internal/agent stays reactive and model-driven; mission
// mode wraps it in a deterministic state machine (plan → execute →
// verify) whose source of truth is a persistent ledger on disk, not the
// conversation. A weak model can't hold a Doom-sized plan in its head
// across a growing transcript — so the harness holds it instead: every
// worker gets a fresh context seeded from the ledger, and everything the
// ledger records about completed work is a measured fact (files actually
// written, checks actually run), never the model's own claim.
package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Phase is where a mission currently stands. Transitions are decided by
// the Runner in Go, never by the model.
type Phase string

const (
	PhaseExplore Phase = "explore" // reserved for the codebase-map stage
	PhasePlan    Phase = "plan"
	PhaseExecute Phase = "execute"
	PhaseVerify  Phase = "verify"
	PhaseDone    Phase = "done"
	PhaseFailed  Phase = "failed"
)

// Subtask statuses. Plain strings in JSON for easy hand-inspection.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// Check is how a subtask's completion gets verified — mechanically, by
// the harness, after the worker finishes. It's an enum of shapes rather
// than a free-form shell string on purpose: narrowing what the planner
// can ask for is the same decision-narrowing lever the plan grammar
// applies to everything else. file_exists / file_absent /
// content_contains / http run natively in Go; shell reuses the same
// OS-aware exec as -verify-cmd.
type Check struct {
	Type     string `json:"type"`               // "shell" | "file_exists" | "file_absent" | "content_contains" | "http" | "none"
	Cmd      string `json:"cmd,omitempty"`      // shell: the command line
	Path     string `json:"path,omitempty"`     // file_exists / file_absent / content_contains
	Contains string `json:"contains,omitempty"` // content_contains: substring that must appear
	URL      string `json:"url,omitempty"`      // http: expect a 200 response
}

// Render describes the check in plain language for prompts and reports.
func (c Check) Render() string {
	switch c.Type {
	case "shell":
		return fmt.Sprintf("shell command must succeed: %s", c.Cmd)
	case "file_exists":
		return fmt.Sprintf("file must exist: %s", c.Path)
	case "file_absent":
		return fmt.Sprintf("file must be absent: %s", c.Path)
	case "content_contains":
		return fmt.Sprintf("file %s must contain %q", c.Path, c.Contains)
	case "http":
		return fmt.Sprintf("GET %s must return 200", c.URL)
	default:
		return "none"
	}
}

// fingerprint uniquely identifies a check so two subtasks can't pass on
// the same evidence.
func (c Check) fingerprint() string {
	return c.Type + "|" + c.Cmd + "|" + normalizePlanPath(c.Path) + "|" + c.Contains + "|" + c.URL
}

// Subtask is one unit of plan. The model authors the top fields once,
// during planning; the fields below the marker are harness-owned and
// never model-written — they record what actually happened.
type Subtask struct {
	ID         string   `json:"id"`        // "s1", "s2", ...
	Milestone  string   `json:"milestone"` // grouping label only, no nesting
	Title      string   `json:"title"`
	Goal       string   `json:"goal"`       // 1-3 imperative sentences
	Acceptance []string `json:"acceptance"` // verifiable statements
	FilesHint  []string `json:"files_hint"` // paths the planner expects to touch
	Check      Check    `json:"check"`

	// Harness-owned from here down.
	Status   string   `json:"status"`
	Attempts int      `json:"attempts"`
	Facts    []string `json:"facts"`   // "wrote: a.js, b.html", "check: PASSED"
	Summary  string   `json:"summary"` // worker's final message, hard-truncated
}

// Mission is the whole ledger: the verbatim task, the plan, and where
// execution stands. It is the single source of truth — workers see a
// rendering of it, never the transcript of any previous worker.
type Mission struct {
	ID       string    `json:"id"`
	Task     string    `json:"task"` // user request VERBATIM, never paraphrased
	Phase    Phase     `json:"phase"`
	Map      string    `json:"map,omitempty"` // codebase map artifact (explore stage)
	Subtasks []Subtask `json:"subtasks"`
	Replans  int       `json:"replans"`
	Cursor   int       `json:"cursor"` // index of the subtask being executed

	// Mutated accumulates every workspace path any worker actually wrote
	// (from agent.RunReport, not model claims), mission-wide and
	// deduplicated — later workers get it as "files likely involved".
	Mutated []string `json:"mutated,omitempty"`

	// Notes are mission-level observations for the final report — e.g.
	// the review stage's verdict, or gaps it flagged that no replan
	// budget remained to address.
	Notes []string `json:"notes,omitempty"`
}

const fileName = "mission.json"

// Load reads a mission ledger from dir (.tars/tasks/<id>).
func Load(dir string) (*Mission, error) {
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		return nil, fmt.Errorf("read mission file: %w", err)
	}
	var m Mission
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode mission file: %w", err)
	}
	return &m, nil
}

// Exists reports whether dir holds a mission ledger — how resume tells a
// mission run apart from a direct-mode run.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, fileName))
	return err == nil
}

// Save writes the ledger atomically (temp file + rename), so a crash
// mid-write can't leave a half-serialized ledger that poisons resume.
func (m *Mission) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create mission dir: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mission: %w", err)
	}
	tmp := filepath.Join(dir, fileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write mission temp file: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, fileName)); err != nil {
		return fmt.Errorf("replace mission file: %w", err)
	}
	return nil
}

// Current returns the subtask at the cursor, or nil past the end.
func (m *Mission) Current() *Subtask {
	if m.Cursor < 0 || m.Cursor >= len(m.Subtasks) {
		return nil
	}
	return &m.Subtasks[m.Cursor]
}

// RenderLedger is the compact plan-status view injected into every worker
// seed — the reinjection that lets a fresh context know where the mission
// stands without ever seeing another worker's transcript. One line per
// subtask; the current one is marked NOW; facts ride along on finished
// ones so later workers know what was actually produced.
func (m *Mission) RenderLedger() string {
	var b strings.Builder
	for i, s := range m.Subtasks {
		status := s.Status
		if i == m.Cursor && (s.Status == StatusPending || s.Status == StatusRunning) {
			status = "NOW"
		}
		fmt.Fprintf(&b, "%s [%s] %s", s.ID, status, s.Title)
		if len(s.Facts) > 0 {
			fmt.Fprintf(&b, "  (%s)", strings.Join(s.Facts, "; "))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// RenderPlan is the full human-readable plan for the approval gate and
// the final report: goals, acceptance criteria, and checks spelled out.
func (m *Mission) RenderPlan() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task: %s\n\n", m.Task)
	renderSubtasks(&b, m.Subtasks)
	return strings.TrimRight(b.String(), "\n")
}

// renderSubtasks writes the detailed subtask view used by RenderPlan and
// by the replan approval gate (which renders only the new subtasks).
func renderSubtasks(b *strings.Builder, subs []Subtask) {
	milestone := ""
	for _, s := range subs {
		if s.Milestone != milestone {
			milestone = s.Milestone
			fmt.Fprintf(b, "== %s ==\n", milestone)
		}
		fmt.Fprintf(b, "%s: %s\n", s.ID, s.Title)
		fmt.Fprintf(b, "   goal: %s\n", s.Goal)
		for _, a := range s.Acceptance {
			fmt.Fprintf(b, "   accept: %s\n", a)
		}
		if len(s.FilesHint) > 0 {
			fmt.Fprintf(b, "   files: %s\n", strings.Join(s.FilesHint, ", "))
		}
		fmt.Fprintf(b, "   check: %s\n", s.Check.Render())
	}
}

// RenderReport is the honest end-of-mission report: every subtask, its
// status, and its recorded facts. Printed on DONE and — more importantly
// — on FAILED: a weak-model harness must fail loudly with everything it
// measured, not fake success.
func (m *Mission) RenderReport() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mission %s: %s\n", m.ID, m.Phase)
	for _, s := range m.Subtasks {
		fmt.Fprintf(&b, "%s [%s] %s\n", s.ID, s.Status, s.Title)
		for _, f := range s.Facts {
			fmt.Fprintf(&b, "   %s\n", f)
		}
	}
	for _, n := range m.Notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	return strings.TrimRight(b.String(), "\n")
}

// AddFact appends a measured fact to a subtask's record.
func (s *Subtask) AddFact(format string, args ...any) {
	s.Facts = append(s.Facts, fmt.Sprintf(format, args...))
}

// ExpectsWrites reports whether finishing this subtask with zero file
// writes is a failure rather than a legitimate outcome. Everything the
// planner is allowed to emit implies a write except the one case it's
// explicitly told to reserve for work that changes no files: check
// "none" with no files_hint. The distinction matters because the worker
// loop refuses a zero-write finish — on a subtask that was never meant
// to write, that refusal would be arguing with a model that is right.
func (s *Subtask) ExpectsWrites() bool {
	if len(s.FilesHint) > 0 {
		return true
	}
	return s.Check.Type != "" && s.Check.Type != "none"
}
