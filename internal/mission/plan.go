package mission

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
	"github.com/keshon/tars/internal/tools"
)

// planTemperature is deliberately low: structured output wants stability,
// but not 0.0 — greedy sampling under a grammar makes weak models loop on
// repeated tokens.
const planTemperature = 0.3

// PlanRequest carries everything one plan-generation call needs.
type PlanRequest struct {
	Task          string
	FileListing   string          // rendered workspace listing, budget-capped
	ExistingFiles map[string]bool // real relative paths, for hallucination checks
	EditNote      string          // human rejection note from the approval gate, if any
	MaxTokens     int

	// Candidates is how many plans to generate and score before picking
	// one. Zero means defaultPlanCandidates; 1 disables the search and
	// keeps the first valid plan, which is what the A/B comparison needs.
	Candidates int

	// OnEvent, if set, narrates the search — which candidate was kept and
	// what the alternatives scored.
	OnEvent func(format string, args ...any)
}

// GeneratePlan makes one tool-free, grammar-constrained model call and
// validates the result structurally in Go. Grammar guarantees syntax; the
// validator guarantees structure; the human approval gate (in the Runner)
// handles semantics. One retry carrying the specific validation errors,
// then a loud failure with the raw output attached — never a loop.
func GeneratePlan(ctx context.Context, client llm.Client, req PlanRequest) (subtasks []Subtask, warnings []string, err error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompts.MissionPlan},
		{Role: llm.RoleUser, Content: fmt.Sprintf(prompts.MissionPlanTask, req.Task, req.FileListing)},
	}
	return generateSubtasks(ctx, client, messages, req)
}

// ReplanRequest carries everything a replan call needs beyond PlanRequest:
// the execution record so far (statuses + measured facts) and the reason
// the old plan stopped.
type ReplanRequest struct {
	PlanRequest
	Record string // Mission.RenderReport() — statuses and facts, verbatim
	Reason string // why replanning: the failing check output, review gaps
}

// GenerateReplan asks for a plan covering only the remaining work. The
// caller keeps the already-executed subtasks frozen (with their measured
// history) and appends the result — the model never gets to rewrite what
// already happened.
func GenerateReplan(ctx context.Context, client llm.Client, req ReplanRequest) (subtasks []Subtask, warnings []string, err error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompts.MissionPlan},
		{Role: llm.RoleUser, Content: fmt.Sprintf(prompts.MissionReplan,
			req.Task, req.Record, req.Reason, req.FileListing)},
	}
	return generateSubtasks(ctx, client, messages, req.PlanRequest)
}

// defaultPlanCandidates is how many plans are generated and scored.
//
// The plan is a single sample from the weakest link and everything
// downstream inherits it: a bad check wastes every fix worker, and a
// replan is another single sample from the same model that just made the
// mistake. Live, on one task in one afternoon: the same three-file
// scaffold was rejected by validation twice in one run and planned
// cleanly in the next. Plan quality is a coin flip, and a bad flip costs
// the whole mission.
//
// This is the cheapest place in the system to spend extra calls. A plan
// call is a couple of thousand prompt tokens against a mission that
// burns twenty worker calls executing whatever it says.
const defaultPlanCandidates = 3

