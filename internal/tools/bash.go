package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultTimeout = 2 * time.Minute
	maxTimeout     = 10 * time.Minute
	cwdMark        = "<<tubas-cwd>>"
)

var bashTool = Tool{
	Name: "bash",
	Description: "Run a shell command in your worktree: tests, a linter, a build, git status or diff. The folder you cd into is kept " +
		"for the next command. A human approves every command first, so run only what you need, and say why. " +
		"Never commit, push or switch branches. Stops after 2 minutes unless timeout says more (600 at most).",
	Schema: schema(map[string]any{
		"command": str("the command line"),
		"why":     str("why you need it, in a few words, for the human who approves it"),
		"timeout": num("seconds before it's stopped (default 120, at most 600)"),
	}, "command"),
	run: func(ctx context.Context, b *Box, input json.RawMessage) (string, error) {
		var in struct {
			Command string `json:"command"`
			Why     string `json:"why"`
			Timeout int    `json:"timeout"`
		}
		if err := decode(input, &in); err != nil {
			return "", err
		}
		command := strings.TrimSpace(in.Command)
		if command == "" {
			return "", errors.New("give the command to run")
		}
		limit := defaultTimeout
		if in.Timeout > 0 {
			limit = min(time.Duration(in.Timeout)*time.Second, maxTimeout)
		}
		dir := b.shellDir()
		if !b.allowed(command) {
			if err := b.approve(ctx, command, in.Why, dir); err != nil {
				return "", err
			}
		}
		out, code, newDir, err := runShell(ctx, command, dir, limit)
		if err != nil {
			return "", err
		}
		note := b.moveShell(newDir)
		text := strings.TrimRight(out, "\n")
		if text == "" {
			text = "(no output)"
		}
		if note != "" {
			text += "\n" + note
		}
		if code != 0 {
			return "", fmt.Errorf("exit %d\n%s", code, text)
		}
		return text, nil
	},
}

// approve asks you before a command, with a senior's review when the
// agent's escalation has before_write.
func (b *Box) approve(ctx context.Context, command, why, dir string) error {
	if b.Ask == nil {
		return errors.New("nobody can approve commands here, so none run")
	}
	detail := "$ " + command
	if why != "" {
		detail += "\nwhy: " + why
	}
	detail += "\nwhere: " + dir
	if b.Review != nil {
		if review := b.Review(ctx, "Review this shell command before a human approves it. Is it safe and needed? "+
			"Start with OK or CONCERN, then say why in a line.\n"+detail); review != "" {
			detail += "\n\n" + review
		}
	}
	ok, reason, err := b.Ask(ctx, "bash("+oneLine(command, 60)+")", detail)
	if err != nil {
		return fmt.Errorf("could not ask for approval: %v", err)
	}
	if !ok {
		if reason == "" {
			reason = "denied by the human reviewer"
		}
		return errors.New(reason + ": don't run it another way")
	}
	return nil
}

// allowed says whether team.yaml lets this command run without a yes: it
// starts with an allowed prefix and chains nothing after it.
func (b *Box) allowed(command string) bool {
	if strings.ContainsAny(command, ";&|`$<>\n\r") {
		return false
	}
	for _, p := range b.Allow {
		p = strings.TrimSpace(p)
		if p != "" && (command == p || strings.HasPrefix(command, p+" ")) {
			return true
		}
	}
	return false
}

// shellDir is where the next command runs: where the last one ended, or
// the worktree.
func (b *Box) shellDir() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cwd != "" && InsideAny(b.cwd, b.Write) {
		if info, err := os.Stat(b.cwd); err == nil && info.IsDir() {
			return b.cwd
		}
	}
	return b.Write[0]
}

// moveShell keeps the folder a command ended in, if it's in the worktree.
func (b *Box) moveShell(dir string) string {
	if dir == "" {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if InsideAny(dir, b.Write) {
		b.cwd = dir
		return ""
	}
	b.cwd = ""
	return "(the shell is back in " + b.Write[0] + ": commands run only in your worktree)"
}

// runShell runs a command line and returns its output, exit code and the
// folder it ended in. A command past its time is stopped with everything
// it started.
func runShell(ctx context.Context, command, dir string, limit time.Duration) (string, int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	exe, kind := Shell()
	var cmd *exec.Cmd
	if kind == "powershell" {
		script := command + "\n$__s = if ($LASTEXITCODE) { $LASTEXITCODE } elseif ($?) { 0 } else { 1 }\n" +
			"Write-Output \"`n" + cwdMark + "$((Get-Location).ProviderPath)\"\nexit $__s"
		cmd = exec.CommandContext(ctx, exe, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	} else {
		script := command + "\n__tubas_s=$?\nprintf '\\n" + cwdMark + "%s\\n' \"$(pwd -W 2>/dev/null || pwd)\"\nexit $__tubas_s"
		cmd = exec.CommandContext(ctx, exe, "-c", script)
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "PAGER=cat", "TERM=dumb", "NO_COLOR=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	stopTree(cmd)
	cmd.WaitDelay = 3 * time.Second // a child that keeps the output open doesn't hold the turn
	err := cmd.Run()

	text := strings.ReplaceAll(out.String(), "\r\n", "\n")
	newDir := ""
	if i := strings.LastIndex(text, cwdMark); i >= 0 {
		newDir = filepath.Clean(filepath.FromSlash(strings.TrimSpace(text[i+len(cwdMark):])))
		text = strings.TrimSuffix(text[:i], "\n")
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		switch {
		case ctx.Err() == context.DeadlineExceeded:
			return text + fmt.Sprintf("\n(stopped: it ran for more than %s; give a longer timeout, up to 600 seconds, if it needs it)", limit), -1, "", nil
		case ctx.Err() != nil:
			return "", -1, "", ctx.Err()
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			return "", -1, "", fmt.Errorf("could not run the shell (%s): %v", exe, err)
		}
	}
	return text, code, newDir, nil
}

// Shell is the shell commands run in: sh, or Git Bash on Windows
// (PowerShell when there's none), as Claude Code does.
func Shell() (exe, kind string) {
	if runtime.GOOS != "windows" {
		if _, err := os.Stat("/bin/bash"); err == nil {
			return "/bin/bash", "sh"
		}
		return "/bin/sh", "sh"
	}
	candidates := []string{os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"), `C:\Program Files\Git\bin\bash.exe`}
	if git, err := exec.LookPath("git"); err == nil { // Git installed elsewhere: its bash is in a folder above
		dir := filepath.Dir(git) // …\Git\cmd, or …\Git\mingw64\bin
		for range 3 {
			dir = filepath.Dir(dir)
			candidates = append(candidates, filepath.Join(dir, "bin", "bash.exe"), filepath.Join(dir, "usr", "bin", "bash.exe"))
		}
	}
	for _, bash := range candidates {
		if bash != "" {
			if _, err := os.Stat(bash); err == nil {
				return bash, "sh"
			}
		}
	}
	return "powershell.exe", "powershell"
}

// ShellCommand runs a command line in Shell, for helpers and hooks.
func ShellCommand(ctx context.Context, line string) *exec.Cmd {
	exe, kind := Shell()
	if kind == "powershell" {
		return exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-Command", line)
	}
	return exec.CommandContext(ctx, exe, "-c", line)
}

// oneLine squeezes a text into one line of at most n characters.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
