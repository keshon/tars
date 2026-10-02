// Package prompts holds every piece of text the agent sends to the model
// that isn't data (task descriptions, tool results). Kept as plain .txt
// files under text/, embedded at build time, instead of Go string
// constants — the reason is git diffs, not aesthetics: a one-sentence
// wording change shows as a one-line diff in a .txt file, not buried in
// a multi-line Go string-concatenation expression.
//
// Anything with a %s/%d placeholder is exposed as a raw format string for
// fmt.Sprintf, not pre-parsed — keeps this package boring (just an
// embed.FS reader), no templating engine for something this simple.
package prompts

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed text/*.txt
var files embed.FS

func read(name string) string {
	data, err := files.ReadFile("text/" + name)
	if err != nil {
		// All these files are embedded at compile time from a fixed list
		// below — a missing file is a build-time mistake, not a runtime
		// condition any caller can meaningfully recover from.
		panic("prompts: missing embedded file " + name + ": " + err.Error())
	}
	return strings.TrimRight(string(data), "\n")
}

var (
	// System is the main system prompt.
	System = read("system.txt")

	// RoleAddendum is appended to System for a delegated subagent given a
	// role. One %s: the role text.
	RoleAddendum = read("role_addendum.txt")

	// Resume is appended when continuing an interrupted run via -resume.
	Resume = read("resume.txt")

	// Verify is the self-check checkpoint message before a run finishes.
	Verify = read("verify.txt")

	// VerifyZeroWrites is appended to Verify when no mutating tool call
	// has succeeded yet this run. One %s: the joined list of mutating
	// tool names.
	VerifyZeroWrites = read("verify_zero_writes.txt")

	// VerifyCheckResult is appended to Verify with real build/test
	// output, when Config.Verify is set. One %s: the output.
	VerifyCheckResult = read("verify_check_result.txt")

	// Truncated fires when a response ended with finish_reason "length"
	// and no tool calls — the backend cut the model off mid-generation
	// and whatever it was building (usually a tool call) was discarded.
	Truncated = read("truncated.txt")

	// LeakDetected fires when a response contains leaked native
	// tool-call template text instead of a real structured call.
	LeakDetected = read("leak_detected.txt")

	// LeakRepeated fires when LeakDetected has already fired
	// MaxStuckSteps times in a row.
	LeakRepeated = read("leak_repeated.txt")

	// StuckFailing fires when every tool call has errored for
	// MaxStuckSteps steps in a row.
	StuckFailing = read("stuck_failing.txt")

	// StuckRepeating fires when the same call signature repeats for
	// MaxStuckSteps steps in a row, regardless of success/failure.
	StuckRepeating = read("stuck_repeating.txt")

	// StuckEscalated replaces StuckFailing/StuckRepeating the second time
	// a run gets stuck. Sending the same words again to a model that
	// already ignored them once is context spent teaching it that the
	// message means nothing; escalation has to change kind, not volume.
	StuckEscalated = read("stuck_escalated.txt")

	// BudgetWarning fires once when context usage crosses 90%. Three
	// placeholders: tokens used, limit, percent.
	BudgetWarning = read("budget_warning.txt")

	// BudgetNotice fires once when context usage crosses 75%. Three
	// placeholders: tokens used, limit, percent.
	BudgetNotice = read("budget_notice.txt")

	// OverflowRecovered fires when a chat call failed with a
	// context-window-exceeded error and history was compacted to recover.
	OverflowRecovered = read("overflow_recovered.txt")

	// SearchFatigue fires once when many consecutive steps pass without
	// any mutating tool call succeeding — the model is exploring/
	// searching but not converging. Nudges it to broaden its approach or
	// stop and ask, instead of indefinitely narrowing the same dead end.
	SearchFatigue = read("search_fatigue.txt")

	// ToolLoop fires once when the same read-only tool is called alone for
	// several consecutive steps with only small argument changes. Steps
	// that mutate the workspace never count toward it — writing several
	// different files in a row is progress, not a loop.
	ToolLoop = read("tool_loop.txt")

	// AnnouncedNotWritten fires when the model tries to end a run that was
	// supposed to write files having written none — the "let me now write
	// the file" finish. Two %s: the model's own closing line, and the
	// names of the mutating tools.
	AnnouncedNotWritten = read("announced_not_written.txt")

	// CompactNotice is inserted once when history is mechanically
	// compacted at 90% context usage.
	CompactNotice = read("compact_notice.txt")

	// VerifyFailedContinue blocks a premature finish when the verify hook
	// reported FAILED. One %s: the verify output.
	VerifyFailedContinue = read("verify_failed_continue.txt")

	// MissionPlan is the system prompt for the mission planning call —
	// tool-free, grammar-constrained JSON output.
	MissionPlan = read("mission_plan.txt")

	// MissionPlanTask is the user message for the planning call. Two %s:
	// the verbatim task, the workspace file listing.
	MissionPlanTask = read("mission_plan_task.txt")

	// MissionPlanRetry asks for a corrected plan after Go-side validation
	// rejected the first one. One %s: the joined validation errors.
	MissionPlanRetry = read("mission_plan_retry.txt")

	// MissionPlanEdit asks for a corrected plan after the human approval
	// gate rejected it with a note. One %s: the note.
	MissionPlanEdit = read("mission_plan_edit.txt")

	// MissionWorker is the system prompt for a mission subtask worker —
	// the baseline tool rules plus stay-in-scope discipline.
	MissionWorker = read("mission_worker.txt")

	// MissionSeed is the compiled first user message for a subtask
	// worker. Nine %s, in order: mission task, rendered ledger, subtask
	// id, subtask title, goal, acceptance bullets, files involved,
	// rendered check, workspace file listing.
	MissionSeed = read("mission_seed.txt")

	// MissionFixSeed is the compiled first user message for a fix worker
	// after a check failure. Ten %s, in order: mission task, rendered
	// ledger, subtask id, title, goal, acceptance bullets, rendered
	// check, the check's actual output, files touched by prior attempts,
	// workspace file listing.
	MissionFixSeed = read("mission_fix_seed.txt")

	// MissionReplan is the user message for a replan call after a
	// subtask exhausted its fix attempts (or final verify regressed).
	// Four %s: task, execution record, reason, workspace file listing.
	MissionReplan = read("mission_replan.txt")

	// MissionMapAnnotate is the system prompt for the read-only
	// exploration worker that annotates the mechanical codebase map.
	MissionMapAnnotate = read("mission_map_annotate.txt")

	// MissionMapAnnotateTask is its user message. Two %s: the mission
	// task, the mechanical map.
	MissionMapAnnotateTask = read("mission_map_annotate_task.txt")

	// MissionReview is the system prompt for the read-only review worker
	// that inspects a finished mission for whole-task gaps.
	MissionReview = read("mission_review.txt")

	// MissionReviewTask is its user message. Three %s: task, rendered
	// plan, rendered execution record.
	MissionReviewTask = read("mission_review_task.txt")

	// MissionVerdict narrows a reviewer's free-text report to an
	// ok/gaps decision (sent with the decision grammar). One %s: the
	// report.
	MissionVerdict = read("mission_verdict.txt")

	// KoboldAddendum tightens tool-call discipline for small local models.
	KoboldAddendum = read("kobold_addendum.txt")

	// OpenAIAddendum tightens schema discipline for hosted models.
	OpenAIAddendum = read("openai_addendum.txt")

	// PlanMode is the system prompt when -plan is set: read-only tools
	// only, so "don't edit" is enforced by the tool set, not by asking.
	PlanMode = read("plan_mode.txt")
)

// WithRole returns System with RoleAddendum appended for role, or System
// unchanged if role is empty.
func WithRole(role string) string {
	if role == "" {
		return System
	}
	return System + "\n\n" + fmt.Sprintf(RoleAddendum, role)
}

// SystemForBackend returns System with the backend-specific addendum for
// kind ("kobold", "llama", "openai"). Unknown kinds return System unchanged.
func SystemForBackend(kind string) string {
	switch kind {
	case "kobold":
		return System + "\n\n" + KoboldAddendum
	case "openai":
		return System + "\n\n" + OpenAIAddendum
	default:
		return System
	}
}
