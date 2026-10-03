package mission

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/keshon/tars/internal/workspace"
)

// checkShellTimeout bounds a shell check the same way tools.RunShell
// bounds the model's own commands — a check that hangs is a failed
// check, not a stuck mission.
const checkShellTimeout = 120 * time.Second

// RunCheck executes a subtask's declared check mechanically and returns
// what it measured. ok is the verdict; output is the evidence (fed to
// fix workers verbatim, so it should carry real command output, not a
// paraphrase). A check the harness can't even attempt (bad type, path
// escaping the workspace) is a failure with the reason as output — never
// a silent pass.
// ValidateCheck rejects probe checks the runner could never satisfy:
// unknown types and missing required fields. loadProbes runs it dry so
// a typo fails before spending model calls, not mid-run after them.
// Every rule here mirrors a branch of RunCheck below; add both together.
func ValidateCheck(c Check) error {
	switch c.Type {
	case "", "none":
		return nil
	case "file_exists", "file_absent":
		if strings.TrimSpace(c.Path) == "" {
			return fmt.Errorf("type %q needs path", c.Type)
		}
		return nil
	case "content_contains":
		if strings.TrimSpace(c.Path) == "" {
			return fmt.Errorf("type %q needs path", c.Type)
		}
		if strings.TrimSpace(c.Contains) == "" {
			return fmt.Errorf("type %q needs non-blank contains (blank needles fail at runtime)", c.Type)
		}
		return nil
	case "shell":
		if strings.TrimSpace(c.Cmd) == "" {
			return fmt.Errorf("type %q needs cmd", c.Type)
		}
		return nil
	case "http":
		if strings.TrimSpace(c.URL) == "" {
			return fmt.Errorf("type %q needs url", c.Type)
		}
		return nil
	default:
		return fmt.Errorf("unknown check type %q", c.Type)
	}
}

func RunCheck(ctx context.Context, c Check, ws *workspace.Workspace) (output string, ok bool) {
	switch c.Type {
	case "", "none":
		return "(no check declared)", true

	case "file_exists":
		full, err := ws.Resolve(c.Path)
		if err != nil {
			return fmt.Sprintf("check path rejected: %v", err), false
		}
		info, err := os.Stat(full)
		if err != nil {
			return fmt.Sprintf("file %s does not exist", c.Path), false
		}
		if info.IsDir() {
			return fmt.Sprintf("%s exists but is a directory, not a file", c.Path), false
		}
		if info.Size() == 0 {
			// A weak model "completing" a subtask by writing an empty file
			// is a known failure shape — existence alone proves nothing.
			return fmt.Sprintf("file %s exists but is empty (0 bytes)", c.Path), false
		}
		return fmt.Sprintf("file %s exists (%d bytes)", c.Path, info.Size()), true

	case "file_absent":
		// The mirror of file_exists for deletions and renames: proves the
		// old path is gone rather than shelling out to `if exist` (cmd)
		// or `[ ! -f ]` (sh), neither of which is portable.
		full, err := ws.Resolve(c.Path)
		if err != nil {
			return fmt.Sprintf("check path rejected: %v", err), false
		}
		if _, err := os.Stat(full); err == nil {
			return fmt.Sprintf("file %s still exists", c.Path), false
		}
		return fmt.Sprintf("file %s is absent", c.Path), true

	case "content_contains":
		// Stronger than file_exists: proves a distinctive symbol from the
		// acceptance criteria actually landed. Caps the read so a huge
		// file can't blow a fix-worker prompt.
		needle := strings.TrimSpace(strings.Trim(c.Contains, `"'`))
		if needle == "" {
			return "content_contains check has empty contains", false
		}
		full, err := ws.Resolve(c.Path)
		if err != nil {
			return fmt.Sprintf("check path rejected: %v", err), false
		}
		const maxRead = 256 * 1024
		data, err := os.ReadFile(full)
		if err != nil {
			return fmt.Sprintf("cannot read %s: %v", c.Path, err), false
		}
		body := data
		if len(body) > maxRead {
			body = body[:maxRead]
		}
		if !strings.Contains(string(body), needle) {
			return fmt.Sprintf("file %s (%d bytes) does not contain %q", c.Path, len(data), needle), false
		}
		return fmt.Sprintf("file %s contains %q", c.Path, needle), true

	case "shell":
		out, err := RunShellCommand(ctx, c.Cmd, ws.Root())
		if err != nil {
			return fmt.Sprintf("command failed: %v\n%s", err, out), false
		}
		if out == "" {
			out = "(no output)"
		}
		return out, true

	case "http":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
		if err != nil {
			return fmt.Sprintf("bad check URL: %v", err), false
		}
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Sprintf("GET %s failed: %v", c.URL, err), false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Sprintf("GET %s returned %s, want 200", c.URL, resp.Status), false
		}
		return fmt.Sprintf("GET %s returned 200", c.URL), true

	default:
		return fmt.Sprintf("unknown check type %q", c.Type), false
	}
}

// RunShellCommand runs one command line in dir with the same OS-aware
// shell choice as tools.RunShell (cmd.exe on Windows, sh elsewhere) and
// returns combined output. Shared by shell checks and main.go's
// -verify-cmd hook so the two can never drift apart.
func RunShellCommand(ctx context.Context, command, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, checkShellTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// DerivedChecks returns checks the harness derives from a subtask's own
// plan, independent of the check the model chose for itself.
//
// A subtask declares up to five acceptance criteria and exactly one
// mechanical check, and the model picks which one. It picks an easy one:
// a plan whose acceptance demanded package.json, vite.config.ts AND
// src/main.ts declared a check that tested package.json for the string
// "vite", and the subtask went green with src/main.ts never written.
//
// files_hint is the structured statement of what the subtask will
// produce, so every path in it is checked whatever the model chose to
// verify. Acceptance prose is deliberately not parsed: guessing paths out
// of a sentence produces failures nobody can act on.
//
// A path the declared check already names is skipped — a weaker
// file_exists beside a content_contains on the same file adds nothing.
func DerivedChecks(sub *Subtask) []Check {
	seen := map[string]bool{}
	if p := normalizePlanPath(sub.Check.Path); p != "" {
		seen[p] = true
	}
	var out []Check
	for _, hint := range sub.FilesHint {
		p := normalizePlanPath(hint)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, Check{Type: "file_exists", Path: p})
	}
	return out
}

// RunDerivedChecks reports the first derived check that fails, or ok when
// the subtask produced everything its plan said it would.
func RunDerivedChecks(ctx context.Context, sub *Subtask, ws *workspace.Workspace) (output string, ok bool) {
	for _, c := range DerivedChecks(sub) {
		if out, passed := RunCheck(ctx, c, ws); !passed {
			return fmt.Sprintf("%s (files_hint promised this subtask would produce it, "+
				"and the declared check does not cover it)", out), false
		}
	}
	return "", true
}
