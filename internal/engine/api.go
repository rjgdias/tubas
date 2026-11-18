package engine

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"tubas/internal/agent"
	"tubas/internal/auth"
	"tubas/internal/llm"
	"tubas/internal/thread"
	"tubas/internal/tools"
)

// ── the native harness: tubas's own agent loop ──────────────────────────
//
// tubas runs the loop itself: send the conversation and the tools, run
// the tools the model calls, send the results back, until it answers.
// internal/llm speaks the two wire formats (Anthropic's Messages API and
// OpenAI's chat completions), streaming, retrying and saying so.
//
// Its own tools (files, search, the shell, the checklist, the web) run
// in the turn (internal/tools); the team's connectors come through the
// tubas gateway, an MCP server started for the turn.

// Price is what a model costs, in US$ per million tokens.
type Price struct {
	Input, Cached, Output float64
}

// NativeEngine runs every agent: tubas is its own harness.
type NativeEngine struct {
	// Setup is your Claude setup (FindProvider): the provider "anthropic"
	// goes where it points, a company gateway included, with its token,
	// apiKeyHelper, headers and model names.
	Setup           Provider
	Providers       map[string]ProviderSpec
	DefaultProvider string
	WorkDir         string
	Gateway         GatewayFunc
	Approvals       *Broker
	Prices          map[string]Price // by model; a model without one shows tokens, no cost
	AllowCommands   []string         // command prefixes that run without your yes (team.yaml allow_commands)
	HTTP            *http.Client
	MaxSteps        int    // model calls per turn (0 = 60, as tucano)
	Model           string // every agent answers with this model (tubas -model)
}

func (e NativeEngine) Name() string {
	if label := e.Setup.Label(); label != "" {
		return "native · " + label
	}
	return "native"
}

// setupSpec is the provider "anthropic": where your
// Claude settings point (a gateway), else Anthropic's API with a key.
func (e NativeEngine) setupSpec() (ProviderSpec, string, map[string]string, error) {
	setup, err := e.Setup.Resolved()
	if err != nil {
		return ProviderSpec{}, "", nil, err
	}
	for _, v := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if truthy(setup.Get(v)) {
			return ProviderSpec{}, "", nil, errors.New("your Claude settings use " + e.Setup.Label() + ", which tubas doesn't reach: /connect a provider (an Anthropic key, or another)")
		}
	}
	spec := anthropicAPI
	if base := setup.Get("ANTHROPIC_BASE_URL"); base != "" {
		spec.Anthropic = base
	}
	key := setup.Get("ANTHROPIC_AUTH_TOKEN")
	if key == "" {
		key = setup.Get("ANTHROPIC_API_KEY")
	}
	if key == "" && spec.Anthropic == anthropicAPI.Anthropic {
		key = auth.Get(Anthropic) // the key you saved with /connect
	}
	if key == "" {
		return ProviderSpec{}, "", nil, errors.New("no key for Anthropic: /connect saves one (or set ANTHROPIC_API_KEY, or a token or apiKeyHelper in your Claude settings)")
	}
	return spec, key, setup.Headers(), nil
}

// setupModel maps claude's short names to the ones your setup asks for
// (a gateway often names models its own way), as Claude Code does.
func (e NativeEngine) setupModel(model string) string {
	if model == "" {
		model = "sonnet"
	}
	if short, ok := claudeAliases[model]; ok {
		if m := e.Setup.Get("ANTHROPIC_DEFAULT_" + strings.ToUpper(model) + "_MODEL"); m != "" {
			return m
		}
		if m := e.Setup.Get("ANTHROPIC_MODEL"); m != "" {
			return m
		}
		return short
	}
	return model
}

// anthropicAPI is the provider "anthropic": Anthropic's
// own API, with an API key (a Claude plan works only through claude).
var anthropicAPI = ProviderSpec{Name: Anthropic, Anthropic: "https://api.anthropic.com", KeyEnv: "ANTHROPIC_API_KEY",
	Models: []string{"claude-sonnet-5-5", "claude-haiku-5-5"}}

// claudeAliases are the ids behind claude's short model names.
var claudeAliases = llm.Aliases

