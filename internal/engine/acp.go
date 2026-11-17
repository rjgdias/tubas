package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── ACP: every other agent CLI ──────────────────────────────────────────
//
// The Agent Client Protocol (agentclientprotocol.com) is how editors like
// Zed talk to agents: opencode, gemini, goose, qwen and many more speak
// it, and adapters bring claude and codex too. t3code runs its agents
// this way, and so does tubas for every harness without a driver of its
// own: one process per turn, initialize → session/new (with the tubas
// gateway as its MCP server) → session/prompt, then session/update
// notifications until the prompt returns.
//
// ACP lets tubas answer the agent's permission requests: reading is
// allowed, editing inside the worktree is allowed, a shell command waits
// for your y, anything else is refused. Never "always".

// ACPAgent is an agent CLI that speaks ACP.
type ACPAgent struct {
	Name    string
	Command []string // the program and the arguments that start ACP mode
}

// KnownACP are the agents tubas tries out of the box, when installed.
// team.yaml's harnesses: adds or replaces any.
var KnownACP = []ACPAgent{
	{"opencode", []string{"opencode", "acp"}},
	{"gemini", []string{"gemini", "--experimental-acp"}},
	{"goose", []string{"goose", "acp"}},
	{"qwen", []string{"qwen", "--experimental-acp"}},
	{"kimi", []string{"kimi", "--acp"}},
	{"cursor", []string{"cursor-agent", "acp"}},
	{"copilot", []string{"copilot", "--acp"}},
	{"claude-acp", []string{"claude-agent-acp"}},
	{"codex-acp", []string{"codex-acp"}},
}

// ACPEngine runs agents through ACP.
type ACPEngine struct {
	Agent     ACPAgent
	WorkDir   string
	Gateway   GatewayFunc
	Approvals *Broker
}

func (e ACPEngine) Name() string { return e.Agent.Name }

