package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── codex ───────────────────────────────────────────────────────────────
//
// CodexEngine runs a turn on OpenAI's Codex through `codex app-server`,
// the JSON-RPC interface tucano and t3code drive: one process per turn
// (tubas turns are stateless), initialize → thread/start → turn/start,
// then notifications until turn/completed.
//
// What the agent may do is decided by tubas, as with claude: codex starts
// with its own shell and extras off and approval_policy never; its tools
// are the tubas gateway (an MCP server); shell commands go through the
// gateway's run_command, which waits for your y. A writer edits files in
// its worktree under codex's workspace-write sandbox; anyone else gets a
// read-only sandbox.

// CodexEngine runs agents with harness: codex.
type CodexEngine struct {
	Command   string // the codex binary; "" finds it (see FindCodex)
	Model     string // overrides every agent's model when set
	WorkDir   string // where codex runs when the agent has no code
	Gateway   GatewayFunc
	Approvals *Broker
	Plan      bool // logged in with ChatGPT: billed to the plan
}

func (c CodexEngine) Name() string { return "codex" }

// codexArgs start the app-server with everything tubas doesn't use off,
// as tucano does.
func codexArgs(sandbox string) []string {
	args := []string{"app-server"}
	for _, f := range []string{"shell_tool", "unified_exec", "apps", "plugins", "browser_use", "computer_use", "in_app_browser",
		"image_generation", "multi_agent", "goals", "view_image", "tool_suggest", "skill_search", "hooks", "memories"} {
		args = append(args, "--disable", f)
	}
	for _, kv := range []string{`sandbox_mode="` + sandbox + `"`, `approval_policy="never"`, "include_permissions_instructions=false",
		"skills.include_instructions=false", "skills.bundled.enabled=false", "project_doc_max_bytes=0",
		`history.persistence="none"`, "mcp_optional_startup_grace_ms=0"} {
		args = append(args, "-c", kv)
	}
	return args
}

type codexEnd struct {
	err error
}

