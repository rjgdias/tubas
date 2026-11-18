package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// What an agent works on, and small text helpers the loop shares.

// codeAccess is where an agent works on code this turn.
type codeAccess struct {
	write  []string          // worktrees it edits
	all    []string          // every code folder, the first one is where claude runs
	names  map[string]string // folder → its repository's name
	prompt string            // what the agent is told about them
}

// codePlaces finds the agent's repositories: a worktree for each one it
// writes (made on first use), your checkout for each one it only reads.
func codePlaces(req TurnRequest) (codeAccess, error) {
	var ca codeAccess
	var names []string
	for name := range req.Repos {
		if access := req.Agent.Tools[name]; access == "read" || access == "write" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ca, nil
	}
	sort.Strings(names)
	ca.names = map[string]string{}
	var lines, readDirs []string
	for _, name := range names {
		if req.Agent.Tools[name] == "write" && req.Workspace != nil {
			wt, err := req.Workspace(name)
			if err != nil {
				return ca, fmt.Errorf("preparing %s: %w", name, err)
			}
			ca.write = append(ca.write, wt)
			ca.names[wt] = name
			lines = append(lines, fmt.Sprintf("- %s: your own worktree at %s. Edit files there freely: a human reviews the whole diff at the end. "+
				"Every shell command waits for a human, so run only what you need (tests, linters, a build). "+
				"Never commit, push, switch branches or touch files outside it.", name, wt))
			continue
		}
		readDirs = append(readDirs, req.Repos[name])
		ca.names[req.Repos[name]] = name
		lines = append(lines, fmt.Sprintf("- %s (read only): %s. Read and search it; never change it.", name, req.Repos[name]))
	}
	ca.all = append(append([]string{}, ca.write...), readDirs...)
	ca.prompt = "\n\n## Code\n\nYou work on these repositories with your file tools (Read, Grep, Glob" +
		map[bool]string{true: ", Edit, Write and a shell", false: ""}[len(ca.write) > 0] + "):\n" + strings.Join(lines, "\n") + "\n"
	return ca, nil
}

// turnEffort is how hard the model thinks on this turn. Output is the most
// expensive kind of token, and thinking is output: agents working in
// parallel (or deciding to pass) think little unless their file says
// otherwise; everyone else uses the agent's setting, or the model's default.
func turnEffort(agentEffort string, kind TurnKind) string {
	if agentEffort != "" {
		return agentEffort
	}
	if kind == KindWorker || kind == KindTeam {
		return "low"
	}
	return ""
}

// describeCall turns a tool call into "run_sql(SELECT ...)".
func describeCall(name string, input json.RawMessage) string {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:] // mcp__tubas__run_sql -> run_sql
	}
	var args map[string]any
	if json.Unmarshal(input, &args) != nil || len(args) == 0 {
		return name + "()"
	}
	if q, ok := args["query"].(string); ok {
		return name + "(" + strings.Join(strings.Fields(q), " ") + ")"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := fmt.Sprint(args[k])
		if len(v) > 40 {
			v = v[:40] + "…"
		}
		parts = append(parts, strings.Join(strings.Fields(v), " "))
	}
	return name + "(" + strings.Join(parts, ", ") + ")"
}

// contentText reads a tool result's content, which is either a string or
// a list of {"type":"text","text":...} blocks.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, len(blocks))
		for i, b := range blocks {
			parts[i] = b.Text
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
