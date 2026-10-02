// Package permission decides whether a tool call may run.
//
// It is a leaf: no internal imports, so the agent loop can depend on it
// without creating a cycle. Policy is last-match-wins over wildcard
// patterns, mirroring pi/opencode: explicit deny beats broad allow.
package permission

import (
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

// Default returns the shipping policy: permissive but sensitive reads ask.
func Default() Policy {
	return Policy{Rules: []Rule{
		{Tool: "*", Pattern: "*", Effect: Allow},
		{Tool: "read_file", Pattern: "*.env", Effect: Ask},
		{Tool: "read_file", Pattern: "*.env.*", Effect: Ask},
		{Tool: "read_file", Pattern: "*credentials*", Effect: Ask},
		{Tool: "read_file", Pattern: "*secret*", Effect: Ask},
	}}
}

// Evaluate returns the effect for a tool call against a resource.
// No match means Allow: the default is open, restrictions opt in.
func (p Policy) Evaluate(tool, resource string) Effect {
	eff := Allow
	matched := false
	for _, r := range p.Rules {
		if !toolMatch(r.Tool, tool) {
			continue
		}
		if !resourceMatch(r.Pattern, resource) {
			continue
		}
		eff = r.Effect
		matched = true
	}
	_ = matched
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
