package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// reduceDefaults bound one reduction. Depth past maxDepth collapses to a
// type summary; arrays past maxItems keep their head plus a count.
const (
	reduceMaxDepth = 4
	reduceMaxItems = 20
	reduceMaxBytes = 48 * 1024
)

// ReduceJSON renders verbose JSON as compact text for a model to read.
// Middle-truncation can cut an array mid-element, leaving half a
// structure to parse; reduction keeps whole values instead and says what
// it dropped. Non-JSON input passes through unchanged (use TruncateMiddle
// for that). Output never exceeds maxBytes; pass 0 for the default.
func ReduceJSON(data []byte, maxBytes int) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	if maxBytes <= 0 {
		maxBytes = reduceMaxBytes
	}
	var b strings.Builder
	dropped := renderValue(&b, v, 0)
	out := b.String()
	if len(out) > maxBytes {
		out = out[:maxBytes] + fmt.Sprintf("\n...(reduced output truncated at %d bytes)", maxBytes)
	}
	if dropped > 0 {
		out += fmt.Sprintf("\n(%d values summarized away — refine the query for detail)", dropped)
	}
	return out
}

func renderValue(b *strings.Builder, v any, depth int) (dropped int) {
	switch t := v.(type) {
	case map[string]any:
		if depth >= reduceMaxDepth {
			fmt.Fprintf(b, "{object with %d keys}", len(t))
			return 1
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("{\n")
		for _, k := range keys {
			b.WriteString(strings.Repeat("  ", depth+1) + k + ": ")
			dropped += renderValue(b, t[k], depth+1)
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat("  ", depth) + "}")
		return dropped
	case []any:
		if depth >= reduceMaxDepth {
			fmt.Fprintf(b, "[array of %d]", len(t))
			return 1
		}
		shown := t
		if len(t) > reduceMaxItems {
			shown = t[:reduceMaxItems]
			dropped += len(t) - reduceMaxItems
		}
		b.WriteString("[\n")
		for _, item := range shown {
			b.WriteString(strings.Repeat("  ", depth+1))
			dropped += renderValue(b, item, depth+1)
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat("  ", depth) + "]")
		if len(t) > reduceMaxItems {
			fmt.Fprintf(b, " (%d of %d shown)", len(shown), len(t))
		}
		return dropped
	case string:
		if len(t) > 500 {
			fmt.Fprintf(b, "%q... (%d chars)", t[:500], len(t))
			return 0
		}
		fmt.Fprintf(b, "%q", t)
		return 0
	default:
		fmt.Fprintf(b, "%v", t)
		return 0
	}
}
