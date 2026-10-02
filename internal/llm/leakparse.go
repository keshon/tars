package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// LeakedCall is a tool call the model wrote as text instead of a
// structured call: the name it meant plus its arguments in object form.
type LeakedCall struct {
	Name      string
	Arguments json.RawMessage
}

// ExtractLeakedCalls scans assistant text for tool calls written as text
// and returns the ones worth executing, the content with them (and any
// trailing partial fragment) removed, and whether the text looked like a
// leak at all.
//
// Recovery beats refusal here. The old behavior answered a leaked call
// with a nudge ("that was text, make a real call"), burning a step — and
// a weak model often failed the retry the same way. A leaked call that
// names a real tool with parseable arguments is unambiguous about intent;
// executing it spends zero extra steps.
//
// Three shapes recover, all gated on known tools with JSON-object args:
//
//  1. tagged: <tool_call>call:NAME{...}</...> or
//     <tool_call>{"name":"NAME","arguments":{...}}</...>
//  2. narrative (Gemma): "made a function call call_ID to NAME with
//     arguments={...}"
//  3. envelope: the whole message (or a ```json fence) is a JSON object
//     with name+arguments, or an array of them.
//
// Anything else with leak markers (unknown tool, broken JSON) is not
// recovered but still reports leak=true, preserving the old nudge path.
// A trailing partial fragment (`<tool_`, a dangling `call_ID`) is held
// back from the cleaned text: with unary responses there is no later
// chunk completing it, so keeping it only feeds the model its own stump
// to imitate.
func ExtractLeakedCalls(content string, known map[string]bool) (calls []LeakedCall, cleaned string, leak bool) {
	cleaned = content
	// Whole-content and fenced envelopes first: they own the message, so
	// surrounding-prose handling below must not re-scan their insides.
	if cs, rest, ok := extractEnvelope(content, known); ok {
		calls = append(calls, cs...)
		cleaned = rest
		leak = true
	}
	if cs, rest, ok := extractFenced(cleaned, known); ok {
		calls = append(calls, cs...)
		cleaned = rest
		leak = true
	}
	// Tagged and narrative shapes, left to right.
	scan := cleaned
	for {
		call, span, ok := nextTaggedOrNarrative(scan, known)
		if !ok {
			break
		}
		calls = append(calls, call)
		scan = scan[:span[0]] + scan[span[1]:]
		leak = true
	}
	cleaned = scan
	if frag, stripped := holdBackPartial(cleaned); frag {
		cleaned = stripped
		leak = true
	}
	cleaned = strings.TrimSpace(cleaned)
	if hasLeakMarkers(content) {
		leak = true
	}
	return calls, cleaned, leak
}

// span is a [start, end) byte range in the scanned string.
type span [2]int

// callObject tries to read raw as {"name": N, "arguments": {...}} with N
// a known tool. Arguments may be an object or a JSON string wrapping one
// (the same shape normalizeArguments accepts on the wire).
func callObject(raw json.RawMessage, known map[string]bool) (LeakedCall, bool) {
	var obj struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Function  *struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return LeakedCall{}, false
	}
	name, args := obj.Name, obj.Arguments
	if obj.Function != nil {
		name, args = obj.Function.Name, obj.Function.Arguments
	}
	if name == "" || !known[name] {
		return LeakedCall{}, false
	}
	args = normalizeArguments(args)
	var probe map[string]any
	if err := json.Unmarshal(args, &probe); err != nil {
		return LeakedCall{}, false
	}
	return LeakedCall{Name: name, Arguments: args}, true
}

