package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// toolResource extracts the policy-matched resource from a call's args:
// command for shell/background, path for file tools, url for check_url.
func toolResource(name string, args json.RawMessage) string {
	var fields struct {
		Command string `json:"command"`
		Path    string `json:"path"`
		From    string `json:"from"`
		To      string `json:"to"`
		URL     string `json:"url"`
		ID      string `json:"id"`
	}
	if json.Unmarshal(args, &fields) != nil {
		return ""
	}
	switch name {
	case "run_shell", "start_background":
		return fields.Command
	case "read_file", "write_file", "patch_file", "patch_lines":
		return fields.Path
	case "move_file":
		return fields.From + "->" + fields.To
	case "check_url", "webfetch", "fetch_raw":
		return fields.URL
	default:
		if fields.Path != "" {
			return fields.Path
		}
		if fields.Command != "" {
			return fields.Command
		}
		return string(args)
	}
}

// mutatedPathsFromCall pulls workspace paths out of a mutating tool
// call's arguments. The project's file tools name them path (write_file,
// patch_file, patch_lines) or from/to (move_file); a mutating tool with
// none of these contributes nothing rather than guessing.
func mutatedPathsFromCall(args json.RawMessage) []string {
	var fields struct {
		Path string `json:"path"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	if json.Unmarshal(args, &fields) != nil {
		return nil
	}
	var out []string
	for _, p := range []string{fields.Path, fields.From, fields.To} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func writePathFromArgs(args json.RawMessage) string {
	var fields struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &fields) != nil || fields.Path == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ReplaceAll(fields.Path, "\\", "/"), "./")
}

// parseDelegateMutations reads the structured DELEGATE header from a
// delegate_task result so the parent run can count subagent writes.
func parseDelegateMutations(content string) (int, []string) {
	if !strings.HasPrefix(content, "DELEGATE\n") {
		return 0, nil
	}
	mutations := 0
	var paths []string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "mutations: ") {
			var n int
			if _, err := fmt.Sscanf(line, "mutations: %d", &n); err == nil {
				mutations = n
			}
			continue
		}
		if p, ok := strings.CutPrefix(line, "paths: "); ok {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
			continue
		}
		if line == "----" {
			break
		}
	}
	return mutations, paths
}
