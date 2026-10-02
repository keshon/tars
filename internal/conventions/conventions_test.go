package conventions

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// checks maps an [enforced: name] tag in docs/conventions.md to the function
// that enforces it. TestDocumentAndChecksAgree fails if this map and the
// document's tags ever disagree.
var checks = map[string]func(*testing.T, *repo){
	"package-layers":      checkPackageLayers,
	"agent-construction":  checkAgentConstruction,
	"prompt-registration": checkPromptRegistration,
	"probe-prompts":       checkProbePrompts,
	"doc-identifiers":     checkDocIdentifiers,
	"comment-dates":       checkCommentDates,
	"cgo-free":            checkCgoFree,
	"skill-paths":         checkSkillPaths,
	"structured-output":   checkStructuredOutput,
}

func TestConventions(t *testing.T) {
	r := load(t)
	for _, name := range sortedKeys(checks) {
		t.Run(name, func(t *testing.T) { checks[name](t, r) })
	}
}

// selfEnforced are tags this file satisfies by existing rather than through
// an entry in checks — the agreement rule is what TestDocumentAndChecksAgree
// itself does.
var selfEnforced = map[string]bool{"doc-and-checks-agree": true}

// TestDocumentAndChecksAgree stops the document advertising enforcement that
// does not run, and a check enforcing something the document never states.
func TestDocumentAndChecksAgree(t *testing.T) {
	r := load(t)
	for _, tag := range sortedKeys(r.rules) {
		if selfEnforced[tag] {
			continue
		}
		if _, ok := checks[tag]; !ok {
			t.Errorf("docs/conventions.md claims [enforced: %s] but no check is registered", tag)
		}
	}
	for _, name := range sortedKeys(checks) {
		if _, ok := r.rules[name]; !ok {
			t.Errorf("check %q runs but docs/conventions.md never states the rule", name)
		}
	}
}

// --- rules ---

// checkPackageLayers keeps the import graph acyclic by construction rather
// than by noticing a cycle once the compiler refuses to build.
func checkPackageLayers(t *testing.T, r *repo) {
	allowed := map[string][]string{
		"llm":        {},
		"prompts":    {},
		"workspace":  {},
		"permission": {},
		"session":    {"llm"},
		"agent":      {"llm", "prompts", "permission", "session"},
	}
	for pkg, permitted := range allowed {
		for _, f := range r.goFiles {
			if pkgOf(r, f) != pkg || strings.HasSuffix(f, "_test.go") {
				continue
			}
			for _, imp := range internalImports(r.read(t, f)) {
				if !contains(permitted, imp) {
					t.Errorf("%s imports internal/%s\n%s",
						r.rel(f), imp, r.rule("package-layers"))
				}
			}
		}
	}
}

// checkAgentConstruction is the whole point of internal/roles: one answer to
// "what is this kind of agent allowed to do", not one per call site.
func checkAgentConstruction(t *testing.T, r *repo) {
	for _, f := range r.goFiles {
		if strings.HasSuffix(f, "_test.go") || strings.Contains(r.rel(f), "internal/roles") {
			continue
		}
		if strings.Contains(r.read(t, f), "agent.New(") {
			t.Errorf("%s calls agent.New directly\n%s",
				r.rel(f), r.rule("agent-construction"))
		}
	}
}

func checkPromptRegistration(t *testing.T, r *repo) {
	registry := r.readPath(t, "internal/prompts/prompts.go")
	entries, err := os.ReadDir(filepath.Join(r.root, "internal", "prompts", "text"))
	if err != nil {
		t.Fatalf("read prompt directory: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		if !strings.Contains(registry, e.Name()) {
			t.Errorf("internal/prompts/text/%s is never referenced by prompts.go\n%s",
				e.Name(), r.rule("prompt-registration"))
		}
	}
}

func checkProbePrompts(t *testing.T, r *repo) {
	dir := filepath.Join(r.root, "eval", "probes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read probe directory: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var probe struct {
			Prompt string `json:"prompt"`
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			t.Errorf("eval/probes/%s is not valid JSON: %v", e.Name(), err)
			continue
		}
		if probe.Prompt == "" {
			t.Errorf("eval/probes/%s names no prompt file\n%s",
				e.Name(), r.rule("probe-prompts"))
			continue
		}
		if _, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(probe.Prompt))); err != nil {
			t.Errorf("eval/probes/%s points at %s, which does not exist\n%s",
				e.Name(), probe.Prompt, r.rule("probe-prompts"))
		}
	}
}

