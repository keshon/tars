// Package permission decides whether a tool call may run.
//
// It is a leaf: no internal imports, so the agent loop can depend on it
// without creating a cycle. Policy is last-match-wins over wildcard
// patterns, mirroring pi/opencode: explicit deny beats broad allow.
package permission

import (
	"fmt"
	"path"
	"strings"
)

// Effect is what a matching rule says.
type Effect int

const (
	Allow Effect = iota
	Ask
	Deny
)

func (e Effect) String() string {
	switch e {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	case Deny:
		return "deny"
	default:
		return "unknown"
	}
}

// Rule pairs a tool + resource pattern with an effect.
// Tool is the tool name ("run_shell", "read_file", "*").
// Pattern matches the call's resource (command line, file path, "*" for any).
type Rule struct {
	Tool    string
	Pattern string
	Effect  Effect
}

// Policy is an ordered ruleset: later rules override earlier ones.
type Policy struct {
	Rules []Rule
}

// destructiveShell are command-line substrings that ask first.
// Tripwire, not sandbox: substring matching is obfuscatable
// (`r\m\ -\rf` sails through), which is why these Ask for operator
// judgment instead of Denying. The workspace guardrail (Resolve) and
// real containment (container/VM) are separate layers; see the
// workspace package header on what each layer promises.
var destructiveShell = []string{
	"rm -rf", "del /s", "del /q", "rd /s", "format ", "mkfs", "dd if=", ":(){",
}

// Default returns the shipping policy: permissive but sensitive reads
// ask, and destructive shell shapes ask. Everything else allows;
// restrictions opt in, and later user rules (--allow/--deny) win.
func Default() Policy {
	rules := []Rule{
		{Tool: "*", Pattern: "*", Effect: Allow},
		{Tool: "read_file", Pattern: "*.env", Effect: Ask},
		{Tool: "read_file", Pattern: "*.env.*", Effect: Ask},
		{Tool: "read_file", Pattern: "*credentials*", Effect: Ask},
		{Tool: "read_file", Pattern: "*secret*", Effect: Ask},
	}
	for _, tool := range []string{"run_shell", "start_background"} {
		for _, pat := range destructiveShell {
			rules = append(rules, Rule{Tool: tool, Pattern: pat, Effect: Ask})
		}
	}
	return Policy{Rules: rules}
}

// Evaluate returns the effect for a tool call against a resource.
// No match means Allow: the default is open, restrictions opt in.
func (p Policy) Evaluate(tool, resource string) Effect {
	eff := Allow
	for _, r := range p.Rules {
		if !toolMatch(r.Tool, tool) {
			continue
		}
		if !resourceMatch(r.Pattern, resource) {
			continue
		}
		eff = r.Effect
	}
	return eff
}

// WithExtra appends user rules (CLI --allow/--deny) so they win over defaults.
func (p Policy) WithExtra(rules []Rule) Policy {
	p.Rules = append(p.Rules, rules...)
	return p
}

func toolMatch(pattern, tool string) bool {
	if pattern == "*" {
		return true
	}
	return strings.EqualFold(pattern, tool)
}

func resourceMatch(pattern, resource string) bool {
	if pattern == "*" || pattern == "" {
		return true
	}
	// Exact or case-insensitive exact first.
	if strings.EqualFold(pattern, resource) {
		return true
	}
	// Glob on basename and full string (covers *.env).
	for _, s := range []string{resource, baseName(resource)} {
		if ok, _ := path.Match(pattern, s); ok {
			return true
		}
		// Also try case-insensitive via lowercasing both.
		if ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(s)); ok {
			return true
		}
	}
	// Substring fallback for plain tokens without wildcards.
	if !strings.ContainsAny(pattern, "*?[") {
		return strings.Contains(strings.ToLower(resource), strings.ToLower(pattern))
	}
	return false
}

func baseName(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// Decide maps an operator answer to an effect: y(es) and a(lways) allow,
// anything else denies. Shared by every front end (CLI gate, TUI,
// future RPC clients) so "n" can never mean allow in one UI and deny in
// another. Callers add context to the denial.
//
// A "n: <note>" (or "no:"/"deny:") answer denies with the note attached,
// so the refusal fed back to the model carries the operator's redirect.
// The note only survives where the caller returns Decide's own error
// (the TUI does; the CLI wraps it in its own wording).
func Decide(answer string) (Effect, error) {
	text := strings.TrimSpace(answer)
	if head, tail, ok := strings.Cut(text, ":"); ok {
		switch strings.ToLower(strings.TrimSpace(head)) {
		case "n", "no", "deny":
			if note := strings.TrimSpace(tail); note != "" {
				return Deny, fmt.Errorf("blocked by operator: %s", note)
			}
			return Deny, fmt.Errorf("blocked by operator")
		}
	}
	switch strings.ToLower(text) {
	case "y", "yes", "a", "always":
		return Allow, nil
	default:
		return Deny, fmt.Errorf("blocked by operator")
	}
}