// generateSubtasks generates several candidate plans, scores them
// mechanically, and returns the best. If none validate, it falls back to
// one repair retry carrying the specific errors — independent sampling
// finds a good plan, repair fixes a nearly-good one, and they solve
// different problems.
func generateSubtasks(ctx context.Context, client llm.Client, messages []llm.Message, req PlanRequest) (subtasks []Subtask, warnings []string, err error) {
	if req.EditNote != "" {
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf(prompts.MissionPlanEdit, req.EditNote),
		})
	}

	candidates := req.Candidates
	if candidates <= 0 {
		candidates = defaultPlanCandidates
	}

	ask := func(msgs []llm.Message) (string, error) {
		resp, cerr := client.Chat(ctx, llm.ChatRequest{
			Messages:    msgs,
			Grammar:     PlanGrammar,
			JSONSchema:  PlanSchema,
			Temperature: planTemperature,
			MaxTokens:   req.MaxTokens,
		})
		if cerr != nil {
			// A chat failure here is the backend, not the plan — mark it
			// infra so the Runner stops resumably instead of failing the
			// mission (or burning the replan budget it was called with).
			return "", fmt.Errorf("%w: plan call: %v", errInfra, cerr)
		}
		return resp.Message.Content, nil
	}

	var (
		best      []Subtask
		bestWarns []string
		bestScore float64
		found     bool
		scores    []string
		lastRaw   string
		lastErrs  []string
	)

	for i := 0; i < candidates; i++ {
		raw, cerr := ask(messages)
		if cerr != nil {
			if found {
				break // a backend that died mid-search still leaves a usable plan
			}
			return nil, nil, cerr
		}
		subs, warns, errs := parseAndValidate(raw, req.Task, req.ExistingFiles)
		if len(errs) > 0 {
			lastRaw, lastErrs = raw, errs
			scores = append(scores, "rejected")
			continue
		}
		score := scorePlan(subs)
		scores = append(scores, fmt.Sprintf("%.1f", score))
		if !found || score > bestScore {
			best, bestWarns, bestScore, found = subs, warns, score, true
		}
	}

	if found {
		if req.OnEvent != nil && candidates > 1 {
			req.OnEvent("plan: kept the best of %d candidates (scores: %s)",
				candidates, strings.Join(scores, ", "))
		}
		return best, bestWarns, nil
	}

	// Nothing validated. Repair is a different lever from resampling: it
	// carries the exact errors back, which is what actually fixes a plan
	// that was close.
	messages = append(messages,
		llm.Message{Role: llm.RoleAssistant, Content: lastRaw},
		llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf(prompts.MissionPlanRetry, strings.Join(lastErrs, "\n"))},
	)
	raw, cerr := ask(messages)
	if cerr != nil {
		return nil, nil, cerr
	}
	subs, warns, errs := parseAndValidate(raw, req.Task, req.ExistingFiles)
	if len(errs) == 0 {
		return subs, warns, nil
	}
	return nil, nil, fmt.Errorf("plan rejected by validation after %d candidates and a repair attempt: %s\nraw output:\n%s",
		candidates, strings.Join(errs, "; "), raw)
}

// scorePlan ranks plans that already passed validation. Higher is better.
//
// Purely mechanical: asking a weak model which of its own plans is best
// would just add another coin flip. Two things are measured.
//
// Check strength, averaged so a plan is not rewarded for having more
// subtasks — the validator already bounds decomposition at both ends. A
// check that builds or tests proves the most, a symbol in a file proves
// something specific, mere existence proves the least.
//
// File overlap, penalised: two subtasks naming the same file in
// files_hint means the second rewrites what the first produced, which is
// the shape that ends with one worker undoing another's work.
func scorePlan(subs []Subtask) float64 {
	if len(subs) == 0 {
		return 0
	}
	var strength float64
	owner := map[string]int{}
	overlaps := 0
	for i := range subs {
		strength += checkStrength(subs[i].Check)
		for _, p := range subs[i].FilesHint {
			p = normalizePlanPath(p)
			if p == "" {
				continue
			}
			owner[p]++
			if owner[p] == 2 {
				overlaps++
			}
		}
	}
	return strength/float64(len(subs)) - float64(overlaps)
}

func checkStrength(c Check) float64 {
	switch c.Type {
	case "shell":
		cmd := strings.ToLower(c.Cmd)
		// A command that compiles or runs the work proves far more than
		// one that merely exits zero.
		for _, strong := range []string{"test", "build", "vet", "lint", "make"} {
			if strings.Contains(cmd, strong) {
				return 3
			}
		}
		return 2
	case "content_contains":
		return 2
	case "file_exists", "file_absent", "http":
		return 1
	default:
		return 0
	}
}

