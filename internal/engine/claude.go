package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tubas/internal/agent"
)

// ClaudeEngine runs each agent turn as one `claude -p` process.
//
// Claude Code is the harness: it runs the agent loop (model, tools,
// retries). tubas is the orchestrator: it decides who runs, with which
// instructions and tools, and shows what happens. They talk through the
// process: the prompt goes in on stdin, and events come out on stdout as
// one JSON object per line (--output-format stream-json).
type ClaudeEngine struct {
	Command string // path to the claude binary; "" finds it (see FindClaude)
	Model   string // overrides every agent's model when set (e.g. "haiku")
	WorkDir string // where claude runs

	// Tools, when set, gives an agent its tools for one turn: an MCP
	// config file and the tool names claude may use without asking.
	// turn identifies this one turn, so its approvals find their way back
	// even when the same agent works in two sessions at once.
	// writeDirs are the worktrees the agent may edit this turn: the
	// gateway answers claude's permission prompts with them.
	Tools func(a agent.Agent, turn string, writeDirs []string) (mcpConfig string, allowed []string, cleanup func(), err error)

	// Approvals, when set, routes approval requests from the agent's
	// tools to this turn's event channel.
	Approvals *Broker

	// Provider is how agents reach the model when your Claude settings
	// name one (a work gateway, Bedrock, Vertex). See FindProvider.
	Provider Provider

	// Providers are the model providers an agent may name (provider:
	// deepseek…); DefaultProvider is the team's, for agents that name
	// none. Plan says your Claude login is a subscription.
	Providers       map[string]ProviderSpec
	DefaultProvider string
	Plan            bool
}

// providerFor is how this agent reaches its model: your Claude setup, or
// another provider's Anthropic-compatible endpoint. It returns the model
// to ask for and whether the turn is billed to your plan.
func (c ClaudeEngine) providerFor(a agent.Agent) (Provider, string, bool, error) {
	name := a.Provider
	if name == "" {
		name = c.DefaultProvider
	}
	if name == "" || name == Anthropic {
		model := a.Model
		if c.Model != "" {
			model = c.Model
		}
		return c.Provider, model, c.Plan, nil
	}
	spec, ok := c.Providers[name]
	if !ok {
		return Provider{}, "", false, fmt.Errorf("%s: no provider named %q (built in: %s; or add it under providers: in team.yaml)",
			a.Name, name, strings.Join(ProviderNames(c.Providers)[1:], ", "))
	}
	model := a.Model
	if isClaudeModel(model) {
		model = "" // a Claude alias means: this provider's default
	}
	p, err := spec.ClaudeProvider(model)
	if err != nil {
		return Provider{}, "", false, fmt.Errorf("%s: %w", a.Name, err)
	}
	return c.Provider.With(p), p.Env["ANTHROPIC_MODEL"], false, nil
}

// isClaudeModel says whether a model name is one of Claude's.
func isClaudeModel(m string) bool {
	return m == "" || m == "haiku" || m == "sonnet" || m == "opus" || strings.HasPrefix(m, "claude")
}

// Name describes the engine, for the header: "claude", or
// "claude · gateway llm.example.com" when your settings name a provider,
// or "claude · deepseek" when the team's provider is another one.
func (c ClaudeEngine) Name() string {
	if c.DefaultProvider != "" && c.DefaultProvider != Anthropic {
		return "claude · " + c.DefaultProvider
	}
	if label := c.Provider.Label(); label != "" {
		return "claude · " + label
	}
	return "claude"
}