// docIdentifier matches a backticked token that is unambiguously a Go
// identifier: camelCase, PascalCase, or pkg.Identifier. Anything else in
// backticks — a command, a flag, a path — is deliberately not checked, since
// guessing at those produces failures nobody can act on.
var docIdentifier = regexp.MustCompile("`([a-z][a-z0-9]*\\.[A-Z][A-Za-z0-9]*|[A-Za-z][A-Za-z0-9]*[A-Z][A-Za-z0-9]*)`")

func checkDocIdentifiers(t *testing.T, r *repo) {
	var code strings.Builder
	for _, f := range r.goFiles {
		code.WriteString(r.read(t, f))
	}
	haystack := code.String()

	for _, doc := range []string{"ARCHITECTURE.md", "README.md", "docs/conventions.md"} {
		for _, m := range docIdentifier.FindAllStringSubmatch(r.readPath(t, doc), -1) {
			name := m[1]
			if i := strings.LastIndex(name, "."); i >= 0 {
				name = name[i+1:] // agent.New -> New
			}
			if !strings.Contains(haystack, name) {
				t.Errorf("%s names `%s`, which does not exist in the code\n%s",
					doc, m[1], r.rule("doc-identifiers"))
			}
		}
	}
}

var datedComment = regexp.MustCompile(`^\s*//.*\b20\d\d-\d\d-\d\d\b`)

// checkCommentDates ratchets: what the codebase already owes is recorded in
// baseline.json and may only shrink. A cleanup that removes the last one
// should also drop the entry, which the test says when it happens.
func checkCommentDates(t *testing.T, r *repo) {
	base := loadBaseline(t, r)
	found := map[string]int{}
	for _, f := range r.goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		n := 0
		for _, line := range strings.Split(r.read(t, f), "\n") {
			if datedComment.MatchString(line) {
				n++
			}
		}
		if n > 0 {
			found[r.rel(f)] = n
		}
	}

	for file, n := range found {
		allowed := base["comment-dates"][file]
		if n > allowed {
			t.Errorf("%s has %d dated comment(s), baseline allows %d\n%s",
				file, n, allowed, r.rule("comment-dates"))
		}
	}
	for file, allowed := range base["comment-dates"] {
		if found[file] < allowed {
			t.Errorf("%s now has %d dated comment(s), below its baseline of %d — "+
				"lower or remove the entry in internal/conventions/baseline.json",
				file, found[file], allowed)
		}
	}
}

// --- helpers ---

type repo struct {
	root    string
	doc     string
	rules   map[string]string
	goFiles []string
	cache   map[string]string
}

func load(t *testing.T) *repo {
	t.Helper()
	root := repoRoot(t)
	r := &repo{root: root, cache: map[string]string{}}
	r.doc = r.readPath(t, "docs/conventions.md")
	r.rules = parseRules(r.doc)
	r.goFiles = goFiles(t, root)
	return r
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the test directory")
		}
		dir = parent
	}
}

var enforcedTag = regexp.MustCompile(`\*\*\[enforced: ([a-z-]+)\]\*\*`)

// parseRules maps each tag to the paragraph stating it, so a failure quotes
// the document rather than a copy of it that can drift.
func parseRules(doc string) map[string]string {
	out := map[string]string{}
	for _, para := range strings.Split(doc, "\n\n") {
		if m := enforcedTag.FindStringSubmatch(para); m != nil {
			out[m[1]] = strings.TrimSpace(para)
		}
	}
	return out
}

