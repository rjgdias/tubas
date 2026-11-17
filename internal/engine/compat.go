package engine

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ── older Claude Code versions ──────────────────────────────────────────
//
// tubas starts claude with options that came in different versions
// (--permission-prompts is recent). A work computer may have an older
// claude, which stops at the first option it doesn't know. So tubas reads
// `claude --help` once and leaves out what this claude doesn't know, with
// the closest older way when there is one.

// claudeValued are the options tubas passes that take a value.
var claudeValued = map[string]bool{
	"--output-format": true, "--model": true, "--system-prompt": true, "--tools": true,
	"--permission-prompts": true, "--permission-prompt-tool": true, "--setting-sources": true,
	"--effort": true, "--mcp-config": true, "--allowedTools": true, "--disallowedTools": true,
	"--add-dir": true, "--settings": true, "--json-schema": true, "--permission-mode": true,
}

// builtinTools are claude's own tools, for older claudes without --tools.
const builtinTools = "Bash,BashOutput,KillShell,Edit,MultiEdit,Write,Read,Grep,Glob,LS,NotebookEdit,NotebookRead,WebFetch,WebSearch,Task,TodoWrite"

var flagPattern = regexp.MustCompile(`--[a-zA-Z][a-zA-Z0-9-]*`)

var claudeFlagCache sync.Map // command path + its time → map[string]bool

// ClaudeFlags are the options the claude at command knows, read from its
// --help (nil when that can't be read: then nothing is left out).
func ClaudeFlags(command string) map[string]bool {
	key := command
	if st, err := os.Stat(command); err == nil {
		key += "|" + st.ModTime().String()
	}
	if v, ok := claudeFlagCache.Load(key); ok {
		return v.(map[string]bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "--help").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--print") {
		return nil
	}
	flags := map[string]bool{}
	for _, f := range flagPattern.FindAllString(string(out), -1) {
		flags[f] = true
	}
	claudeFlagCache.Store(key, flags)
	return flags
}

// compatArgs keeps the options known is missing out of args, using the
// older way when there is one. It says what it left out.
func compatArgs(args []string, known map[string]bool) (kept, dropped []string) {
	if known == nil {
		return args, nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") || known[a] {
			kept = append(kept, a)
			if claudeValued[a] && i+1 < len(args) {
				kept = append(kept, args[i+1])
				i++
			}
			continue
		}
		value := ""
		if claudeValued[a] && i+1 < len(args) {
			value = args[i+1]
			i++
		}
		dropped = append(dropped, a)
		// --tools "" (no built-in tools): older claudes take a deny list.
		if a == "--tools" && value == "" && known["--disallowedTools"] {
			kept = append(kept, "--disallowedTools", builtinTools)
		}
	}
	return kept, dropped
}

// ClaudeCmd is a claude command line with your provider: the options this
// claude knows, your settings' apiKeyHelper (run by claude, or by tubas
// when this claude has no --settings) and the environment for it.
func ClaudeCmd(ctx context.Context, command string, args []string, p Provider) (*exec.Cmd, error) {
	args = append(append([]string(nil), args...), p.SettingsArgs()...)
	kept, dropped := compatArgs(args, ClaudeFlags(command))
	for _, d := range dropped {
		if d == "--settings" && p.KeyHelper != "" {
			// No --settings here: tubas runs your apiKeyHelper and hands
			// claude the key, as an older claude reads it from the env.
			resolved, err := p.Resolved()
			if err != nil {
				return nil, err
			}
			p = resolved
		}
	}
	cmd := exec.CommandContext(ctx, command, kept...)
	cmd.Env = p.Environ()
	return cmd, nil
}

// MissingFlags says which of the options tubas uses the claude at command
// doesn't know (tubas works around them), for tubas doctor.
func MissingFlags(command string) []string {
	known := ClaudeFlags(command)
	if known == nil {
		return nil
	}
	var missing []string
	for _, f := range []string{"--permission-prompts", "--tools", "--setting-sources", "--settings", "--strict-mcp-config",
		"--disable-slash-commands", "--no-session-persistence", "--include-partial-messages", "--effort", "--json-schema"} {
		if !known[f] {
			missing = append(missing, f)
		}
	}
	return missing
}
