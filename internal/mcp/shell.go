package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ── run_command: the shell, for harnesses tubas can't intercept ─────────
//
// claude asks tubas before each shell command (see permission.go). codex,
// ACP agents and the api harness run with their own shell turned off, and
// get this tool instead: it runs a command in the agent's worktree after
// your y in the inbox, every time, with the senior's review first when
// escalation has before_write.

const maxCommandOutput = 4000

func (g *Gateway) shellTool(dirs []string) tool {
	schema := object(map[string]any{
		"command": str("the command line to run, in your worktree"),
		"why":     str("why you need it, in a few words, for the human who approves it"),
	}, "command")
	return tool{name: "run_command", description: "Run a shell command (tests, a linter, a build) in your worktree. " +
		"A human approves every command first, so run only what you need. Never commit, push or switch branches.",
		schema: schema, noApproval: true, // it asks itself, with the folder and the reason
		call: func(raw json.RawMessage) (any, error) {
			var req struct {
				Command string `json:"command"`
				Why     string `json:"why"`
			}
			if err := json.Unmarshal(raw, &req); err != nil || strings.TrimSpace(req.Command) == "" {
				return toolResult("give the command to run", true), nil
			}
			dir := dirs[0]
			detail := "$ " + req.Command
			if req.Why != "" {
				detail += "\nwhy: " + req.Why
			}
			detail += "\nwhere: " + dir
			if g.senior != nil && g.senior.cfg.Has("before_write") {
				detail += "\n\n" + g.senior.ask("Review this shell command before a human approves it. Is it safe and needed? "+
					"Start with OK or CONCERN, then say why in a line.\n"+detail)
			}
			approved, reason, err := g.askApproval("run_command("+clip(oneLine(req.Command), 60)+")", detail)
			if err != nil {
				return toolResult("could not ask for approval: "+err.Error(), true), nil
			}
			if !approved {
				if reason == "" {
					reason = "denied by the human reviewer"
				}
				return toolResult(reason+": do not run it another way", true), nil
			}
			out, code := runShell(req.Command, dir, 10*time.Minute)
			text := fmt.Sprintf("exit %d\n%s", code, out)
			return toolResult(text, code != 0), nil
		}}
}

// runShell runs a command line with the system's shell and returns its
// output (the end of it, when long) and exit code.
func runShell(command, dir string, limit time.Duration) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if r := []rune(text); len(r) > maxCommandOutput {
		text = "…" + string(r[len(r)-maxCommandOutput:])
	}
	code := 0
	if err != nil {
		code = -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		if ctx.Err() != nil {
			text += "\n(stopped: it ran for more than " + limit.String() + ")"
		}
	}
	return text, code
}
