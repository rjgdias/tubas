package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"tubas/internal/agent"
)

// fakeCodex stands in for `codex app-server`: it answers the handshake,
// asks for an approval of its own (tubas must decline), plays a turn and
// ends with an answer that tells the test what it saw. With mode "slow"
// it waits for turn/interrupt instead.
func fakeCodex(mode string) {
	in := bufio.NewReader(os.Stdin)
	write := func(m map[string]any) {
		data, _ := json.Marshal(m)
		fmt.Println(string(data))
	}
	seen := map[string]any{"args": os.Args[1:]}
	wd, _ := os.Getwd()
	seen["dir"] = wd
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
			Result map[string]any  `json:"result"`
		}
		json.Unmarshal(line, &m)
		switch m.Method {
		case "initialize":
			write(map[string]any{"id": m.ID, "result": map[string]any{}})
		case "thread/start":
			seen["start"] = m.Params
			write(map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]any{"id": "th1"}}})
		case "turn/start":
			seen["input"] = m.Params["input"]
			write(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]any{"id": "tu1"}}})
			if mode == "slow" {
				continue
			}
			write(map[string]any{"id": 900, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
		case "turn/interrupt":
			write(map[string]any{"id": m.ID, "result": map[string]any{}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"id": "tu1", "status": "interrupted"}}})
		case "":
			if m.Result == nil { // our approval request came back
				continue
			}
			seen["decision"] = m.Result["decision"]
			n := func(method string, params map[string]any) { write(map[string]any{"method": method, "params": params}) }
			n("item/started", map[string]any{"item": map[string]any{"type": "mcpToolCall", "id": "i1", "server": "tubas",
				"tool": "data__run_sql", "arguments": map[string]any{"query": "SELECT 1"}}})
			n("item/completed", map[string]any{"item": map[string]any{"type": "mcpToolCall", "id": "i1", "status": "completed",
				"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "1 row\nmore"}}}}})
			n("item/agentMessage/delta", map[string]any{"delta": "thinking aloud"})
			answer, _ := json.Marshal(seen)
			n("item/completed", map[string]any{"item": map[string]any{"type": "agentMessage", "id": "m1", "text": string(answer)}})
			n("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": map[string]any{
				"inputTokens": 1000, "cachedInputTokens": 600, "outputTokens": 50}}})
			n("turn/completed", map[string]any{"turn": map[string]any{"id": "tu1", "status": "completed"}})
		}
	}
}

func codexTurn(t *testing.T, mode string, a agent.Agent, req TurnRequest, ctx context.Context) (Reply, []Event, error) {
	t.Helper()
	exe, _ := os.Executable()
	t.Setenv("TUBAS_FAKE_CODEX", mode)
	eng := CodexEngine{Command: exe, WorkDir: t.TempDir(), Plan: true,
		Gateway: func(a agent.Agent, turn string, writeDirs []string, harness string) (GatewayLaunch, bool) {
			return GatewayLaunch{Command: "tubas", Args: []string{"mcp", "-agent", a.Name},
				Env: map[string]string{"TUBAS_TURN": turn, "TUBAS_HARNESS": harness, "TUBAS_WRITE_DIRS": strings.Join(writeDirs, ";")}}, true
		}}
	req.Agent, req.Prompt = a, "find the drop"
	out := make(chan Event, 64)
	reply, err := eng.RunTurn(ctx, req, out)
	close(out)
	var events []Event
	for e := range out {
		events = append(events, e)
	}
	return reply, events, err
}

func TestCodexRunsATurn(t *testing.T) {
	a := agent.Agent{Name: "leo", Model: "gpt-5.5", Instructions: "You check pipelines.", TokenLimit: 50000, Tools: map[string]string{"data": "read"}}
	reply, events, err := codexTurn(t, "turn", a, TurnRequest{}, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var seen struct {
		Args     []string       `json:"args"`
		Start    map[string]any `json:"start"`
		Input    []any          `json:"input"`
		Decision string         `json:"decision"`
	}
	if err := json.Unmarshal([]byte(reply.Text), &seen); err != nil {
		t.Fatalf("the answer is the last agent message: %q", reply.Text)
	}
	args := strings.Join(seen.Args, " ")
	if !strings.HasPrefix(args, "app-server") || !strings.Contains(args, "--disable shell_tool") || !strings.Contains(args, `approval_policy="never"`) {
		t.Errorf("codex starts as an app-server with its shell off: %s", args)
	}
	if seen.Decision != "decline" {
		t.Errorf("codex's own approvals are declined, got %q", seen.Decision)
	}
	if seen.Start["sandbox"] != "read-only" || seen.Start["model"] != "gpt-5.5" || seen.Start["ephemeral"] != true ||
		!strings.Contains(fmt.Sprint(seen.Start["developerInstructions"]), "You check pipelines.") {
		t.Errorf("thread/start carries the agent: %v", seen.Start)
	}
	gw, _ := seen.Start["config"].(map[string]any)["mcp_servers.tubas"].(map[string]any)
	if env, _ := gw["env"].(map[string]any); env["TUBAS_HARNESS"] != "codex" || env["TUBAS_TURN"] == "" {
		t.Errorf("the tubas gateway is codex's MCP server: %v", gw)
	}
	if reply.Tokens != 1050 || reply.Cached != 600 || !reply.Plan || reply.Model != "gpt-5.5" {
		t.Errorf("usage and billing: %+v", reply)
	}
	var calls, results, text []string
	for _, e := range events {
		switch e := e.(type) {
		case ToolCalled:
			calls = append(calls, e.Call)
		case ToolResult:
			results = append(results, e.Text)
		case TextDelta:
			text = append(text, e.Text)
		}
	}
	if strings.Join(calls, "|") != "run_sql(SELECT 1)" || strings.Join(results, "|") != "1 row" || strings.Join(text, "") != "thinking aloud" {
		t.Errorf("events: calls %v, results %v, text %v", calls, results, text)
	}
}

func TestCodexWriterWorksInItsWorktree(t *testing.T) {
	wt := t.TempDir()
	a := agent.Agent{Name: "leo", Model: "sonnet", Instructions: "x", Tools: map[string]string{"api": "write"}}
	req := TurnRequest{Repos: map[string]string{"api": "/repos/api"}, Workspace: func(string) (string, error) { return wt, nil }}
	reply, _, err := codexTurn(t, "turn", a, req, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var seen struct {
		Dir   string         `json:"dir"`
		Start map[string]any `json:"start"`
	}
	json.Unmarshal([]byte(reply.Text), &seen)
	if seen.Start["sandbox"] != "workspace-write" || seen.Start["cwd"] != wt || seen.Start["model"] != nil {
		t.Errorf("a writer edits in its worktree, on codex's default model: %v", seen.Start)
	}
	if !strings.Contains(fmt.Sprint(seen.Start["developerInstructions"]), "run_command") {
		t.Error("the agent is told its shell is run_command")
	}
	gw := seen.Start["config"].(map[string]any)["mcp_servers.tubas"].(map[string]any)
	if gw["env"].(map[string]any)["TUBAS_WRITE_DIRS"] != wt {
		t.Errorf("the gateway knows the worktree: %v", gw["env"])
	}
}

func TestCodexStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	_, _, err := codexTurn(t, "slow", agent.Agent{Name: "leo", Instructions: "x"}, TurnRequest{}, ctx)
	if err != context.Canceled {
		t.Errorf("esc interrupts the turn: %v", err)
	}
}