// extractEnvelope tries the whole message as one call object or an array
// of them. Returns the calls and the leftover text ("" when the message
// was nothing but the envelope).
func extractEnvelope(content string, known map[string]bool) ([]LeakedCall, string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed[0] != '[' && trimmed[0] != '{' {
		return nil, content, false
	}
	raw, end := balancedJSON(trimmed, 0)
	if raw == "" || strings.TrimSpace(trimmed[end:]) != "" {
		return nil, content, false
	}
	if call, ok := callObject(json.RawMessage(raw), known); ok {
		return []LeakedCall{call}, "", true
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(json.RawMessage(raw), &arr); err != nil {
		return nil, content, false
	}
	var calls []LeakedCall
	for _, item := range arr {
		call, ok := callObject(item, known)
		if !ok {
			return nil, content, false
		}
		calls = append(calls, call)
	}
	if len(calls) == 0 {
		return nil, content, false
	}
	return calls, "", true
}

// extractFenced tries each ```json (or bare ```) fence as an envelope.
// A fence that does not parse as calls is left alone: prose code samples
// must never be eaten.
func extractFenced(content string, known map[string]bool) ([]LeakedCall, string, bool) {
	type cut struct{ from, to int }
	var calls []LeakedCall
	var cuts []cut
	pos := 0
	for {
		rel := strings.Index(content[pos:], "```")
		if rel < 0 {
			break
		}
		start := pos + rel
		after := content[start+3:]
		nl := strings.Index(after, "\n")
		if nl < 0 {
			break
		}
		body := after[nl+1:]
		close := strings.Index(body, "```")
		if close < 0 {
			break // unclosed fence: hold-back handles a trailing one
		}
		inner := body[:close]
		fenceEnd := start + 3 + nl + 1 + close + 3
		if cs, _, ok := extractEnvelope(inner, known); ok {
			calls = append(calls, cs...)
			cuts = append(cuts, cut{start, fenceEnd})
		}
		pos = fenceEnd
	}
	if len(calls) == 0 {
		return nil, content, false
	}
	var b strings.Builder
	prev := 0
	for _, c := range cuts {
		b.WriteString(content[prev:c.from])
		prev = c.to
	}
	b.WriteString(content[prev:])
	return calls, strings.TrimSpace(b.String()), true
}

var tagOpen = regexp.MustCompile(`<\|?tool_call>?`)

// nextTaggedOrNarrative finds the first recoverable tagged or narrative
// call in s. Returns the call and its byte span.
func nextTaggedOrNarrative(s string, known map[string]bool) (LeakedCall, span, bool) {
	best := span{-1, -1}
	var bestCall LeakedCall
	found := false
	consider := func(call LeakedCall, sp span) {
		if !found || sp[0] < best[0] {
			best, bestCall, found = sp, call, true
		}
	}
	// Tagged regions: <tool_call>...</tool_call>, <|tool_call|>...,
	// or an unclosed tag running to end of message.
	for _, loc := range tagOpen.FindAllStringIndex(s, -1) {
		inner := s[loc[1]:]
		end := len(s)
		if i := strings.Index(inner, "</tool_call>"); i >= 0 {
			end = loc[1] + i + len("</tool_call>")
			inner = inner[:i]
		} else if i := strings.Index(inner, "<"); i >= 0 {
			end = loc[1] + i
			inner = inner[:i]
		}
		if call, ok := parseTaggedInner(inner, known); ok {
			consider(call, span{loc[0], end})
		}
	}
	// Narrative: "made a function call <id> to <name> with arguments=<json>".
	if loc := narrativeRe.FindStringSubmatchIndex(s); loc != nil {
		name := s[loc[2]:loc[3]]
		if known[name] {
			if raw, end := balancedJSON(s, loc[1]); raw != "" {
				args := normalizeArguments(json.RawMessage(raw))
				var probe map[string]any
				if json.Unmarshal(args, &probe) == nil {
					consider(LeakedCall{Name: name, Arguments: args}, span{loc[0], end})
				}
			}
		}
	}
	return bestCall, best, found
}

var narrativeRe = regexp.MustCompile(`(?i)made a function call \S+ to ([A-Za-z0-9_]+) with arguments=`)

// parseTaggedInner reads the body of a tag: either `call:NAME{json}` or a
// JSON object with name+arguments.
func parseTaggedInner(inner string, known map[string]bool) (LeakedCall, bool) {
	trimmed := strings.TrimSpace(inner)
	if m := regexp.MustCompile(`^call:([A-Za-z0-9_]+)\s*`).FindStringSubmatch(trimmed); m != nil {
		name := m[1]
		if !known[name] {
			return LeakedCall{}, false
		}
		rest := trimmed[len(m[0]):]
		raw, _ := balancedJSON(rest, 0)
		if raw == "" {
			return LeakedCall{}, false
		}
		args := normalizeArguments(json.RawMessage(raw))
		var probe map[string]any
		if json.Unmarshal(args, &probe) != nil {
			return LeakedCall{}, false
		}
		return LeakedCall{Name: name, Arguments: args}, true
	}
	raw, _ := balancedJSON(trimmed, 0)
	if raw == "" {
		return LeakedCall{}, false
	}
	return callObject(json.RawMessage(raw), known)
}

// balancedJSON returns the JSON value starting at the first `{` or `[`
// at or after start, with string/escape awareness. "" when none opens.
func balancedJSON(s string, start int) (string, int) {
	open := -1
	for i := start; i < len(s); i++ {
		if s[i] == '{' || s[i] == '[' {
			open = i
			break
		}
	}
	if open < 0 {
		return "", start
	}
	depth := 0
	inStr := false
	esc := false
	for i := open; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return s[open : i+1], i + 1
			}
		}
	}
	return "", start
}

// partialTail matches a trailing fragment that can only be the stump of
// a leaked call: an opening tag fragment, a dangling call id, or an
// arguments key with nothing after it. Trailing whitespace allowed.
var partialTail = regexp.MustCompile(`(?i)(<\|?tool_?[a-z_]*|<\|tool_call\|?|call_[A-Za-z0-9_]*|arguments\s*=\s*)\s*$`)

// holdBackPartial strips a trailing partial fragment. Strata's hold-back
// waits for the next chunk; with unary responses there is none, so the
// stump is dropped instead of fed back as content to imitate.
func holdBackPartial(content string) (bool, string) {
	if loc := partialTail.FindStringIndex(content); loc != nil {
		return true, strings.TrimSpace(content[:loc[0]])
	}
	return false, content
}

// hasLeakMarkers is the old blocklist, kept only for the unparseable
// remainder: tagged text naming an unknown tool, or JSON too broken to
// balance, still deserves the nudge rather than a silent accept.
func hasLeakMarkers(content string) bool {
	lower := strings.ToLower(content)
	if strings.Contains(lower, "<tool_call") || strings.Contains(lower, "<|tool_call") {
		return true
	}
	if strings.Contains(lower, "made a function call") {
		return true
	}
	return strings.Contains(content, "call_") && strings.Contains(lower, "arguments=")
}

// LeakCallID synthesizes a tool-call id for a recovered call. Model-made
// ids (call_92023) can repeat across steps, which would corrupt tool-result
// matching — harness ids carry the step instead.
func LeakCallID(step, i int) string {
	return fmt.Sprintf("leaked-%d-%d", step, i)
}