func (r *repo) rule(tag string) string {
	text, ok := r.rules[tag]
	if !ok {
		return fmt.Sprintf("(docs/conventions.md states no rule for %q)", tag)
	}
	return "\ndocs/conventions.md says:\n" + indent(text)
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

func (r *repo) rel(path string) string {
	rel, err := filepath.Rel(r.root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func (r *repo) read(t *testing.T, path string) string {
	t.Helper()
	if s, ok := r.cache[path]; ok {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Normalized to LF because git hands Windows checkouts CRLF, and every
	// rule here reasons about text. parseRules splits paragraphs on a
	// blank line, which never occurs as "\n\n" in a CRLF file: the whole
	// document parsed as one blob, one rule was found, and the other eight
	// reported themselves unstated. It failed loudly here and would fail
	// the same way on any fresh clone on this OS.
	r.cache[path] = strings.ReplaceAll(string(data), "\r\n", "\n")
	return r.cache[path]
}

func (r *repo) readPath(t *testing.T, rel string) string {
	t.Helper()
	return r.read(t, filepath.Join(r.root, filepath.FromSlash(rel)))
}

// goFiles lists the repo's Go sources, skipping directories that hold
// generated output or an agent's scratch space rather than source.
func goFiles(t *testing.T, root string) []string {
	t.Helper()
	skip := map[string]bool{".git": true, ".agent": true, "sandbox": true, "results": true}
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	sort.Strings(out)
	return out
}

func pkgOf(r *repo, path string) string {
	rel := r.rel(path)
	const prefix = "internal/"
	if !strings.HasPrefix(rel, prefix) {
		return ""
	}
	rest := rel[len(prefix):]
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return ""
}

var importLine = regexp.MustCompile(`"github\.com/keshon/tars/internal/([a-z]+)"`)

func internalImports(src string) []string {
	var out []string
	for _, m := range importLine.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	return out
}

func loadBaseline(t *testing.T, r *repo) map[string]map[string]int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.root, "internal", "conventions", "baseline.json"))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	var out map[string]map[string]int
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse baseline: %v", err)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cgoImport matches the pseudo-package cgo uses, in either import form.
var cgoImport = regexp.MustCompile(`(?m)^\s*(?:import\s+)?_?\s*"C"\s*$`)

// checkCgoFree keeps `go build` a single step on every platform this can
// plausibly run on.
func checkCgoFree(t *testing.T, r *repo) {
	for _, f := range r.goFiles {
		if cgoImport.MatchString(r.read(t, f)) {
			t.Errorf("%s imports C%s", r.rel(f), r.rule("cgo-free"))
		}
	}
}

// skillRef matches a skills/<name>/SKILL.md path written in a prompt.
var skillRef = regexp.MustCompile(`skills/[A-Za-z0-9_-]+/SKILL\.md`)

// checkSkillPaths keeps prompts honest about files the agent may not have.
func checkSkillPaths(t *testing.T, r *repo) {
	dir := filepath.Join(r.root, "internal", "prompts", "text")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read prompt directory: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		body := r.read(t, filepath.Join(dir, e.Name()))
		for _, ref := range skillRef.FindAllString(body, -1) {
			if _, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(ref))); err != nil {
				t.Errorf("internal/prompts/text/%s names %s, which does not exist%s",
					e.Name(), ref, r.rule("skill-paths"))
			}
			for _, line := range strings.Split(body, "\n") {
				if strings.Contains(line, ref) && !strings.Contains(line, "if it exists") {
					t.Errorf("internal/prompts/text/%s names %s without saying \"if it exists\"%s",
						e.Name(), ref, r.rule("skill-paths"))
				}
			}
		}
	}
}

// checkStructuredOutput requires both encodings on any constrained request.
//
// Found the expensive way: nine mission runs on llama-server planned with no
// constraint at all, because the call sent a GBNF to an endpoint that takes
// response_format. The backend answered 200 and the failure surfaced as
// "output is not valid JSON" three layers away.
func checkStructuredOutput(t *testing.T, r *repo) {
	for _, f := range r.goFiles {
		if strings.HasSuffix(f, "_test.go") ||
			filepath.Base(filepath.Dir(f)) == "llm" {
			continue // the llm package defines both fields and tests them directly
		}
		body := r.read(t, f)
		for _, lit := range chatRequestLiterals(body) {
			gbnf := strings.Contains(lit, "Grammar:")
			schema := strings.Contains(lit, "JSONSchema:")
			if gbnf == schema {
				continue
			}
			has, missing := "Grammar", "JSONSchema"
			if schema {
				has, missing = missing, has
			}
			t.Errorf("%s: a ChatRequest sets %s without %s%s",
				r.rel(f), has, missing, r.rule("structured-output"))
		}
	}
}

// chatRequestLiterals returns the body of each ChatRequest composite literal,
// matched by brace depth so a nested struct field does not end it early.
func chatRequestLiterals(body string) []string {
	var out []string
	for _, marker := range []string{"ChatRequest{", "ChatRequest {"} {
		for i := 0; ; {
			j := strings.Index(body[i:], marker)
			if j < 0 {
				break
			}
			start := i + j + len(marker)
			depth, end := 1, -1
			for k := start; k < len(body) && end < 0; k++ {
				switch body[k] {
				case '{':
					depth++
				case '}':
					if depth--; depth == 0 {
						end = k
					}
				}
			}
			if end < 0 {
				break
			}
			out = append(out, body[start:end])
			i = end
		}
	}
	return out
}
