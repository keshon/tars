package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReduceJSON_ArrayHeadPlusCount(t *testing.T) {
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}
	data := `{"results": [`
	for i, n := range items {
		if i > 0 {
			data += ","
		}
		data += string(rune('0' + n%10))
	}
	data += `], "total": 100}`
	out := ReduceJSON([]byte(data), 0)
	if !strings.Contains(out, "20 of 100 shown") {
		t.Fatalf("no head+count: %q", out)
	}
	if !strings.Contains(out, "total: 100") {
		t.Fatalf("scalar lost: %q", out)
	}
	if !strings.Contains(out, "summarized away") {
		t.Fatalf("no drop report: %q", out)
	}
}

func TestReduceJSON_DepthCollapse(t *testing.T) {
	// Built programmatically: hand-counted braces failed once already.
	v := map[string]any{"f": 1}
	for _, k := range []string{"e", "d", "c", "b", "a"} {
		v = map[string]any{k: v}
	}
	data, _ := json.Marshal(v)
	out := ReduceJSON(data, 0)
	if !strings.Contains(out, "object with") {
		t.Fatalf("depth not collapsed: %q", out)
	}
	if !strings.Contains(out, "a:") || !strings.Contains(out, "b:") {
		t.Fatalf("shallow keys lost: %q", out)
	}
}

func TestReduceJSON_InvalidPassesThrough(t *testing.T) {
	if got := ReduceJSON([]byte("plain text"), 0); got != "plain text" {
		t.Fatalf("got %q", got)
	}
}

func TestReduceJSON_ScalarsWhole(t *testing.T) {
	out := ReduceJSON([]byte(`{"ok":true,"n":42,"s":"hi"}`), 0)
	for _, want := range []string{`ok: true`, `n: 42`, `s: "hi"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %q", want, out)
		}
	}
}

func TestReduceJSON_ByteCap(t *testing.T) {
	big := `{"items":["` + strings.Repeat("x", 5000) + `"]}`
	out := ReduceJSON([]byte(big), 100)
	if len(out) > 500 {
		t.Fatalf("over cap: %d bytes", len(out))
	}
}
