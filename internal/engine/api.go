package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ── api: tubas's own agent loop ─────────────────────────────────────────
//
// For agents with only an API key or a local model (no agent CLI), tubas
// runs the loop itself, as agentes.chat's "model brain" does: send the
// conversation and the tools, run the tools the model calls, send the
// results back, until it answers. Two wire formats cover the providers:
// Anthropic's Messages API (Anthropic, and the Anthropic-compatible
// DeepSeek, GLM, Kimi, Qwen) and OpenAI's chat completions (OpenRouter,
// Ollama, LM Studio, any OpenAI-compatible base URL).
//
// The tools are the same tubas gateway every harness gets, started as an
// MCP server for the turn, so access and approvals don't change.

// Price is what a model costs, in US$ per million tokens.
type Price struct {
	Input, Cached, Output float64
}

// APIEngine runs agents with harness: api.
type APIEngine struct {
	Providers       map[string]ProviderSpec
	DefaultProvider string
	WorkDir         string
	Gateway         GatewayFunc
	Approvals       *Broker
	Prices          map[string]Price // by model; a model without one shows tokens, no cost
	HTTP            *http.Client
	MaxSteps        int // model calls per turn (0 = 30)
}

func (e APIEngine) Name() string { return "api" }

// anthropicAPI is the provider "anthropic" for the api harness: Anthropic's
// own API, with an API key (a Claude plan works only through claude).
var anthropicAPI = ProviderSpec{Name: Anthropic, Anthropic: "https://api.anthropic.com", KeyEnv: "ANTHROPIC_API_KEY",
	Models: []string{"claude-sonnet-5-5", "claude-haiku-5-5"}}

// claudeAliases are the ids behind claude's short model names.
var claudeAliases = map[string]string{"haiku": "claude-haiku-5-5", "sonnet": "claude-sonnet-5-5", "opus": "claude-opus-5-5"}

// apiTool is a tool as the model sees it.
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

// apiStep is one model answer.
type apiStep struct {
	Text                  string
	Calls                 []apiCall
	Input, Cached, Output int
}

// apiChat is one wire format: it keeps the conversation and asks for the
// next step.
type apiChat interface {
	next(ctx context.Context) (apiStep, error)
	results(calls []apiCall, outputs []string, failed []bool)
}

