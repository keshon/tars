package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// onePixelPNG is a 1x1 transparent PNG.
var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

func writeTestPNG(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, onePixelPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEncodeContent_PlainStringWithoutImages(t *testing.T) {
	raw, err := encodeContent("hi", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `"hi"` {
		t.Fatalf("content = %s, want plain JSON string", raw)
	}
}

func TestEncodeContent_MultipartWithImage(t *testing.T) {
	p := writeTestPNG(t, "a.png")
	raw, err := encodeContent("see this", []string{p}, true)
	if err != nil {
		t.Fatal(err)
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("content is not a parts array: %s", raw)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "see this" {
		t.Fatalf("first part = %+v", parts)
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Fatalf("second part = %+v", parts[1])
	}
	if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("url = %.40q", parts[1].ImageURL.URL)
	}
}

func TestEncodeContent_KoboldRefusesImages(t *testing.T) {
	p := writeTestPNG(t, "a.png")
	if _, err := encodeContent("see", []string{p}, false); err == nil {
		t.Fatal("want loud refusal, got silent encoding")
	}
}

func TestEncodeContent_ErrorsAreDescriptive(t *testing.T) {
	for _, tc := range []struct {
		name   string
		images []string
	}{
		{"missing", []string{filepath.Join(t.TempDir(), "nope.png")}},
		{"bad ext", []string{writeTestFile(t, "a.txt", "text")}},
		{"directory", []string{t.TempDir()}},
	} {
		if _, err := encodeContent("see", tc.images, true); err == nil {
			t.Errorf("%s: want error, got nil", tc.name)
		}
	}
}

func writeTestFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEncodeMessages_PreservesToolCalls(t *testing.T) {
	p := writeTestPNG(t, "a.png")
	msgs := []Message{{
		Role:    RoleAssistant,
		Content: "work",
		Images:  []string{p},
		ToolCalls: []ToolCall{{
			ID: "1", Name: "list_files", Arguments: json.RawMessage(`{"path":"."}`),
		}},
	}}
	out, err := encodeMessages(msgs, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].ToolCalls) != 1 {
		t.Fatalf("tool calls lost: %+v", out)
	}
	if string(out[0].ToolCalls[0].Function.Arguments) != `"{\"path\":\".\"}"` {
		// encodeArguments re-wraps the object form into a JSON string.
		t.Fatalf("args = %s", out[0].ToolCalls[0].Function.Arguments)
	}
}

func TestDecodeContent_StringAndParts(t *testing.T) {
	if got := decodeContent(json.RawMessage(`"hi"`)); got != "hi" {
		t.Fatalf("string: %q", got)
	}
	parts, _ := json.Marshal([]contentPart{
		{Type: "text", Text: "a"},
		{Type: "image_url", ImageURL: &struct {
			URL string `json:"url"`
		}{URL: "data:x"}},
	})
	if got := decodeContent(parts); got != "a" {
		t.Fatalf("parts: %q", got)
	}
}