func (e ACPEngine) RunTurn(ctx context.Context, req TurnRequest, out chan<- Event) (Reply, error) {
	a := req.Agent
	code, err := codePlaces(req)
	if err != nil {
		return Reply{}, err
	}
	dir := e.WorkDir
	if len(code.all) > 0 {
		dir = code.all[0]
	}
	turn := newTurnID()
	servers := []map[string]any{}
	if e.Gateway != nil {
		if launch, ok := e.Gateway(a, turn, code.write, "acp"); ok {
			env := []map[string]string{}
			keys := make([]string, 0, len(launch.Env))
			for k := range launch.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				env = append(env, map[string]string{"name": k, "value": launch.Env[k]})
			}
			servers = append(servers, map[string]any{"name": gatewayName, "command": launch.Command, "args": launch.Args, "env": env})
		}
	}
	if e.Approvals != nil {
		defer e.Approvals.Attach(ctx, turn, out)()
	}
	if len(e.Agent.Command) == 0 {
		return Reply{}, fmt.Errorf("%s has no command", e.Agent.Name)
	}
	cmd := exec.Command(e.Agent.Command[0], e.Agent.Command[1:]...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Reply{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Reply{}, err
	}
	if err := cmd.Start(); err != nil {
		return Reply{}, fmt.Errorf("starting %s: %w", e.Agent.Name, err)
	}
	defer func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}()

	var mu sync.Mutex
	var text strings.Builder
	var reply Reply
	titles := map[string]string{} // tool call id -> what it does, for its result
	emit := func(ev Event) { send(ctx, out, ev) }
	onNotify := func(method string, params json.RawMessage) {
		if method != "session/update" {
			return
		}
		var p struct {
			Update map[string]any `json:"update"`
		}
		json.Unmarshal(params, &p)
		u := p.Update
		mu.Lock()
		defer mu.Unlock()
		switch str(u["sessionUpdate"]) {
		case "agent_message_chunk":
			if c, ok := u["content"].(map[string]any); ok {
				chunk := str(c["text"])
				text.WriteString(chunk)
				emit(TextDelta{a.Name, chunk})
			}
		case "tool_call":
			call := acpCall(u)
			titles[str(u["toolCallId"])] = call
			emit(ToolCalled{a.Name, call})
			if st := str(u["status"]); st == "completed" || st == "failed" {
				emit(ToolResult{Agent: a.Name, Text: firstLine(acpContent(u)), Failed: st == "failed"})
			}
		case "tool_call_update":
			if st := str(u["status"]); st == "completed" || st == "failed" {
				emit(ToolResult{Agent: a.Name, Text: firstLine(acpContent(u)), Failed: st == "failed"})
			}
		case "usage_update":
			if n, ok := u["used"].(float64); ok {
				reply.Tokens = int(n)
			}
		}
	}
	rules := acpRules{read: append(append([]string{}, code.all...), dir), write: code.write}
	onRequest := func(method string, params json.RawMessage) (any, error) {
		if method != "session/request_permission" {
			return nil, fmt.Errorf("%s is not supported", method)
		}
		return e.permission(ctx, a.Name, rules, params, out), nil
	}
	conn := newRPC(stdout, stdin, true, onNotify, onRequest)

	fail := func(err error) error {
		if tail := lastLine(stderr.String()); tail != "" {
			return fmt.Errorf("%s: %v (%s)", e.Agent.Name, err, tail)
		}
		return fmt.Errorf("%s: %w", e.Agent.Name, err)
	}
	if _, err := conn.call(ctx, "initialize", map[string]any{"protocolVersion": 1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]string{"name": "tubas", "version": "1"}}); err != nil {
		return Reply{}, fail(err)
	}
	res, err := conn.call(ctx, "session/new", map[string]any{"cwd": dir, "mcpServers": servers})
	if err != nil {
		return Reply{}, fail(err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(res, &session)
	// ACP has no system prompt: the agent's instructions come first.
	prompt := SystemPrompt(a, req.Team) + acpCodePrompt(code) + "\n\n---\n\n" + req.Prompt
	done := make(chan error, 1)
	var stop struct {
		StopReason string `json:"stopReason"`
	}
	go func() {
		res, err := conn.call(context.Background(), "session/prompt", map[string]any{"sessionId": session.SessionID,
			"prompt": []map[string]string{{"type": "text", "text": prompt}}})
		if err == nil {
			json.Unmarshal(res, &stop)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return Reply{}, fail(err)
		}
	case <-ctx.Done():
		conn.notify("session/cancel", map[string]string{"sessionId": session.SessionID})
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		return Reply{}, ctx.Err()
	case <-conn.done:
		return Reply{}, fail(errors.New("stopped before answering"))
	}
	switch stop.StopReason {
	case "refusal":
		return Reply{}, fail(errors.New("the model refused"))
	case "cancelled":
		return Reply{}, context.Canceled
	}
	mu.Lock()
	defer mu.Unlock()
	reply.Text = text.String()
	if err := overLimit(reply.Tokens, a.TokenLimit); err != nil {
		return reply, err
	}
	return reply, nil
}

// acpRules are the folders an ACP agent may read and change.
type acpRules struct{ read, write []string }

// permission answers one session/request_permission by tubas's rules.
func (e ACPEngine) permission(ctx context.Context, agentName string, rules acpRules, params json.RawMessage, out chan<- Event) any {
	var p struct {
		ToolCall map[string]any `json:"toolCall"`
		Options  []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	json.Unmarshal(params, &p)
	pick := func(allow bool) any {
		want := "reject_once"
		if allow {
			want = "allow_once" // never "always": each one asks again
		}
		for _, o := range p.Options {
			if o.Kind == want {
				return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": o.OptionID}}
			}
		}
		if !allow {
			for _, o := range p.Options {
				if strings.HasPrefix(o.Kind, "reject") {
					return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": o.OptionID}}
				}
			}
		}
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
	}
	tc := p.ToolCall
	paths := acpPaths(tc)
	inside := func(dirs []string) bool {
		for _, path := range paths {
			if !within(path, dirs) {
				return false
			}
		}
		return true
	}
	switch str(tc["kind"]) {
	case "read", "search", "think":
		return pick(inside(rules.read))
	case "edit", "delete", "move":
		return pick(len(paths) > 0 && len(rules.write) > 0 && inside(rules.write))
	case "execute":
		if len(rules.write) == 0 {
			return pick(false) // only agents that write code run commands
		}
		reply := make(chan bool, 1)
		detail := "$ " + acpCommand(tc) + "\nwhere: " + rules.write[0]
		if send(ctx, out, ApprovalRequested{Agent: agentName, Call: "shell(" + clipStr(oneLine(acpCommand(tc)), 60) + ")", Reply: reply, Detail: detail}) != nil {
			return pick(false)
		}
		select {
		case ok := <-reply:
			return pick(ok)
		case <-ctx.Done():
			return pick(false)
		}
	}
	return pick(false)
}

func acpCall(u map[string]any) string {
	title := str(u["title"])
	if title == "" {
		title = str(u["kind"])
	}
	return oneLine(title)
}

// acpContent is the text of a tool call's result.
func acpContent(u map[string]any) string {
	if list, ok := u["content"].([]any); ok {
		var parts []string
		for _, item := range list {
			m, _ := item.(map[string]any)
			if c, ok := m["content"].(map[string]any); ok {
				parts = append(parts, str(c["text"]))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	if raw, ok := u["rawOutput"]; ok {
		return contentFrom(raw)
	}
	return str(u["status"])
}

func acpPaths(tc map[string]any) []string {
	var paths []string
	if locs, ok := tc["locations"].([]any); ok {
		for _, l := range locs {
			if m, ok := l.(map[string]any); ok && str(m["path"]) != "" {
				paths = append(paths, str(m["path"]))
			}
		}
	}
	if in, ok := tc["rawInput"].(map[string]any); ok {
		for _, k := range []string{"path", "file_path", "filePath"} {
			if p := str(in[k]); p != "" {
				paths = append(paths, p)
			}
		}
	}
	return paths
}

func acpCommand(tc map[string]any) string {
	if in, ok := tc["rawInput"].(map[string]any); ok {
		if c := str(in["command"]); c != "" {
			return c
		}
		if list, ok := in["command"].([]any); ok {
			var parts []string
			for _, p := range list {
				parts = append(parts, fmt.Sprint(p))
			}
			return strings.Join(parts, " ")
		}
	}
	return str(tc["title"])
}

// within says whether path is inside one of dirs.
func within(path string, dirs []string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, d := range dirs {
		base, err := filepath.Abs(d)
		if err != nil || d == "" {
			continue
		}
		rel, err := filepath.Rel(base, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func clipStr(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// acpCodePrompt tells an ACP agent where it works on code.
func acpCodePrompt(code codeAccess) string {
	if len(code.all) == 0 {
		return ""
	}
	return strings.Replace(code.prompt, "(Read, Grep, Glob, Edit, Write and a shell)", "(read, search, edit, and a shell whose every command a human approves)", 1)
}

// FindACP finds the ACP agents installed here: the known ones whose
// program is on the PATH, plus team.yaml's.
func FindACP(team map[string]string) []ACPAgent {
	var found []ACPAgent
	for _, a := range KnownACP {
		if _, overridden := team[a.Name]; overridden {
			continue
		}
		if path, err := exec.LookPath(a.Command[0]); err == nil {
			found = append(found, ACPAgent{a.Name, append([]string{path}, a.Command[1:]...)})
		}
	}
	names := make([]string, 0, len(team))
	for n := range team {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if words := strings.Fields(team[n]); len(words) > 0 {
			if path, err := exec.LookPath(words[0]); err == nil {
				words[0] = path
			}
			found = append(found, ACPAgent{n, words})
		}
	}
	return found
}
