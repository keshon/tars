package mission

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keshon/tars/internal/prompts"
	"github.com/keshon/tars/internal/workspace"
)

// Context budget knobs. Sizes are characters (≈ tokens*4); the whole
// compiled seed should stay around 4k tokens so a worker starts with
// most of the window genuinely free — the exact opposite of a long
// direct-mode run, where step 40 starts with almost nothing.
const (
	maxListingEntries = 200
	maxListingChars   = 6 * 1024
	maxSummaryChars   = 400
)

// skipDirs are never listed and never offered to the planner: agent
// bookkeeping, VCS internals, dependency trees. A weak model that sees
// node_modules in its file listing will read node_modules.
var skipDirs = map[string]bool{
	".tars": true, ".git": true, "node_modules": true,
	".idea": true, ".vscode": true, "__pycache__": true,
}

// WorkspaceListing walks the workspace mechanically (no model involved)
// and returns a rendered file listing plus the set of real relative
// paths — the listing seeds planner/worker prompts, the set backs the
// validator's hallucinated-path defense. Bounded on both entry count
// and characters, with an honest truncation marker.
func WorkspaceListing(ws *workspace.Workspace) (string, map[string]bool) {
	existing := make(map[string]bool)
	var entries []string
	truncated := false

	filepath.WalkDir(ws.Root(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if path == ws.Root() {
			return nil
		}
		rel, err := filepath.Rel(ws.Root(), path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		existing[rel] = true
		if len(entries) >= maxListingEntries {
			truncated = true
			return nil // keep filling the existing-set, stop rendering
		}
		entries = append(entries, rel)
		return nil
	})

	sort.Strings(entries)
	listing := strings.Join(entries, "\n")
	if len(listing) > maxListingChars {
		listing = listing[:maxListingChars]
		if cut := strings.LastIndexByte(listing, '\n'); cut > 0 {
			listing = listing[:cut]
		}
		truncated = true
	}
	if listing == "" {
		listing = "(workspace is empty)"
	}
	if truncated {
		listing += "\n…(listing truncated)"
	}
	return listing, existing
}

// CompileSeed builds a subtask worker's entire first user message from
// the ledger — this, not any previous transcript, is how continuity
// crosses worker boundaries. The mission task and the acceptance
// criteria are carried verbatim; everything about completed work is a
// harness-recorded fact.
func CompileSeed(m *Mission, sub *Subtask, listing string) string {
	acceptance := "- (none declared)"
	if len(sub.Acceptance) > 0 {
		acceptance = "- " + strings.Join(sub.Acceptance, "\n- ")
	}

	// Union of the planner's guess and what the mission actually touched
	// so far — the second half is what lets worker N find the real files
	// worker N-1 created regardless of what the plan predicted.
	files := unionPaths(sub.FilesHint, m.Mutated)
	filesLine := "(none listed — inspect the workspace)"
	if len(files) > 0 {
		filesLine = strings.Join(files, ", ")
	}

	return withSkillHint(m.Task, fmt.Sprintf(prompts.MissionSeed,
		m.Task,
		m.RenderLedger(),
		sub.ID, sub.Title,
		strings.TrimSpace(sub.Goal),
		acceptance,
		filesLine,
		sub.Check.Render(),
		listing,
	))
}

// CompileFixSeed builds a fix worker's first user message after a check
// failure. The difference from a normal seed is all evidence: the check
// that failed with its real output (measured, not paraphrased), and the
// files previous attempts actually touched — "the bug is almost certainly
// in one of these". checkOutput arrives already truncated by the caller.
func CompileFixSeed(m *Mission, sub *Subtask, checkOutput, listing string) string {
	acceptance := "- (none declared)"
	if len(sub.Acceptance) > 0 {
		acceptance = "- " + strings.Join(sub.Acceptance, "\n- ")
	}
	files := unionPaths(sub.FilesHint, m.Mutated)
	filesLine := "(none recorded — inspect the workspace)"
	if len(files) > 0 {
		filesLine = strings.Join(files, ", ")
	}
	if strings.TrimSpace(checkOutput) == "" {
		checkOutput = "(no output)"
	}

	return withSkillHint(m.Task, fmt.Sprintf(prompts.MissionFixSeed,
		m.Task,
		m.RenderLedger(),
		sub.ID, sub.Title,
		strings.TrimSpace(sub.Goal),
		acceptance,
		sub.Check.Render(),
		checkOutput,
		filesLine,
		listing,
	))
}

func withSkillHint(task, seed string) string {
	if h := SkillHint(task); h != "" {
		return seed + "\n\n" + h
	}
	return seed
}

func unionPaths(a, b []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range [][]string{a, b} {
		for _, p := range list {
			p = filepath.ToSlash(p)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