func parseAndValidate(raw, task string, existing map[string]bool) (subtasks []Subtask, warnings, errs []string) {
	raw = stripThink(raw)
	var doc struct {
		Subtasks []Subtask `json:"subtasks"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &doc); err != nil {
		return nil, nil, []string{fmt.Sprintf("output is not valid JSON: %v", err)}
	}
	subtasks = doc.Subtasks

	if len(subtasks) == 0 {
		return nil, nil, []string{"plan has no subtasks"}
	}
	if len(subtasks) > 8 {
		return nil, nil, []string{fmt.Sprintf("plan has %d subtasks, maximum is 8", len(subtasks))}
	}

	// A check identical to an earlier subtask's check cannot distinguish
	// its own subtask's work — live shape: four subtasks all checked
	// "file_exists: plan.md", so once s1 created the file, s2-s4 were
	// unverifiable.
	checkOwner := make(map[string]string)

	for i := range subtasks {
		s := &subtasks[i]
		// IDs and statuses are harness-owned — overwrite instead of
		// validating; a weak model shouldn't be able to break sequencing.
		s.ID = fmt.Sprintf("s%d", i+1)
		s.Status = StatusPending
		s.Attempts = 0
		s.Facts = nil
		s.Summary = ""

		if strings.TrimSpace(s.Goal) == "" {
			errs = append(errs, fmt.Sprintf("%s: goal is empty", s.ID))
		}
		if strings.TrimSpace(s.Title) == "" {
			errs = append(errs, fmt.Sprintf("%s: title is empty", s.ID))
		}
		if len(s.Acceptance) == 0 {
			errs = append(errs, fmt.Sprintf("%s: no acceptance criteria", s.ID))
		}

		if msg := readOnlySubtaskErr(s, existing); msg != "" {
			errs = append(errs, msg)
		}

		if note := repairUnrunnableCheck(s); note != "" {
			warnings = append(warnings, note)
		}
		errs = append(errs, validateCheck(s, existing)...)

		if s.Check.Type != "" && s.Check.Type != "none" {
			fp := s.Check.fingerprint()
			if owner, dup := checkOwner[fp]; dup {
				errs = append(errs, fmt.Sprintf("%s: has the same check as %s — a check must verify its "+
					"OWN subtask's work. Merge the two subtasks into one, or give this one a check that "+
					"detects its specific contribution (e.g. content_contains for a symbol it adds)",
					s.ID, owner))
			} else {
				checkOwner[fp] = s.ID
			}
		}

		// files_hint paths that don't exist yet are fine — most plans
		// create files — but they must be surfaced to the human as
		// new-file intents, never silently trusted as existing.
		for _, p := range s.FilesHint {
			if !existing[normalizePlanPath(p)] {
				warnings = append(warnings, fmt.Sprintf("%s: %s does not exist yet (will be created)", s.ID, p))
			}
		}
	}

	// One blob for a multi-file task is the reactive loop with ceremony.
	if msg := planTooCoarse(task, subtasks); msg != "" {
		errs = append(errs, msg)
	}
	return subtasks, warnings, errs
}

// stripThink drops Qwen-style <think>…</think> wrappers. When the plan
// grammar is ignored by the backend, thinking models dump thousands of
// tokens of reasoning before the JSON — Unmarshal then fails or the
// validator never sees the real plan.
func stripThink(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			return strings.TrimSpace(s)
		}
		rest := s[start+len("<think>"):]
		end := strings.Index(rest, "</think>")
		if end < 0 {
			return strings.TrimSpace(s[:start])
		}
		s = s[:start] + rest[end+len("</think>"):]
	}
}

// readOnlySubtaskErr rejects "read/understand the plan" units. Workers
// already have read tools; anything "understood" dies with the worker.
// Live shape 2026-07-14: s1 read PLAN.md with check none → retry "fixed"
// it to file_exists on the already-existing file → still rejected.
func readOnlySubtaskErr(s *Subtask, existing map[string]bool) string {
	if !allHintsExist(s.FilesHint, existing) {
		return ""
	}
	if len(s.FilesHint) == 0 {
		return ""
	}
	low := strings.ToLower(s.Title + " " + s.Goal)
	readish := strings.Contains(low, "read ") || strings.Contains(low, "understand") ||
		strings.Contains(low, "analyz") || strings.Contains(low, "review") ||
		strings.Contains(low, "inspect") || strings.Contains(low, "study ") ||
		strings.Contains(low, "look at")
	noneOrVacuous := s.Check.Type == "" || s.Check.Type == "none" ||
		(s.Check.Type == "file_exists" && existing[normalizePlanPath(s.Check.Path)])
	if readish || noneOrVacuous {
		return fmt.Sprintf("%s: read-only subtask — REMOVE it. Workers read existing files themselves; "+
			"only plan subtasks that CREATE or MODIFY files. Put any \"read the plan\" work into the "+
			"first implementation subtask's goal", s.ID)
	}
	return ""
}

func allHintsExist(hints []string, existing map[string]bool) bool {
	if len(hints) == 0 {
		return false
	}
	for _, p := range hints {
		if !existing[normalizePlanPath(p)] {
			return false
		}
	}
	return true
}

func validateCheck(s *Subtask, existing map[string]bool) (errs []string) {
	c := s.Check
	switch c.Type {
	case "none":
		if len(s.FilesHint) > 0 && !allHintsExist(s.FilesHint, existing) {
			errs = append(errs, fmt.Sprintf("%s: touches files (%s) but has no check — pick file_exists, file_absent, content_contains, or shell",
				s.ID, strings.Join(s.FilesHint, ", ")))
		}
	case "file_exists":
		path := normalizePlanPath(c.Path)
		switch {
		case strings.TrimSpace(path) == "":
			errs = append(errs, fmt.Sprintf("%s: file_exists check has no path", s.ID))
		case existing[path]:
			// Live failure shape: a plan checked file_exists on plan.md,
			// which already existed — the subtask "passed" having done
			// nothing. A check that is already true before any work
			// verifies nothing.
			errs = append(errs, fmt.Sprintf("%s: file_exists check on %q verifies nothing — the file "+
				"already exists. Point it at a file this subtask CREATES, use content_contains for a "+
				"symbol you add, or use a shell check that proves the change", s.ID, c.Path))
		case isDirOf(existing, path):
			// Live failure shape: a plan checked file_exists on
			// internal/tools (a directory) — unsatisfiable, and the fix
			// loop burned three workers trying to satisfy it.
			errs = append(errs, fmt.Sprintf("%s: file_exists check path %q is a directory — "+
				"the check can never pass. Name a specific file", s.ID, c.Path))
		}
	case "file_absent":
		path := normalizePlanPath(c.Path)
		switch {
		case strings.TrimSpace(path) == "":
			errs = append(errs, fmt.Sprintf("%s: file_absent check has no path", s.ID))
		case !existing[path]:
			// Mirror of the file_exists vacuity rule: absence of a file
			// that was never there proves nothing. Name a file the
			// workspace listing shows, which this subtask removes.
			errs = append(errs, fmt.Sprintf("%s: file_absent check on %q verifies nothing — the file "+
				"does not exist yet. Point it at a file this subtask DELETES, or use content_contains "+
				"for what remains", s.ID, c.Path))
		}
	case "content_contains":
		path := normalizePlanPath(c.Path)
		needle := strings.TrimSpace(strings.Trim(c.Contains, `"'`))
		s.Check.Contains = needle // normalize planner-escaped quotes like "\"vite\""
		switch {
		case strings.TrimSpace(path) == "":
			errs = append(errs, fmt.Sprintf("%s: content_contains check has no path", s.ID))
		case isDirOf(existing, path):
			errs = append(errs, fmt.Sprintf("%s: content_contains path %q is a directory — name a file", s.ID, c.Path))
		case needle == "":
			errs = append(errs, fmt.Sprintf("%s: content_contains check has empty contains", s.ID))
		case len(needle) < 3:
			errs = append(errs, fmt.Sprintf("%s: content_contains %q is too short — use a distinctive symbol (function name, export, tag)", s.ID, c.Contains))
		}
	case "shell":
		cmd := strings.TrimSpace(strings.ToLower(c.Cmd))
		switch {
		case cmd == "":
			errs = append(errs, fmt.Sprintf("%s: shell check has no cmd", s.ID))
		case vacuousShellCheck(cmd):
			errs = append(errs, fmt.Sprintf("%s: shell check %q always succeeds and verifies nothing", s.ID, c.Cmd))
		default:
			if reason := mutatingShellCheck(cmd); reason != "" {
				errs = append(errs, fmt.Sprintf("%s: shell check %q %s", s.ID, c.Cmd, reason))
			} else if reason := unsatisfiableShellCheck(cmd); reason != "" {
				errs = append(errs, fmt.Sprintf("%s: shell check %q %s", s.ID, c.Cmd, reason))
			} else if reason := missingCommand(cmd); reason != "" {
				errs = append(errs, fmt.Sprintf("%s: shell check %q %s", s.ID, c.Cmd, reason))
			}
		}
	case "http":
		if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
			errs = append(errs, fmt.Sprintf("%s: http check needs an http(s) URL, got %q", s.ID, c.URL))
		}
	default:
		errs = append(errs, fmt.Sprintf("%s: unknown check type %q", s.ID, c.Type))
	}
	return errs
}

