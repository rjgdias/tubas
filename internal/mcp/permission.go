package mcp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// ── code: answering claude's permission prompts ─────────────────────────
//
// An agent with write access to a repository runs claude with its own
// file tools, in the session's worktree, and --permission-prompt-tool
// pointing here. Reading never asks. Everything else that would prompt
// comes to this tool, and the rules live in code:
//
//   - Edit / Write inside the agent's worktree: allowed. You review the
//     whole diff at the end, so each edit doesn't need you.
//   - Edit / Write anywhere else: denied.
//   - Bash: waits for your y in the inbox, every time (with the senior's
//     review first, when escalation has before_write).
//   - Anything else: denied.

// permissionTool answers permission prompts for the worktrees in dirs.
func (g *Gateway) permissionTool(dirs []string) tool {
	schema := object(map[string]any{
		"tool_name":   str("the tool claude wants to use"),
		"input":       map[string]any{"type": "object", "description": "its arguments"},
		"tool_use_id": str("the call's id"),
	}, "tool_name", "input")
	return tool{name: "permission", description: "Answers permission prompts for this agent (used by tubas, not by the agent).",
		schema: schema, readOnly: true,
		call: func(raw json.RawMessage) (any, error) {
			var req struct {
				Tool  string         `json:"tool_name"`
				Input map[string]any `json:"input"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return permission(false, nil, "bad request: "+err.Error()), nil
			}
			ok, why := g.decide(req.Tool, req.Input, dirs)
			return permission(ok, req.Input, why), nil
		}}
}

// decide applies the rules above.
func (g *Gateway) decide(tool string, input map[string]any, dirs []string) (bool, string) {
	path := func() string {
		for _, k := range []string{"file_path", "notebook_path", "path"} {
			if p, ok := input[k].(string); ok && p != "" {
				return p
			}
		}
		return ""
	}
	switch tool {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		p := path()
		if p != "" && insideAny(p, dirs) {
			return true, ""
		}
		return false, fmt.Sprintf("%s is outside your worktree: you may only change files in %s", p, strings.Join(dirs, ", "))
	case "Read", "Grep", "Glob", "LS":
		if p := path(); p == "" || insideAny(p, dirs) {
			return true, ""
		}
		return false, "that path is outside the folders you were given"
	case "Bash", "PowerShell":
		command, _ := input["command"].(string)
		why, _ := input["description"].(string)
		detail := "$ " + command
		if why != "" {
			detail += "\nwhy: " + why
		}
		detail += "\nwhere: " + strings.Join(dirs, ", ")
		if g.senior != nil && g.senior.cfg.Has("before_write") {
			detail += "\n\n" + g.senior.ask("Review this shell command before a human approves it. Is it safe and needed? "+
				"Start with OK or CONCERN, then say why in a line.\n"+detail)
		}
		approved, reason, err := g.askApproval(strings.ToLower(tool)+"("+clip(oneLine(command), 60)+")", detail)
		if err != nil {
			return false, "could not ask for approval: " + err.Error()
		}
		if !approved {
			if reason == "" {
				reason = "denied by the human reviewer"
			}
			return false, reason + ": do not run it another way"
		}
		return true, ""
	}
	return false, tool + " is not available in tubas"
}

// permission is the answer claude expects from a permission prompt tool.
func permission(allow bool, input map[string]any, message string) any {
	answer := map[string]any{"behavior": "deny", "message": message}
	if allow {
		if input == nil {
			input = map[string]any{}
		}
		answer = map[string]any{"behavior": "allow", "updatedInput": input}
	}
	data, _ := json.Marshal(answer)
	return toolResult(string(data), false)
}

// insideAny says whether path is inside one of dirs, symbolic links
// included.
func insideAny(path string, dirs []string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, d := range dirs {
		base, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		if staysInside(abs, base) == nil {
			return true
		}
	}
	return false
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