func (e APIEngine) RunTurn(ctx context.Context, req TurnRequest, out chan<- Event) (Reply, error) {
	a := req.Agent
	name := a.Provider
	if name == "" {
		name = e.DefaultProvider
	}
	spec := anthropicAPI
	if name != "" && name != Anthropic {
		var ok bool
		if spec, ok = e.Providers[name]; !ok {
			return Reply{}, fmt.Errorf("%s: no provider named %q", a.Name, name)
		}
	}
	key, missing := spec.Key()
	if missing {
		return Reply{}, fmt.Errorf("%s: %s needs its key: set the environment variable %s", a.Name, spec.Name, spec.KeyEnv)
	}
	model := a.Model
	if isClaudeModel(model) && spec.Name != Anthropic || model == "" {
		model = ""
		if len(spec.Models) > 0 {
			model = spec.Models[0]
		}
	}
	if full, ok := claudeAliases[model]; ok && spec.Name == Anthropic {
		model = full // the API wants the model's id
	}
	if model == "" {
		return Reply{}, fmt.Errorf("%s: say which model to use (model: in the agent's file)", a.Name)
	}

	code, err := codePlaces(req)
	if err != nil {
		return Reply{}, err
	}
	turn := newTurnID()
	var tools []apiTool
	var gw *rpcConn
	if e.Gateway != nil {
		if launch, ok := e.Gateway(a, turn, code.write, "api"); ok {
			launch.Env["TUBAS_READ_DIRS"] = strings.Join(code.all, string(os.PathListSeparator))
			stop, conn, list, err := startGateway(ctx, launch, e.WorkDir)
			if err != nil {
				return Reply{}, fmt.Errorf("%s: starting the tools: %w", a.Name, err)
			}
			defer stop()
			gw, tools = conn, list
		}
	}
	if e.Approvals != nil {
		defer e.Approvals.Attach(ctx, turn, out)()
	}

	client := e.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	system := SystemPrompt(a, req.Team) + apiCodePrompt(code)
	var chat apiChat
	if spec.Anthropic != "" {
		chat = &messagesChat{http: client, base: spec.Anthropic, key: key, official: spec.Name == Anthropic, model: model,
			system: system, tools: tools, prompt: req.Prompt, effort: turnEffort(a.Effort, req.Kind)}
	} else {
		chat = &openAIChat{http: client, base: spec.OpenAI, key: key, model: model, system: system, tools: tools, prompt: req.Prompt}
	}

	var reply Reply
	reply.Model = model
	steps := e.MaxSteps
	if steps <= 0 {
		steps = 30
	}
	price, priced := e.Prices[model]
	for i := 0; ; i++ {
		if i == steps {
			return reply, fmt.Errorf("%s: still calling tools after %d steps", a.Name, steps)
		}
		step, err := chat.next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return reply, ctx.Err()
			}
			return reply, fmt.Errorf("%s: %w", a.Name, err)
		}
		reply.Tokens += step.Input + step.Output
		reply.Cached += step.Cached
		if priced {
			reply.Cost += (float64(step.Input-step.Cached)*price.Input + float64(step.Cached)*price.Cached + float64(step.Output)*price.Output) / 1e6
		}
		if err := overLimit(step.Input, a.TokenLimit); err != nil {
			return reply, err
		}
		if step.Text != "" {
			send(ctx, out, TextDelta{a.Name, step.Text})
		}
		if len(step.Calls) == 0 {
			reply.Text = step.Text
			return reply, nil
		}
		outputs, failed := make([]string, len(step.Calls)), make([]bool, len(step.Calls))
		for j, call := range step.Calls {
			send(ctx, out, ToolCalled{a.Name, describeCall(call.Name, call.Args)})
			if gw == nil {
				outputs[j], failed[j] = "no tools are available", true
			} else {
				outputs[j], failed[j] = callTool(ctx, gw, call)
			}
			ev := ToolResult{Agent: a.Name, Text: firstLine(outputs[j]), Failed: failed[j]}
			if k := strings.Index(outputs[j], "\n↑ "); k >= 0 {
				ev.Text += "\n" + strings.TrimSpace(outputs[j][k+1:])
			}
			send(ctx, out, ev)
		}
		chat.results(step.Calls, outputs, failed)
	}
}

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

// apiCodePrompt tells an api agent where it works on code.
func apiCodePrompt(code codeAccess) string {
	if len(code.all) == 0 {
		return ""
	}
	p := strings.Replace(code.prompt, "(Read, Grep, Glob, Edit, Write and a shell)",
		"(code__read_file, code__list_files, code__search, code__write_file in your worktree, and code__run_command, which a human approves)", 1)
	return strings.Replace(p, "(Read, Grep, Glob)", "(code__read_file, code__list_files, code__search)", 1)
}