func (c ClaudeEngine) RunTurn(ctx context.Context, req TurnRequest, out chan<- Event) (Reply, error) {
	a := req.Agent
	command := c.Command
	if command == "" {
		var err error
		if command, err = FindClaude(); err != nil {
			return Reply{}, err
		}
	}
	provider, model, plan, err := c.providerFor(a)
	if err != nil {
		return Reply{}, err
	}

	args := []string{
		"-p",
		"--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--model", model,
		"--system-prompt", SystemPrompt(a, req.Team),
		"--tools", "", // no built-in tools: no shell, no file edits
		"--strict-mcp-config",          // ignore MCP servers configured elsewhere
		"--permission-prompts", "none", // anything not allowed below is denied
		"--no-session-persistence",
		// Skip your settings, CLAUDE.md, hooks and skills: the agent gets
		// only what tubas gives it. (--safe-mode would also drop our MCP.)
		"--setting-sources", "",
		"--disable-slash-commands",
	}
	if effort := turnEffort(a.Effort, req.Kind); effort != "" {
		args = append(args, "--effort", effort)
	}
	dir := c.WorkDir
	code, err := codePlaces(req)
	if err != nil {
		return Reply{}, err
	}
	if len(code.all) > 0 {
		// Code access: claude's own file tools, in the repository folders
		// only. Reading needs no one; edits and commands become permission
		// prompts that the gateway answers (see mcp.permissionTool).
		tools := "Read,Grep,Glob"
		if len(code.write) > 0 {
			tools += ",Edit,Write,Bash,PowerShell" // the shell is PowerShell on Windows
			setArg(args, "--permission-prompts", "host")
			args = append(args, "--permission-prompt-tool", "mcp__tubas__permission")
		}
		setArg(args, "--tools", tools)
		setArg(args, "--system-prompt", SystemPrompt(a, req.Team)+code.prompt)
		dir = code.all[0]
		for _, d := range code.all[1:] {
			args = append(args, "--add-dir", d)
		}
	}
	turn := newTurnID()
	if c.Tools != nil {
		config, allowed, cleanup, err := c.Tools(a, turn, code.write)
		if err != nil {
			return Reply{}, fmt.Errorf("preparing tools: %w", err)
		}
		defer cleanup()
		if config != "" {
			args = append(args, "--mcp-config", config)
		}
		if len(allowed) > 0 {
			args = append(args, "--allowedTools", strings.Join(allowed, ","))
		}
	}
	if c.Approvals != nil {
		defer c.Approvals.Attach(ctx, turn, out)()
	}

	// CommandContext kills the process if ctx is cancelled (esc, r, q).
	// Only the options this claude knows (an older one stops at the first
	// it doesn't), with your apiKeyHelper and your provider's environment.
	cmd, err := ClaudeCmd(ctx, command, args, provider)
	if err != nil {
		return Reply{}, err
	}
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(req.Prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Reply{}, err
	}
	if err := cmd.Start(); err != nil {
		return Reply{}, fmt.Errorf("starting claude: %w", err)
	}

	reply, st, parseErr := readStream(stdout, a.Name, a.TokenLimit, func(e Event) error { return send(ctx, out, e) })
	if parseErr != nil && parseErr != errNoResult {
		_ = cmd.Process.Kill() // e.g. over the token limit: stop it now
	}
	waitErr := cmd.Wait()

	switch {
	case ctx.Err() != nil:
		return Reply{}, ctx.Err()
	case parseErr == errKeyRefused:
		return Reply{}, keyRefused(provider, st.retry)
	case parseErr == errNoResult:
		return Reply{}, stoppedEarly(waitErr, stderr.String(), st)
	case parseErr != nil:
		return Reply{}, parseErr
	case waitErr != nil:
		return Reply{}, explain(fmt.Sprintf("claude exited: %v %s", waitErr, lastLine(stderr.String())))
	}
	reply.Model, reply.Plan = model, plan
	if !isClaudeModel(model) {
		reply.Cost = 0 // claude prices only its own models; another provider's comes from the api catalog
	}
	return reply, nil
}

// codeAccess is where an agent works on code this turn.
type codeAccess struct {
	write  []string // worktrees it edits
	all    []string // every code folder, the first one is where claude runs
	prompt string   // what the agent is told about them
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
	var lines, readDirs []string
	for _, name := range names {
		if req.Agent.Tools[name] == "write" && req.Workspace != nil {
			wt, err := req.Workspace(name)
			if err != nil {
				return ca, fmt.Errorf("preparing %s: %w", name, err)
			}
			ca.write = append(ca.write, wt)
			lines = append(lines, fmt.Sprintf("- %s: your own worktree at %s. Edit files there freely: a human reviews the whole diff at the end. "+
				"Every shell command waits for a human, so run only what you need (tests, linters, a build). "+
				"Never commit, push, switch branches or touch files outside it.", name, wt))
			continue
		}
		readDirs = append(readDirs, req.Repos[name])
		lines = append(lines, fmt.Sprintf("- %s (read only): %s. Read and search it; never change it.", name, req.Repos[name]))
	}
	ca.all = append(append([]string{}, ca.write...), readDirs...)
	ca.prompt = "\n\n## Code\n\nYou work on these repositories with your file tools (Read, Grep, Glob" +
		map[bool]string{true: ", Edit, Write and a shell", false: ""}[len(ca.write) > 0] + "):\n" + strings.Join(lines, "\n") + "\n"
	return ca, nil
}

// setArg replaces the value after a flag.
func setArg(args []string, flag, value string) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			args[i+1] = value
			return
		}
	}
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

