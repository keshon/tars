// Package checks runs deterministic post-write checks: gofmt and a
// curated secret scan over file content the agent just wrote. No LLM,
// no network, stdlib only. Findings are report-only data; the loop
// decides what to do with them (report, never repair as a side
// effect). The two-tier shape (cheap per-edit here, deep session-end
// pass later) plus dedupe cache and display clamp come straight from
// the impeccable hook playbook.
package checks

import (
	"bytes"
	"go/format"
	"go/scanner"
	"regexp"
	"strconv"
	"strings"
)

// Finding is one deterministic check result.
type Finding struct {
	Rule    string
	Path    string
	Line    int
	Summary string
}

// Gofmt reports when Go source is not gofmt-clean (or does not parse).
// Line points at the first differing line, or the syntax error.
func Gofmt(path string, src []byte) []Finding {
	formatted, err := format.Source(src)
	if err != nil {
		line := 0
		msg := "does not parse"
		if list, ok := err.(scanner.ErrorList); ok && len(list) > 0 {
			line = list[0].Pos.Line
			msg = "does not parse: " + list[0].Msg
		}
		return []Finding{{Rule: "gofmt-syntax", Path: path, Line: line, Summary: msg}}
	}
	if bytes.Equal(formatted, src) {
		return nil
	}
	return []Finding{{Rule: "gofmt", Path: path, Line: firstDiffLine(src, formatted), Summary: "not gofmt-clean"}}
}

// firstDiffLine returns the 1-based first differing line of a and b.
func firstDiffLine(a, b []byte) int {
	as := bytes.Split(a, []byte{'\n'})
	bs := bytes.Split(b, []byte{'\n'})
	for i := 0; i < len(as) && i < len(bs); i++ {
		if !bytes.Equal(as[i], bs[i]) {
			return i + 1
		}
	}
	return min(len(as), len(bs)) + 1
}

// secretPatterns is a deliberately short, low-false-positive list:
// well-known prefixes, not generic key=value shapes (those flag prose).
// Curated, not exhaustive — a tripwire, not a vault audit.
var secretPatterns = []struct {
	rule string
	re   *regexp.Regexp
}{
	{"secret-aws", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"secret-github", regexp.MustCompile(`(?:github_pat_|ghp_)[A-Za-z0-9_]{10,}`)},
	{"secret-private-key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY(?: BLOCK)?-----`)},
	{"secret-slack", regexp.MustCompile(`xox[bpas]-[A-Za-z0-9-]+`)},
}

// Secrets scans text lines for known secret prefixes, with line numbers.
func Secrets(path string, src []byte) []Finding {
	var out []Finding
	for i, line := range strings.Split(string(src), "\n") {
		for _, p := range secretPatterns {
			if p.re.MatchString(line) {
				out = append(out, Finding{Rule: p.rule, Path: path, Line: i + 1, Summary: "possible secret (" + p.rule + ")"})
			}
		}
	}
	return out
}

// ScanFile runs the checks that apply to a path: gofmt for Go source,
// secrets for everything.
func ScanFile(path string, src []byte) []Finding {
	var out []Finding
	if strings.HasSuffix(path, ".go") {
		out = append(out, Gofmt(path, src)...)
	}
	return append(out, Secrets(path, src)...)
}

// Cache dedupes findings across repeated scans: key is rule + path +
// line, so the same finding reported twice surfaces once. Per-run state;
// the loop owns its lifetime (session-end pass shares it with per-edit
// scans, which is what makes the deep pass quiet on known findings).
type Cache struct {
	seen map[string]struct{}
}

// Filter drops already-seen findings, remembering the new ones.
func (c *Cache) Filter(in []Finding) []Finding {
	if c.seen == nil {
		c.seen = map[string]struct{}{}
	}
	var out []Finding
	for _, f := range in {
		k := f.Rule + "\x00" + f.Path + "\x00" + strconv.Itoa(f.Line)
		if _, ok := c.seen[k]; ok {
			continue
		}
		c.seen[k] = struct{}{}
		out = append(out, f)
	}
	return out
}

// Clamp caps findings for display: at most max, plus the dropped count
// so the footer can say "and N more" instead of silently losing them.
func Clamp(in []Finding, max int) (out []Finding, dropped int) {
	if max < 0 {
		max = 0
	}
	if len(in) <= max {
		return in, 0
	}
	return in[:max], len(in) - max
}