// apiTool is a tool as the gateway lists it.
type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"inputSchema"`
}

// apiCall is a tool call the model made.
type apiCall struct {
	ID, Name string
	Args     json.RawMessage
}

// clientFor is the wire format, model and provider an agent runs on.
// like is the Claude model a gateway's own name stands for (sonnet →
// acme-claude-sonnet), so the request asks what that model accepts.
func (e NativeEngine) clientFor(a agent.Agent) (client llm.Client, model, like string, setup bool, err error) {
	name := a.Provider
	if name == "" {
		name = e.DefaultProvider
	}
	if name == "" || name == Anthropic {
		spec, key, headers, err := e.setupSpec()
		if err != nil {
			return nil, "", "", true, err
		}
		model = e.setupModel(a.Model)
		like = a.Model
		if like == "" {
			like = "sonnet"
		}
		if _, alias := llm.Aliases[like]; !alias {
			like = ""
		}
		return &llm.Anthropic{Base: spec.Anthropic, Key: key, Official: spec.Anthropic == anthropicAPI.Anthropic,
			Headers: headers, Like: like, HTTP: e.HTTP}, model, like, true, nil
	}
	spec, ok := Spec(e.Providers, name)
	if !ok {
		return nil, "", "", false, fmt.Errorf("no provider named %q: /connect adds one", name)
	}
	key, missing := spec.Key()
	if missing {
		return nil, "", "", false, spec.NeedsKey()
	}
	model = a.Model
	if (model == "" || llm.IsClaude(model)) && len(spec.Models) > 0 {
		model = spec.Models[0] // no model, or a Claude name on another provider: its own first model
	} else if llm.IsClaude(model) {
		model = ""
	}
	if model == "" {
		return nil, "", "", false, errors.New("say which model to use (model: in the agent's file)")
	}
	if spec.Anthropic != "" {
		return &llm.Anthropic{Base: spec.Anthropic, Key: key, HTTP: e.HTTP}, model, "", false, nil
	}
	return &llm.OpenAI{Base: spec.OpenAI, Key: key, HTTP: e.HTTP}, model, "", false, nil
}

// maxContinues is how many times a turn cut by max_tokens carries on.
const maxContinues = 3

// textEvery is how often streamed text reaches the screen: in batches,
// since the server keeps every event.
const textEvery = 50 * time.Millisecond

func (e NativeEngine) RunTurn(ctx context.Context, req TurnRequest, out chan<- Event) (Reply, error) {
	a := req.Agent
	if e.Model != "" {
		a.Model = e.Model
	}
	client, model, like, setup, err := e.clientFor(a)
	if err != nil {
		return Reply{}, fmt.Errorf("%s: %w", a.Name, err)
	}
	code, err := codePlaces(req)
	if err != nil {
		return Reply{}, err
	}
	turn := newTurnID()
	// The agent's own tools run here, in this turn: files, search, the
	// shell, the checklist, the web.
	box := &tools.Box{Read: code.all, Write: code.write, Allow: e.AllowCommands,
		Todos: func(items []tools.Todo) { send(ctx, out, Todos{Agent: a.Name, Items: items}) }}
	if e.Approvals != nil {
		box.Ask = func(ctx context.Context, call, detail string) (bool, string, error) {
			return e.Approvals.Ask(ctx, turn, a.Name, call, detail)
		}
	}
	if req.Conversation != "" { // a site you allowed stays allowed for the conversation
		sites, _ := siteApprovals.LoadOrStore(req.Threads+"|"+req.Conversation, &sync.Map{})
		box.Domains = sites.(*sync.Map)
	}
	senior := NewSenior(a, e.WorkDir, e)
	if senior.Has("before_write") { // a stronger model looks at each command before you do
		box.Review = func(ctx context.Context, prompt string) string { return senior.Ask(prompt) }
	}
	var gwTools []apiTool
	var gw *rpcConn
	if e.Gateway != nil {
		if launch, ok := e.Gateway(a, turn, code.write); ok {
			launch.Env["TUBAS_READ_DIRS"] = strings.Join(code.all, string(os.PathListSeparator))
			stop, conn, list, err := startGateway(ctx, launch, e.WorkDir)
			if err != nil {
				return Reply{}, fmt.Errorf("%s: starting the tools: %w", a.Name, err)
			}
			defer stop()
			gw, gwTools = conn, list
		}
	}
	// The APIs accept tool names of letters, digits, _ and - (64 at most):
	// the model sees a clean name, the gateway gets the real one.
	real := map[string]string{}
	var offered []llm.Tool
	for _, t := range box.Tools() {
		offered = append(offered, llm.Tool{Name: t.Name, Description: t.Description, Schema: t.Schema})
	}
	for _, t := range gwTools {
		clean := functionName(t.Name)
		real[clean] = t.Name
		offered = append(offered, llm.Tool{Name: clean, Description: t.Description, Schema: orEmpty(t.Schema)})
	}
	if e.Approvals != nil {
		defer e.Approvals.Attach(ctx, turn, out)()
	}

	caps := llm.Lookup(model, like)
	price := caps.Price
	if !caps.Known {
		if p, ok := e.Prices[model]; ok { // a team's own prices
			price = llm.Price{Input: p.Input, Output: p.Output, CacheRead: p.Cached, CacheWrite: p.Input * 1.25}
		}
	}
	request := llm.Request{Model: model, System: SystemPrompt(a, req.Team) + box.Prompt(code.names), Tools: offered,
		Effort: turnEffort(a.Effort, req.Kind)}

	// The agent's history in this conversation: a follow-up continues it,
	// told only what happened since it last spoke.
	hist, closeHistory, err := openHistory(req, thread.Frame(model, request.System, offered))
	if err != nil {
		return Reply{}, fmt.Errorf("%s: its history: %w", a.Name, err)
	}
	defer closeHistory()
	first := req.Prompt
	if hist.continues() && req.Followup != "" {
		first = req.Followup
		if news := pastDigest(req.Past, a.Name, hist.lastAt()); news != "" {
			first = news + "\n" + first
		}
	}

	// Streamed text goes to the screen in batches; a new step starts on a
	// new paragraph; an attempt that broke has its text taken back.
	var buf strings.Builder
	shown, before, restore := "", "", ""
	stepStarted := false
	lastFlush := time.Now()
	flush := func() {
		if buf.Len() > 0 {
			shown += buf.String()
			send(ctx, out, TextDelta{a.Name, buf.String()})
			buf.Reset()
			lastFlush = time.Now()
		}
	}
	events := llm.Events{
		Text: func(s string) {
			if restore != "" { // the failed attempt's text was dropped: put back what came before it
				buf.WriteString(restore)
				restore = ""
			}
			if !stepStarted {
				if shown != "" || buf.Len() > 0 {
					buf.WriteString("\n\n")
				}
				stepStarted = true
			}
			buf.WriteString(s)
			if time.Since(lastFlush) >= textEvery {
				flush()
			}
		},
		Retry: func(r llm.Retry) {
			if r.Partial {
				buf.Reset()
				shown, restore, stepStarted = before, before, false
			}
			send(ctx, out, Retrying{Agent: a.Name, Attempt: r.Attempt, Max: r.Max, Status: r.Status,
				Reason: r.Reason, Delay: r.Delay, Partial: r.Partial})
		},
	}

	var reply Reply
	reply.Model = model
	limit := compactAt(a.TokenLimit, caps)
	// compact summarizes the conversation so far; the next requests start
	// from the summary. Only between tool rounds, never inside one.
	compact := func() {
		send(ctx, out, ToolCalled{a.Name, "compact(the conversation is long)"})
		ask := request
		ask.Messages = append(hist.messages(), llm.UserText(thread.CompactPrompt))
		resp, err := client.Stream(ctx, ask, llm.Events{Retry: events.Retry})
		summary := strings.TrimSpace(resp.Message.Text())
		if err == nil && summary == "" {
			err = errors.New("the model wrote no summary")
		}
		if err == nil {
			err = hist.compact(summary)
		}
		if err != nil {
			send(ctx, out, ToolResult{Agent: a.Name, Failed: true, Text: "not compacted: " + firstLine(err.Error())})
			return
		}
		reply.Tokens += resp.Usage.Input + resp.Usage.Output
		reply.Cached += resp.Usage.CacheRead
		reply.Cost += price.Cost(resp.Usage)
		send(ctx, out, ToolResult{Agent: a.Name, Text: fmt.Sprintf("%dK tokens summarized in %d words", resp.Usage.Input/1000, len(strings.Fields(summary)))})
	}
	if hist.lastInput() > limit {
		compact()
	}
	if err := hist.add(llm.UserText(first)); err != nil {
		return reply, fmt.Errorf("%s: writing its history: %w", a.Name, err)
	}
	request.Messages = hist.messages()
	steps := e.MaxSteps
	if steps <= 0 {
		steps = 60
	}
	continues, refreshed := 0, false
	answer := "" // the final answer, across max_tokens continuations
	for i := 0; ; i++ {
		if i == steps {
			return reply, fmt.Errorf("%s: still calling tools after %d steps", a.Name, steps)
		}
		before = shown
		if continues == 0 {
			stepStarted = false
		}
		resp, err := client.Stream(ctx, request, events)
		flush()
		if err != nil && llm.StatusOf(err) == 401 && setup && e.Setup.KeyHelper != "" && !refreshed {
			// The token from your apiKeyHelper expired: a fresh one, once.
			refreshed = true
			forgetHelperKey()
			if fresh, m, l, s, ferr := e.clientFor(a); ferr == nil {
				client, model, like, setup = fresh, m, l, s
				i--
				continue
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return reply, ctx.Err()
			}
			return reply, fmt.Errorf("%s: %w", a.Name, explainProvider(err))
		}
		reply.Tokens += resp.Usage.Input + resp.Usage.Output
		reply.Cached += resp.Usage.CacheRead
		reply.Cost += price.Cost(resp.Usage)
		text := resp.Message.Text()
		if resp.Stop == llm.StopMaxTokens {
			// Cut short: keep what was written, not a tool call cut in half.
			resp.Message.Blocks = slices.DeleteFunc(slices.Clone(resp.Message.Blocks), func(b llm.Block) bool { return b.Type == "tool_use" })
		}
		if len(resp.Message.Blocks) > 0 {
			if err := hist.addAnswer(resp.Message, resp.Usage.Input); err != nil {
				return reply, fmt.Errorf("%s: writing its history: %w", a.Name, err)
			}
		}
		switch resp.Stop {
		case llm.StopMaxTokens:
			// Carry on, a few times, in the same paragraph.
			answer += text
			if continues < maxContinues && len(resp.Message.Blocks) > 0 {
				continues++
				if err := hist.add(llm.UserText("Continue exactly where you stopped.")); err != nil {
					return reply, err
				}
				request.Messages = hist.messages()
				continue
			}
			reply.Text = strings.TrimSpace(answer + "\n\n(cut: the answer was longer than the model may write at once)")
			return reply, nil
		case llm.StopRefusal:
			note := "the model declined to answer this"
			if resp.Detail != "" {
				note += ": " + resp.Detail
			}
			reply.Text = strings.TrimSpace(answer + text + "\n\n(" + note + ")")
			return reply, nil
		case llm.StopPause:
			request.Messages = hist.messages() // the server's tools go on
			continue
		}
		continues = 0
		calls := resp.Message.Calls()
		if len(calls) == 0 {
			reply.Text = strings.TrimSpace(answer + text)
			return reply, nil
		}
		answer = ""
		if err := hist.add(e.runCalls(ctx, a.Name, calls, box, gw, real, senior, out)); err != nil {
			return reply, fmt.Errorf("%s: writing its history: %w", a.Name, err)
		}
		if resp.Usage.Input > limit { // the tool round is whole: a good place to compact
			compact()
		}
		request.Messages = hist.messages()
	}
}

// explainProvider turns a provider's error into what to do.
func explainProvider(err error) error {
	switch llm.StatusOf(err) {
	case 401, 403:
		return fmt.Errorf("the provider refused the key (%d): run /connect, or check your token or apiKeyHelper · %w", llm.StatusOf(err), err)
	case 404:
		return fmt.Errorf("this model or URL isn't there (404): /model picks one your provider serves · %w", err)
	case 429:
		return fmt.Errorf("the provider is limiting use right now (429): wait a little and ask again · %w", err)
	}
	return err
}

// maxToolOutput is how much of a tool's result goes back to the model.
const maxToolOutput = 60000

// functionName makes a tool name the APIs accept, as tucano does:
// letters, digits, _ and -, at most 64 (a long one keeps a hash).
func functionName(name string) string {
	clean := []rune(nonFunctionChars.ReplaceAllString(name, "_"))
	if len(clean) <= 64 {
		return string(clean)
	}
	sum := sha1.Sum([]byte(name))
	return string(clean[:55]) + "_" + hex.EncodeToString(sum[:])[:8]
}

var nonFunctionChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// startGateway runs `tubas mcp` for the turn and lists its tools.
func startGateway(ctx context.Context, launch GatewayLaunch, dir string) (func(), *rpcConn, []apiTool, error) {
	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range launch.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	stop := func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
		}
	}
	conn := newRPC(stdout, stdin, true, nil, func(m string, _ json.RawMessage) (any, error) {
		return nil, fmt.Errorf("%s is not supported", m)
	})
	if _, err := conn.call(ctx, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "tubas", "version": "1"}}); err != nil {
		stop()
		return nil, nil, nil, err
	}
	conn.notify("notifications/initialized", nil)
	res, err := conn.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	var list struct {
		Tools []apiTool `json:"tools"`
	}
	json.Unmarshal(res, &list)
	return stop, conn, list.Tools, nil
}

// callTool runs one tool through the gateway; an approval may make it wait.
func callTool(ctx context.Context, gw *rpcConn, call apiCall) (string, bool) {
	args := call.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	res, err := gw.call(ctx, "tools/call", map[string]any{"name": call.Name, "arguments": args})
	if err != nil {
		return err.Error(), true
	}
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	json.Unmarshal(res, &r)
	var parts []string
	for _, c := range r.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n"), r.IsError
}

func orEmpty(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
