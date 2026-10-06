package llm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// imageMaxBytes caps one attached image at 20MB: vision turns already
// spend thousands of context tokens per picture, and anything bigger
// is a video frame or a mistake, not a screenshot.
const imageMaxBytes = 20 << 20

// imageMIME maps accepted extensions to wire MIME types. Deliberately
// narrow: servers agree on these, and an exotic codec failing server-
// side looks exactly like model blindness.
var imageMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
	".bmp":  "image/bmp",
}

// IsImagePath reports whether path names a sendable image by
// extension. The wire encoder re-checks existence and size.
func IsImagePath(path string) bool {
	_, ok := imageMIME[strings.ToLower(filepath.Ext(path))]
	return ok
}

// encodeImageFile reads an absolute image path into a data URL. Paths
// arrive resolved (workspace escape already checked at attach time);
// this checks existence, size, and format only.
func encodeImageFile(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	mime, ok := imageMIME[ext]
	if !ok {
		return "", fmt.Errorf("image %q: format %q not supported (png, jpg, webp, gif, bmp)", path, ext)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("image %q: %w", path, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("image %q: is a directory", path)
	}
	if fi.Size() > imageMaxBytes {
		return "", fmt.Errorf("image %q: %d bytes over the %d-byte cap", path, fi.Size(), imageMaxBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("image %q: %w", path, err)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// contentPart is one OpenAI content part: text or an image URL.
type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

// encodeContent builds a message's wire content: a plain string when
// imageless (byte-identical to the old shape — text models, text
// backends, and golden tests never see a difference), a multipart
// array otherwise. allowImages is false on backends that cannot
// process pictures: attaching there is a loud error, never a silent
// drop the model then hallucinates around.
func encodeContent(text string, images []string, allowImages bool) (json.RawMessage, error) {
	if len(images) == 0 {
		raw, err := json.Marshal(text)
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
	if !allowImages {
		return nil, fmt.Errorf("images attached but this backend cannot process images — use a vision-capable llama-server backend with --mmproj")
	}
	parts := []contentPart{{Type: "text", Text: text}}
	for _, path := range images {
		url, err := encodeImageFile(path)
		if err != nil {
			return nil, err
		}
		parts = append(parts, contentPart{
			Type: "image_url",
			ImageURL: &struct {
				URL string `json:"url"`
			}{URL: url},
		})
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// encodeMessages converts history to the wire shape, shared by Chat
// and Stream so both paths attach (or refuse) identically.
func encodeMessages(messages []Message, allowImages bool) ([]wireMessage, error) {
	out := make([]wireMessage, 0, len(messages))
	for _, m := range messages {
		content, err := encodeContent(m.Content, m.Images, allowImages)
		if err != nil {
			return nil, err
		}
		wm := wireMessage{Role: string(m.Role), Content: content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			// Per the OpenAI tool-call wire format, function.arguments is a
			// JSON-encoded *string*, not a raw object — encodeArguments
			// re-wraps our internal object form before it goes back out.
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireFunction{
					Name:      tc.Name,
					Arguments: encodeArguments(tc.Arguments),
				},
			})
		}
		out = append(out, wm)
	}
	return out, nil
}