// planTooCoarse rejects "one subtask does everything" when the task
// already names multiple deliverable files — that shape is the reactive
// loop with extra steps, not a real decomposition.
func planTooCoarse(task string, subtasks []Subtask) string {
	if len(subtasks) != 1 {
		return ""
	}
	files := FileMentions(task)
	if len(files) < 2 {
		return ""
	}
	return fmt.Sprintf("task names %d files (%s) but plan has only 1 subtask — split into one subtask per file/module (merge only tightly-coupled edits to the SAME file)",
		len(files), strings.Join(files, ", "))
}

// normalizePlanPath makes a model-written path comparable against the
// walk-produced existing-files set: forward slashes, no ./ prefix.
func normalizePlanPath(p string) string {
	return strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "./")
}

// isDirOf reports whether path is a directory, judged purely from the
// existing-files set: it's a directory iff some real file lives under it.
func isDirOf(existing map[string]bool, path string) bool {
	prefix := strings.TrimRight(path, "/") + "/"
	for f := range existing {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}

// vacuousShellCheck flags commands that succeed regardless of whether any
// work was done — a check that can't fail verifies nothing. Matched on
// the first word (lowercased): a real weak-model plan produced `ls -l` as
// a "check" and it passed vacuously, so this is a live failure shape, not
// paranoia.
// unsatisfiableShellCheck flags commands that fail regardless of whether
// the work was done, returning why. The opposite hazard to
// vacuousShellCheck and the more expensive one: a check that can never
// pass turns every fix attempt into a guessing game about the command
// rather than the code, and the fix loop spends its whole budget there.
//
// A live plan checked `go test ./internal/tools/parseurl_test.go`. A
// single _test.go file cannot be compiled alone when it depends on the
// package around it, so the check failed on correct code; three fix
// workers and a replan were spent re-running variants of the command.
func unsatisfiableShellCheck(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) < 2 || fields[0] != "go" {
		return ""
	}
	switch fields[1] {
	case "test", "build", "vet", "run":
	default:
		return ""
	}
	for _, arg := range fields[2:] {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if strings.HasSuffix(arg, "_test.go") {
			return "can never pass — a _test.go file cannot be compiled on its own " +
				"when it uses the package around it. Name the package instead, " +
				"e.g. `go test ./internal/tools/`"
		}
	}
	return ""
}