func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body any, out any) error {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, firstLine(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

// ── Anthropic Messages ──────────────────────────────────────────────────

type messagesChat struct {
	http             *http.Client
	base, key, model string
	official         bool
	system, prompt   string
	effort           string
	tools            []apiTool
	messages         []map[string]any
}

func (c *messagesChat) next(ctx context.Context) (apiStep, error) {
	if c.messages == nil {
		c.messages = []map[string]any{{"role": "user", "content": c.prompt}}
	}
	tools := make([]map[string]any, len(c.tools))
	for i, t := range c.tools {
		tools[i] = map[string]any{"name": t.Name, "description": t.Description, "input_schema": json.RawMessage(orEmpty(t.Schema))}
	}
	body := map[string]any{"model": c.model, "max_tokens": 8192, "messages": c.messages,
		// The system prompt and the tools never change in a session: cached.
		"system": []map[string]any{{"type": "text", "text": c.system, "cache_control": map[string]string{"type": "ephemeral"}}}}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	headers := map[string]string{"anthropic-version": "2023-06-01"}
	if c.official {
		headers["x-api-key"] = c.key
	} else {
		headers["Authorization"] = "Bearer " + c.key
		headers["x-api-key"] = c.key
	}
	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			Input       int `json:"input_tokens"`
			Output      int `json:"output_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := postJSON(ctx, c.http, strings.TrimRight(c.base, "/")+"/v1/messages", headers, body, &resp); err != nil {
		return apiStep{}, err
	}
	var step apiStep
	var content []map[string]any
	var texts []string
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
			content = append(content, map[string]any{"type": "text", "text": b.Text})
		case "tool_use":
			step.Calls = append(step.Calls, apiCall{b.ID, b.Name, b.Input})
			content = append(content, map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": json.RawMessage(orEmpty(b.Input))})
		}
	}
	step.Text = strings.Join(texts, "\n")
	u := resp.Usage
	step.Input, step.Cached, step.Output = u.Input+u.CacheRead+u.CacheCreate, u.CacheRead, u.Output
	c.messages = append(c.messages, map[string]any{"role": "assistant", "content": content})
	return step, nil
}

func (c *messagesChat) results(calls []apiCall, outputs []string, failed []bool) {
	var blocks []map[string]any
	for i, call := range calls {
		blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": call.ID, "content": outputs[i], "is_error": failed[i]})
	}
	c.messages = append(c.messages, map[string]any{"role": "user", "content": blocks})
}

// ── OpenAI chat completions ─────────────────────────────────────────────

type openAIChat struct {
	http             *http.Client
	base, key, model string
	system, prompt   string
	tools            []apiTool
	messages         []map[string]any
}

func (c *openAIChat) next(ctx context.Context) (apiStep, error) {
	if c.messages == nil {
		c.messages = []map[string]any{{"role": "system", "content": c.system}, {"role": "user", "content": c.prompt}}
	}
	tools := make([]map[string]any, len(c.tools))
	for i, t := range c.tools {
		tools[i] = map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description,
			"parameters": json.RawMessage(orEmpty(t.Schema))}}
	}
	body := map[string]any{"model": c.model, "messages": c.messages}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	headers := map[string]string{}
	if c.key != "" {
		headers["Authorization"] = "Bearer " + c.key
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Details    struct {
				Cached int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := postJSON(ctx, c.http, strings.TrimRight(c.base, "/")+"/chat/completions", headers, body, &resp); err != nil {
		return apiStep{}, err
	}
	if len(resp.Choices) == 0 {
		return apiStep{}, errors.New("the model returned no answer")
	}
	msg := resp.Choices[0].Message
	step := apiStep{Text: msg.Content, Input: resp.Usage.Prompt, Cached: resp.Usage.Details.Cached, Output: resp.Usage.Completion}
	assistant := map[string]any{"role": "assistant", "content": msg.Content}
	if len(msg.ToolCalls) > 0 {
		var calls []map[string]any
		for _, tc := range msg.ToolCalls {
			step.Calls = append(step.Calls, apiCall{tc.ID, tc.Function.Name, json.RawMessage(orEmpty(json.RawMessage(tc.Function.Arguments)))})
			calls = append(calls, map[string]any{"id": tc.ID, "type": "function",
				"function": map[string]string{"name": tc.Function.Name, "arguments": tc.Function.Arguments}})
		}
		assistant["tool_calls"] = calls
	}
	c.messages = append(c.messages, assistant)
	return step, nil
}

func (c *openAIChat) results(calls []apiCall, outputs []string, failed []bool) {
	for i, call := range calls {
		text := outputs[i]
		if failed[i] {
			text = "error: " + text
		}
		c.messages = append(c.messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": text})
	}
}

func orEmpty(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
