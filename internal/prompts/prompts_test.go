package prompts

import (
	"runtime"
	"strings"
	"testing"
)

func TestAllPromptsLoadNonEmpty(t *testing.T) {
	all := map[string]string{
		"System":                 System,
		"RoleAddendum":           RoleAddendum,
		"Resume":                 Resume,
		"Verify":                 Verify,
		"VerifyZeroWrites":       VerifyZeroWrites,
		"VerifyCheckResult":      VerifyCheckResult,
		"LeakDetected":           LeakDetected,
		"LeakRepeated":           LeakRepeated,
		"StuckFailing":           StuckFailing,
		"StuckRepeating":         StuckRepeating,
		"StuckEscalated":         StuckEscalated,
		"BudgetWarning":          BudgetWarning,
		"BudgetNotice":           BudgetNotice,
		"StepNotice":             StepNotice,
		"StepWarning":            StepWarning,
		"SearchFatigue":          SearchFatigue,
		"ToolLoop":               ToolLoop,
		"TodoOpen":               TodoOpen,
		"Closing":                Closing,
		"BudgetContinue":         BudgetContinue,
		"DelegateHint":           DelegateHint,
		"AskHint":                AskHint,
		"WriteHint":              WriteHint,
		"SubagentScope":          SubagentScope,
		"AnnouncedNotWritten":    AnnouncedNotWritten,
		"Truncated":              Truncated,
		"WrapUp":                 WrapUp,
		"CompactNotice":          CompactNotice,
		"VerifyFailedContinue":   VerifyFailedContinue,
		"MissionPlan":            MissionPlan,
		"MissionPlanTask":        MissionPlanTask,
		"MissionPlanRetry":       MissionPlanRetry,
		"MissionPlanEdit":        MissionPlanEdit,
		"MissionWorker":          MissionWorker,
		"MissionSeed":            MissionSeed,
		"MissionFixSeed":         MissionFixSeed,
		"MissionReplan":          MissionReplan,
		"MissionMapAnnotate":     MissionMapAnnotate,
		"MissionMapAnnotateTask": MissionMapAnnotateTask,
		"MissionReview":          MissionReview,
		"MissionReviewTask":      MissionReviewTask,
		"MissionVerdict":         MissionVerdict,
	}
	for name, val := range all {
		if val == "" {
			t.Errorf("%s loaded empty", name)
		}
		if strings.HasSuffix(val, "\n") {
			t.Errorf("%s has a trailing newline, want trimmed", name)
		}
	}
}

func TestMissionSeed_PlaceholderCountMatchesCompiler(t *testing.T) {
	if got := strings.Count(MissionSeed, "%s"); got != 9 {
		t.Fatalf("MissionSeed has %d %%s placeholders, want 9 — mission.CompileSeed passes exactly nine", got)
	}
	if got := strings.Count(MissionFixSeed, "%s"); got != 10 {
		t.Fatalf("MissionFixSeed has %d %%s placeholders, want 10 — mission.CompileFixSeed passes exactly ten", got)
	}
	if got := strings.Count(MissionReplan, "%s"); got != 4 {
		t.Fatalf("MissionReplan has %d %%s placeholders, want 4", got)
	}
	if got := strings.Count(MissionReviewTask, "%s"); got != 3 {
		t.Fatalf("MissionReviewTask has %d %%s placeholders, want 3", got)
	}
}

func TestWithRole_EmptyReturnsSystemUnchanged(t *testing.T) {
	if got := WithRole(""); got != System {
		t.Fatalf("WithRole(\"\") changed the prompt, want it unchanged")
	}
}

func TestWithRole_NonEmptyAppendsFormattedAddendum(t *testing.T) {
	got := WithRole("a strict reviewer")
	if !strings.HasPrefix(got, System) {
		t.Fatal("WithRole result must start with the base System prompt")
	}
	if !strings.Contains(got, "a strict reviewer") {
		t.Fatalf("role text not found in result: %q", got)
	}
	if strings.Contains(got, "%s") {
		t.Fatalf("role addendum format string was not substituted: %q", got)
	}
}

// TestSystem_KeepsLoadBearingGuards pins the sentences that shape model
// behavior: tool discipline, anti-fake-save, background policy, asking.
// Rewording deletes the test's premise, so reword deliberately —
// update the fragments, never just delete them.
func TestVerify_KeepsProgressGuard(t *testing.T) {
	if !strings.Contains(Verify, "A progress report is not a finish") {
		t.Error("verify lost its progress-report guard")
	}
}

func TestTodoOpen_NamesCheckoff(t *testing.T) {
	if !strings.Contains(TodoOpen, "check it off") {
		t.Error("todo bounce must name the checkoff action, not just more work")
	}
}

func TestPlatformLine_NamesCurrentOS(t *testing.T) {
	got := PlatformLine()
	if got == "" {
		t.Fatal("platform line must not be empty")
	}
	if !strings.Contains(strings.ToLower(got), runtime.GOOS) {
		t.Fatalf("platform line %q must name runtime.GOOS %q", got, runtime.GOOS)
	}
}

func TestSystem_KeepsLoadBearingGuards(t *testing.T) {
	for _, want := range []string{
		"does NOT save it anywhere",
		"Always use move_file",
		"always use list_files",
		"Use grep_files instead",
		"Never run a long-running process",
		"use ask_user to ask one focused clarifying question",
		"A progress report (\"done X so far\"",
		"never finish with items unchecked",
	} {
		if !strings.Contains(System, want) {
			t.Errorf("system prompt lost its guard %q", want)
		}
	}
}