// missingCommand reports a check whose very first word is not an
// executable on this machine, which makes it unsatisfiable for a reason
// that has nothing to do with the work.
//
// The planner writes checks against the environment it imagines rather
// than the one it has, and POSIX habits on a Windows host are the usual
// shape. Shell builtins are excluded because LookPath cannot see them:
// `if`, `echo` and their friends exist inside cmd.exe and sh with no file
// on disk, so testing them would reject working checks.
func missingCommand(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	first := fields[0]
	// Anything with shell syntax in it is a pipeline or a builtin
	// construct, not a bare program name, and is beyond this check.
	if strings.ContainsAny(cmd, "|&<>()") || shellBuiltins[first] {
		return ""
	}
	if _, err := exec.LookPath(first); err != nil {
		return fmt.Sprintf("starts with %q, which is not an executable on this machine - "+
			"the check would fail whatever the work did. Use a command this host has", first)
	}
	// LookPath succeeding only proves a file with that name exists — stale
	// shims (pipx/uv wrappers pointing at deleted interpreters) pass it
	// and die at start. Prove it runs with a side-effect-free --version
	// probe; a broken binary fails the check whatever the work did, the
	// same as a missing one.
	//
	// Validation must stay fast, so the probe carries its own short
	// timeout rather than the caller's context.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch res := tools.ProbeCommand(ctx, first, "--version"); res.Status {
	case "ok":
		return ""
	case "missing":
		// Raced with an uninstall between LookPath and exec — report the
		// probe's wording, which names the fix.
		return fmt.Sprintf("starts with %q: %s", first, res.Hint)
	default:
		return fmt.Sprintf("starts with %q, which %s", first, res.Hint)
	}
}

// shellBuiltins are the constructs LookPath cannot find because they have
// no file on disk.
var shellBuiltins = map[string]bool{
	"if": true, "for": true, "echo": true, "cd": true, "set": true,
	"type": true, "dir": true, "exit": true, "call": true, "rem": true,
	"test": true, "true": true, "false": true,
}

func vacuousShellCheck(cmd string) bool {
	first := cmd
	if i := strings.IndexAny(first, " \t"); i >= 0 {
		first = first[:i]
	}
	switch first {
	case "echo", "ls", "dir", "pwd", "cd", "true", "cat", "type", "whoami", "date", "exit":
		return true
	}
	return false
}