// FindClaude locates the claude binary: $TUBAS_CLAUDE, then the npm
// install's native binary (calling it directly avoids the .cmd wrapper,
// which mangles arguments on Windows), then the PATH.
func FindClaude() (string, error) {
	if p := os.Getenv("TUBAS_CLAUDE"); p != "" {
		return p, nil
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		p := filepath.Join(appData, "npm", "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	p, err := exec.LookPath("claude")
	if err != nil {
		return "", errors.New("claude not found: install Claude Code or set TUBAS_CLAUDE")
	}
	return p, nil
}

// ── reading the stream ──────────────────────────────────────────────────

// streamLine holds the fields tubas reads from each line of
// --output-format stream-json. Fields that a line does not have stay
// empty: one struct can decode every kind of line.
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	// type "stream_event": a raw API event, used here for text deltas.
	Event *struct {
		Type  string `json:"type"`
		Delta *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`

	// type "assistant" or "user": a whole message.
	Message *struct {
		Content []block `json:"content"`
		Usage   *usage  `json:"usage"`
	} `json:"message"`

	// type "result": the end of the turn.
	Result    string  `json:"result"`
	IsError   bool    `json:"is_error"`
	Usage     *usage  `json:"usage"`
	TotalCost float64 `json:"total_cost_usd"`

	// type "system", subtype "api_retry": a request failed, claude tries
	// again (up to max_retries, waiting longer each time).
	Attempt      int     `json:"attempt"`
	MaxRetries   int     `json:"max_retries"`
	RetryDelayMs float64 `json:"retry_delay_ms"`
	ErrorStatus  *int    `json:"error_status"`
	Error        string  `json:"error"`
}

// streamState is what reading the stream remembers for when it goes
// wrong: the last retry, and lines that weren't claude's JSON.
type streamState struct {
	retry *Retrying
	noise []string
}

// errKeyRefused stops a turn whose key the provider refused twice:
// claude would keep trying for minutes, with the same key.
var errKeyRefused = errors.New("key refused")

type block struct {
	Type      string          `json:"type"` // text, tool_use, tool_result
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"` // string or list of text blocks
	IsError   bool            `json:"is_error"`
	ToolUseID string          `json:"tool_use_id"`
}

type usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
}

func (u usage) total() int {
	return u.InputTokens + u.OutputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

// parseStream turns claude's stream into tubas events and returns the
// final answer. limit, when above 0, stops the turn once the context
// grows past that many tokens.
func parseStream(r io.Reader, agentName string, limit int, emit func(Event) error) (Reply, error) {
	reply, _, err := readStream(r, agentName, limit, emit)
	return reply, err
}

// errNoResult: the stream ended before claude said how the turn went.
// RunTurn turns it into what claude printed and why it stopped.
var errNoResult = errors.New("claude stopped before answering")

func readStream(r io.Reader, agentName string, limit int, emit func(Event) error) (Reply, streamState, error) {
	reader := bufio.NewReader(r)
	var reply Reply
	var st streamState
	gotResult := false
	for {
		line, readErr := reader.ReadBytes('\n') // lines can be long: no fixed buffer
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var l streamLine
			if err := json.Unmarshal(trimmed, &l); err != nil {
				// A warning printed on stdout (a proxy, Node): keep it for
				// the error, and read on.
				if len(st.noise) < 5 {
					st.noise = append(st.noise, firstLine(string(trimmed)))
				}
			} else {
				done, err := handleLine(l, agentName, limit, emit, &reply, &st)
				if err != nil {
					return reply, st, err
				}
				gotResult = gotResult || done
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return reply, st, readErr
		}
	}
	if !gotResult {
		return reply, st, errNoResult
	}
	return reply, st, nil
}

func handleLine(l streamLine, agentName string, limit int, emit func(Event) error, reply *Reply, st *streamState) (bool, error) {
	switch l.Type {
	case "system":
		if l.Subtype != "api_retry" {
			return false, nil
		}
		r := Retrying{Agent: agentName, Attempt: l.Attempt, Max: l.MaxRetries, Reason: l.Error,
			Delay: time.Duration(l.RetryDelayMs) * time.Millisecond}
		if l.ErrorStatus != nil {
			r.Status = *l.ErrorStatus
		}
		st.retry = &r
		// A refused key stays refused: claude already ran your apiKeyHelper
		// again after the first 401, so stop instead of minutes of retries.
		if (r.Status == 401 || r.Status == 403) && r.Attempt >= 2 {
			return true, errKeyRefused
		}
		return false, emit(r)
	case "stream_event":
		if l.Event != nil && l.Event.Type == "content_block_delta" &&
			l.Event.Delta != nil && l.Event.Delta.Type == "text_delta" {
			return false, emit(TextDelta{agentName, l.Event.Delta.Text})
		}

	case "assistant":
		if l.Message == nil {
			return false, nil
		}
		for _, b := range l.Message.Content {
			if b.Type == "tool_use" {
				if err := emit(ToolCalled{agentName, describeCall(b.Name, b.Input)}); err != nil {
					return false, err
				}
			}
		}
		if u := l.Message.Usage; u != nil && limit > 0 && u.total() > limit {
			return false, fmt.Errorf("token limit reached: %d of %d", u.total(), limit)
		}

	case "user":
		if l.Message == nil {
			return false, nil
		}
		for _, b := range l.Message.Content {
			if b.Type == "tool_result" {
				text := contentText(b.Content)
				ev := ToolResult{Agent: agentName, Text: firstLine(text), Failed: b.IsError}
				// Advice from a stronger model (escalation) comes as a line
				// starting with ↑: keep it, so the screen can show it.
				if i := strings.Index(text, "\n↑ "); i >= 0 {
					ev.Text += "\n" + strings.TrimSpace(text[i+1:])
				}
				if err := emit(ev); err != nil {
					return false, err
				}
			}
		}

	case "result":
		if l.IsError || (l.Subtype != "" && l.Subtype != "success") {
			return true, explain(firstLine(l.Result))
		}
		reply.Text = l.Result
		if l.Usage != nil {
			reply.Tokens = l.Usage.total()
			reply.Cached = l.Usage.CacheReadTokens
		}
		reply.Cost = l.TotalCost
		return true, nil
	}
	return false, nil
}

// keyRefused says what to check when the provider refuses the key.
func keyRefused(p Provider, r *Retrying) error {
	status := 401
	if r != nil {
		status = r.Status
	}
	msg := fmt.Sprintf("the provider refused the key (%d), twice", status)
	switch {
	case p.KeyHelper != "":
		msg += ": your apiKeyHelper (in " + strings.Join(p.Sources, ", ") + ") did not give a valid token. " +
			"Run it in a terminal to see what it prints: " + p.KeyHelper
	case p.Get("ANTHROPIC_BASE_URL") != "":
		msg += ": check ANTHROPIC_AUTH_TOKEN or ANTHROPIC_API_KEY in your Claude settings"
	default:
		msg += ": run claude and type /login"
	}
	return errors.New(msg + " · tubas doctor checks it step by step")
}

// stoppedEarly says why claude stopped before answering: the provider
// problem it last retried, what it printed, how it exited.
func stoppedEarly(waitErr error, stderr string, st streamState) error {
	var parts []string
	if st.retry != nil {
		parts = append(parts, st.retry.Problem())
	}
	if tail := lastLines(stderr, 3); tail != "" {
		parts = append(parts, tail)
	}
	parts = append(parts, st.noise...)
	if waitErr != nil {
		parts = append(parts, "claude exited: "+waitErr.Error())
	}
	if len(parts) == 0 {
		parts = append(parts, "it printed nothing")
	}
	return explain("stopped before answering: " + strings.Join(parts, " · ") + " · tubas doctor checks your setup step by step")
}

// lastLines is the end of what a program printed, on one line.
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " · ")
}

// explain turns what claude says when a turn fails into what to do.
func explain(msg string) error {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "not logged in") || strings.Contains(low, "/login"):
		return errors.New("claude is not signed in: run claude and type /login, " +
			"or put your company gateway in ~/.claude/settings.json (env: ANTHROPIC_BASE_URL and a token or apiKeyHelper)")
	case strings.Contains(low, "401") || strings.Contains(low, "authentication") || strings.Contains(low, "invalid api key"):
		return errors.New("the provider refused the key (" + strings.TrimSpace(msg) + "): check the token or apiKeyHelper in your Claude settings")
	case strings.Contains(low, "model") && (strings.Contains(low, "not found") || strings.Contains(low, "404") || strings.Contains(low, "invalid")):
		return errors.New("this model is not available here (" + strings.TrimSpace(msg) + "): /model picks one your provider serves")
	case strings.Contains(low, "credit balance") || strings.Contains(low, "usage limit") || strings.Contains(low, "rate limit") || strings.Contains(low, "429"):
		return errors.New("the provider is limiting use right now (" + strings.TrimSpace(msg) + "): wait a little and ask again")
	}
	return errors.New("claude: " + strings.TrimSpace(msg))
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