func (c CodexEngine) RunTurn(ctx context.Context, req TurnRequest, out chan<- Event) (Reply, error) {
	a := req.Agent
	command := c.Command
	if command == "" {
		var err error
		if command, err = FindCodex(); err != nil {
			return Reply{}, err
		}
	}
	code, err := codePlaces(req)
	if err != nil {
		return Reply{}, err
	}
	dir, sandbox := c.WorkDir, "read-only"
	if len(code.all) > 0 {
		dir = code.all[0]
	}
	if len(code.write) > 0 {
		sandbox = "workspace-write"
	}
	turn := newTurnID()
	config := map[string]any{"web_search": "disabled"}
	if effort := turnEffort(a.Effort, req.Kind); effort != "" {
		config["model_reasoning_effort"] = codexEffort(effort)
	}
	if c.Gateway != nil {
		if launch, ok := c.Gateway(a, turn, code.write, "codex"); ok {
			config["mcp_servers."+gatewayName] = map[string]any{"command": launch.Command, "args": launch.Args, "env": launch.Env,
				"default_tools_approval_mode": "approve", "tool_timeout_sec": 900}
		}
	}
	if c.Approvals != nil {
		defer c.Approvals.Attach(ctx, turn, out)()
	}

	cmd := exec.Command(command, codexArgs(sandbox)...)
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
		return Reply{}, fmt.Errorf("starting codex: %w", err)
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

	var mu sync.Mutex // the reader goroutine writes these
	var reply Reply
	var lastMessage string
	ended := make(chan codexEnd, 1)
	end := func(err error) {
		select {
		case ended <- codexEnd{err}:
		default:
		}
	}
	emit := func(e Event) { send(ctx, out, e) }
	onNotify := func(method string, params json.RawMessage) {
		var p struct {
			Delta      string         `json:"delta"`
			Item       map[string]any `json:"item"`
			TokenUsage struct {
				Total struct {
					Input  int `json:"inputTokens"`
					Cached int `json:"cachedInputTokens"`
					Output int `json:"outputTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
			Turn struct {
				Status string `json:"status"`
				Error  any    `json:"error"`
			} `json:"turn"`
			Error     any  `json:"error"`
			WillRetry bool `json:"willRetry"`
		}
		json.Unmarshal(params, &p)
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "item/agentMessage/delta":
			emit(TextDelta{a.Name, p.Delta})
		case "item/started":
			if call := codexCall(p.Item); call != "" {
				emit(ToolCalled{a.Name, call})
			}
		case "item/completed":
			switch str(p.Item["type"]) {
			case "agentMessage":
				lastMessage = str(p.Item["text"])
			default:
				if text, failed, ok := codexResult(p.Item); ok {
					emit(ToolResult{Agent: a.Name, Text: text, Failed: failed})
				}
			}
		case "thread/tokenUsage/updated":
			t := p.TokenUsage.Total
			reply.Tokens, reply.Cached = t.Input+t.Output, t.Cached
			if err := overLimit(t.Input, a.TokenLimit); err != nil {
				end(err)
			}
		case "error":
			if !p.WillRetry {
				end(fmt.Errorf("codex: %s", errorText(p.Error)))
			}
		case "turn/completed":
			switch p.Turn.Status {
			case "failed":
				end(fmt.Errorf("codex: %s", errorText(p.Turn.Error)))
			case "interrupted":
				end(context.Canceled)
			default:
				end(nil)
			}
		}
	}
	conn := newRPC(stdout, stdin, false, onNotify, codexDecline)

	if _, err := conn.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "tubas", "title": "tubas", "version": "1"},
		"capabilities": map[string]bool{"experimentalApi": false, "requestAttestation": false},
	}); err != nil {
		return Reply{}, codexErr(err, &stderr)
	}
	conn.notify("initialized", nil)
	model := a.Model
	if c.Model != "" {
		model = c.Model
	}
	start := map[string]any{"cwd": dir, "sandbox": sandbox, "approvalPolicy": "never",
		"developerInstructions": SystemPrompt(a, req.Team) + codexCodePrompt(code), "config": config, "ephemeral": true}
	if !isClaudeModel(model) {
		start["model"] = model // a Claude alias means: codex's default
	}
	res, err := conn.call(ctx, "thread/start", start)
	if err != nil {
		return Reply{}, codexErr(err, &stderr)
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	json.Unmarshal(res, &thread)
	res, err = conn.call(ctx, "turn/start", map[string]any{"threadId": thread.Thread.ID,
		"input": []map[string]any{{"type": "text", "text": req.Prompt, "text_elements": []any{}}}})
	if err != nil {
		return Reply{}, codexErr(err, &stderr)
	}
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	json.Unmarshal(res, &started)

	select {
	case e := <-ended:
		if e.err != nil {
			if errors.Is(e.err, context.Canceled) && ctx.Err() != nil {
				return Reply{}, ctx.Err()
			}
			return Reply{}, e.err
		}
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn.call(stop, "turn/interrupt", map[string]string{"threadId": thread.Thread.ID, "turnId": started.Turn.ID})
		cancel()
		return Reply{}, ctx.Err()
	case <-conn.done:
		return Reply{}, codexErr(errors.New("codex stopped before the turn ended"), &stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	reply.Text, reply.Model, reply.Plan = lastMessage, model, c.Plan
	if isClaudeModel(model) {
		reply.Model = ""
	}
	return reply, nil
}

// gatewayName is the gateway's MCP server name in every harness.
const gatewayName = "tubas"

// codexDecline answers codex's own approval requests: no. Everything the
// agent may do goes through the tubas gateway (tucano does the same).
func codexDecline(method string, _ json.RawMessage) (any, error) {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]string{"decision": "decline"}, nil
	case "applyPatchApproval", "execCommandApproval":
		return map[string]string{"decision": "denied"}, nil
	case "item/tool/requestUserInput":
		return map[string]any{"answers": map[string]any{}}, nil
	case "mcpServer/elicitation/request":
		return map[string]any{"action": "decline", "content": nil, "_meta": nil}, nil
	case "item/tool/call":
		return map[string]any{"success": false, "contentItems": []any{}}, nil
	}
	return nil, fmt.Errorf("%s is not supported", method)
}

// codexCall describes an item that starts work, for the screen.
func codexCall(item map[string]any) string {
	switch str(item["type"]) {
	case "mcpToolCall":
		args, _ := json.Marshal(item["arguments"])
		return describeCall(str(item["tool"]), args)
	case "commandExecution":
		return "shell(" + oneLine(str(item["command"])) + ")"
	case "fileChange":
		return "edit(" + strings.Join(changedPaths(item), ", ") + ")"
	case "webSearch":
		return "web_search()"
	}
	return ""
}

// codexResult is how an item that did work ended.
func codexResult(item map[string]any) (text string, failed, ok bool) {
	status := str(item["status"])
	failed = status == "failed" || status == "declined" || item["error"] != nil
	switch str(item["type"]) {
	case "mcpToolCall":
		if failed {
			return firstLine(errorText(item["error"])), true, true
		}
		return firstLine(contentFrom(item["result"])), false, true
	case "commandExecution":
		return firstLine(str(item["aggregatedOutput"])), failed, true
	case "fileChange":
		return fmt.Sprintf("%d files", len(changedPaths(item))), failed, true
	}
	return "", false, false
}

func changedPaths(item map[string]any) []string {
	var paths []string
	if changes, ok := item["changes"].([]any); ok {
		for _, ch := range changes {
			if m, ok := ch.(map[string]any); ok {
				paths = append(paths, filepath.Base(str(m["path"])))
			}
		}
	}
	return paths
}

// contentFrom reads an MCP result: text blocks, or a plain string.
func contentFrom(v any) string {
	switch r := v.(type) {
	case string:
		return r
	case map[string]any:
		if blocks, ok := r["content"].([]any); ok {
			var parts []string
			for _, b := range blocks {
				if m, ok := b.(map[string]any); ok {
					parts = append(parts, str(m["text"]))
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	data, _ := json.Marshal(v)
	return string(data)
}

func errorText(v any) string {
	switch e := v.(type) {
	case nil:
		return "failed"
	case string:
		return e
	case map[string]any:
		if m := str(e["message"]); m != "" {
			return m
		}
	}
	data, _ := json.Marshal(v)
	return string(data)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func codexErr(err error, stderr *bytes.Buffer) error {
	if tail := lastLine(stderr.String()); tail != "" {
		return fmt.Errorf("codex: %v (%s)", err, tail)
	}
	return fmt.Errorf("codex: %w", err)
}

// codexEffort maps tubas's effort to codex's reasoning effort.
func codexEffort(e string) string {
	switch e {
	case "xhigh", "max":
		return "high"
	}
	return e
}

// codexCodePrompt tells a codex agent where it works on code.
func codexCodePrompt(code codeAccess) string {
	if len(code.all) == 0 {
		return ""
	}
	p := strings.Replace(code.prompt, "with your file tools (Read, Grep, Glob, Edit, Write and a shell)",
		"with your file tools (read, search, apply patches); shell commands go through the run_command tool, and a human approves each one", 1)
	return strings.Replace(p, "with your file tools (Read, Grep, Glob)", "with your file tools (read and search)", 1)
}

// overLimit stops a turn whose context grew past the agent's token limit.
// Every harness calls it.
func overLimit(tokens, limit int) error {
	if limit > 0 && tokens > limit {
		return fmt.Errorf("token limit reached: %d of %d", tokens, limit)
	}
	return nil
}

// FindCodex locates the codex binary: $TUBAS_CODEX, the native binary of
// the npm install (calling it directly avoids the .cmd wrapper on
// Windows), then the PATH.
func FindCodex() (string, error) {
	if p := os.Getenv("TUBAS_CODEX"); p != "" {
		return p, nil
	}
	if appData := os.Getenv("APPDATA"); appData != "" && runtime.GOOS == "windows" {
		matches, _ := filepath.Glob(filepath.Join(appData, "npm", "node_modules", "@openai", "codex", "vendor", "*", "codex", "codex.exe"))
		if len(matches) > 0 {
			return matches[0], nil
		}
	}
	if p, err := exec.LookPath("codex"); err == nil {
		return p, nil
	}
	return "", errors.New("codex is not installed (npm i -g @openai/codex, then codex login)")
}

var loggedInRe, notLoggedRe = regexp.MustCompile(`(?i)logged in`), regexp.MustCompile(`(?i)not logged in`)

// CodexLogin reads `codex login status`, as tucano does: logged in, and
// whether it is a ChatGPT plan (billed to it) or an API key.
func CodexLogin(command string) (loggedIn, plan bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "login", "status").CombinedOutput()
	text := string(out)
	loggedIn = err == nil && loggedInRe.MatchString(text) && !notLoggedRe.MatchString(text)
	return loggedIn, loggedIn && strings.Contains(strings.ToLower(text), "chatgpt")
}

// CodexModels lists the models codex offers (`codex debug models`, or
// its cache), by priority.
func CodexModels(command string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, command, "debug", "models").Output()
	if err != nil {
		home, _ := os.UserHomeDir()
		if data, err = os.ReadFile(filepath.Join(home, ".codex", "models_cache.json")); err != nil {
			return nil
		}
	}
	type model struct {
		Slug       string `json:"slug"`
		Visibility string `json:"visibility"`
		Priority   *int   `json:"priority"`
	}
	var list []model
	var wrapped struct {
		Models []model `json:"models"`
	}
	if json.Unmarshal(data, &wrapped) == nil && len(wrapped.Models) > 0 {
		list = wrapped.Models
	} else {
		json.Unmarshal(data, &list)
	}
	prio := func(m model) int {
		if m.Priority == nil {
			return 999
		}
		return *m.Priority
	}
	sort.SliceStable(list, func(i, j int) bool { return prio(list[i]) < prio(list[j]) })
	var out []string
	for _, m := range list {
		if m.Slug != "" && (m.Visibility == "" || m.Visibility == "list") {
			out = append(out, m.Slug)
		}
	}
	return out
}