// mutatingShellCheck flags a check that performs the subtask instead of
// verifying it.
//
// Observed three runs out of three on a probe that had passed before it.
// The subtask was "create numbers.txt" and the model gave it this check:
//
//	python -c "with open('numbers.txt', 'w') as f: f.write('3\n5\n34\n')"
//
// It writes the file. Had it run, the check would have passed for every
// plan whether or not the worker did anything, because the check was the
// work. It did not run: the embedded quoting does not survive cmd.exe, so
// it exited 1, burned the three fix attempts and killed the mission.
// Either outcome is bad, and they are the same defect — a check must
// observe, not act.
//
// Deliberately narrow. Redirection and a write-mode open are unambiguous;
// a mutating first word is judged only in that position, so `grep -q '>'
// f` and `python stats.py | findstr /x 42` stay legal. A check that
// mutates in some way not listed here still gets through, and that is the
// right trade against rejecting plans that were fine.
func mutatingShellCheck(cmd string) string {
	// Redirection, ignoring fd duplication like 2>&1 and >&2.
	for i := 0; i < len(cmd); i++ {
		if cmd[i] != '>' {
			continue
		}
		if i+1 < len(cmd) && cmd[i+1] == '&' {
			continue // >&
		}
		if i > 0 && cmd[i-1] == '&' {
			continue // &> — still a redirect, but caught by the next rule
		}
		if i > 0 && (cmd[i-1] == '=' || cmd[i-1] == '<' || cmd[i-1] == '-') {
			continue // >=, <>, ->
		}
		if i > 0 && cmd[i-1] == '2' && i+1 < len(cmd) && cmd[i+1] == '&' {
			continue
		}
		return "writes instead of verifying (shell redirection) — a check must " +
			"observe the result, not produce it"
	}

	// A write-mode open in an inline script.
	if strings.Contains(cmd, "open(") {
		for _, mode := range []string{`'w'`, `"w"`, `'a'`, `"a"`, `'x'`, `"x"`,
			`'wb'`, `"wb"`, `'w+'`, `"w+"`} {
			if strings.Contains(cmd, mode) {
				return "writes instead of verifying (opens a file for writing) — " +
					"a check must observe the result, not produce it"
			}
		}
	}

	// A mutating command in leading position. Only there: these words are
	// harmless as arguments or inside a pattern.
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "touch", "mkdir", "rm", "del", "rmdir", "mv", "ren", "rename",
		"cp", "copy", "tee", "truncate":
		return "runs a command that changes the workspace — a check must " +
			"observe the result, not produce it"
	}
	return ""
}

// repairUnrunnableCheck downgrades a shell check that cannot run on this
// host, instead of letting it sink the plan that carries it.
//
// The plan and the check are separate judgements, and only one of them is
// the work. Live: a replan correctly diagnosed a save/load bug and
// proposed exactly the right fix - store_var and get_var instead of JSON,
// to stop integers coming back as floats - and attached the check
// `grep 'store_var' Save.gd`. This host has no grep. The whole plan was
// rejected, the correct fix was discarded, and the mission failed with
// the work never attempted.
//
// Replacing the check costs little: DerivedChecks already adds
// file_exists for every files_hint path, so the subtask stays verified
// against what it declared it would produce. A check that names a path
// keeps that path; one that names nothing degrades to none and leans on
// the derived checks and the final verify.
//
// Returns a warning describing the substitution, so a human reading the
// plan sees the check they are actually getting.
func repairUnrunnableCheck(s *Subtask) string {
	if s.Check.Type != "shell" {
		return ""
	}
	cmd := strings.TrimSpace(strings.ToLower(s.Check.Cmd))
	if cmd == "" {
		return "" // validateCheck reports an empty cmd; not this rule's business
	}
	reason := missingCommand(cmd)
	if reason == "" {
		return ""
	}
	old := s.Check.Cmd
	if path := firstCheckablePath(s); path != "" {
		s.Check = Check{Type: "file_exists", Path: path}
		return fmt.Sprintf("%s: shell check %q %s - replaced with file_exists on %s",
			s.ID, old, reason, path)
	}
	s.Check = Check{Type: "none"}
	return fmt.Sprintf("%s: shell check %q %s - dropped; the subtask now rests on "+
		"its derived checks and the final verification", s.ID, old, reason)
}

// firstCheckablePath picks the path a downgraded check should watch: the
// one the check already named if it named one, else the subtask's first
// declared output.
func firstCheckablePath(s *Subtask) string {
	if p := strings.TrimSpace(s.Check.Path); p != "" {
		return p
	}
	for _, p := range s.FilesHint {
		if p = strings.TrimSpace(p); p != "" {
			return p
		}
	}
	return ""
}
