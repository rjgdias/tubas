package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tubas/internal/agent"
)

// fakeACP stands in for an ACP agent (opencode acp, gemini…): it plays a
// turn that asks for three permissions and answers with what it saw.
func fakeACP() {
	in := bufio.NewReader(os.Stdin)
	write := func(m map[string]any) {
		m["jsonrpc"] = "2.0"
		data, _ := json.Marshal(m)
		fmt.Println(string(data))
	}
	type msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params map[string]any  `json:"params"`
		Result map[string]any  `json:"result"`
	}
	next := func() (msg, bool) {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return msg{}, false
		}
		var m msg
		json.Unmarshal(line, &m)
		return m, true
	}
	update := func(u map[string]any) {
		write(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "s1", "update": u}})
	}
	ask := func(id int, toolCall map[string]any) string {
		write(map[string]any{"id": id, "method": "session/request_permission", "params": map[string]any{"sessionId": "s1",
			"toolCall": toolCall, "options": []any{
				map[string]any{"optionId": "yes-always", "name": "Always", "kind": "allow_always"},
				map[string]any{"optionId": "yes", "name": "Allow", "kind": "allow_once"},
				map[string]any{"optionId": "no", "name": "Reject", "kind": "reject_once"}}}})
		for {
			m, ok := next()
			if !ok {
				return "closed"
			}
			if string(m.ID) == fmt.Sprint(id) && m.Method == "" {
				outcome, _ := m.Result["outcome"].(map[string]any)
				return fmt.Sprint(outcome["optionId"])
			}
		}
	}
	seen := map[string]any{}
	for {
		m, ok := next()
		if !ok {
			return
		}
		switch m.Method {
		case "initialize":
			write(map[string]any{"id": m.ID, "result": map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}, "authMethods": []any{}}})
		case "session/new":
			seen["new"] = m.Params
			write(map[string]any{"id": m.ID, "result": map[string]any{"sessionId": "s1"}})
		case "session/prompt":
			seen["prompt"] = m.Params["prompt"]
			cwd, _ := seen["new"].(map[string]any)["cwd"].(string)
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "on it. "}})
			update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "c1", "title": "Run npm test", "kind": "execute", "status": "pending"})
			seen["execute"] = ask(77, map[string]any{"toolCallId": "c1", "kind": "execute", "rawInput": map[string]any{"command": "npm test"}})
			update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "c1", "status": "completed",
				"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "8 passed"}}}})
			seen["outside"] = ask(78, map[string]any{"toolCallId": "c2", "kind": "edit", "locations": []any{map[string]any{"path": filepath.Join(os.TempDir(), "elsewhere", "x.txt")}}})
			seen["inside"] = ask(79, map[string]any{"toolCallId": "c3", "kind": "edit", "locations": []any{map[string]any{"path": filepath.Join(cwd, "src", "app.py")}}})
			answer, _ := json.Marshal(seen)
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": string(answer)}})
			write(map[string]any{"id": m.ID, "result": map[string]any{"stopReason": "end_turn"}})
		}
	}
}

func TestACPAgentRunsATurn(t *testing.T) {
	exe, _ := os.Executable()
	t.Setenv("TUBAS_FAKE_ACP", "1")
	wt := t.TempDir()
	eng := ACPEngine{Agent: ACPAgent{Name: "opencode", Command: []string{exe}}, WorkDir: t.TempDir(),
		Gateway: func(a agent.Agent, turn string, writeDirs []string, harness string) (GatewayLaunch, bool) {
			return GatewayLaunch{Command: "tubas", Args: []string{"mcp"}, Env: map[string]string{"TUBAS_HARNESS": harness, "TUBAS_TURN": turn}}, true
		}}
	a := agent.Agent{Name: "ana", Harness: "opencode", Instructions: "You fix the front end.", Tools: map[string]string{"front": "write"}}
	req := TurnRequest{Agent: a, Prompt: "add the region filter", Repos: map[string]string{"front": "/repos/front"},
		Workspace: func(string) (string, error) { return wt, nil }}
	out := make(chan Event, 64)
	var calls, results []string
	asked := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range out {
			switch e := e.(type) {
			case ApprovalRequested:
				asked++
				if !strings.Contains(e.Detail, "$ npm test") || !strings.Contains(e.Detail, wt) {
					t.Errorf("the inbox gets the command and where it runs: %q", e.Detail)
				}
				e.Reply <- true
			case ToolCalled:
				calls = append(calls, e.Call)
			case ToolResult:
				results = append(results, e.Text)
			}
		}
	}()
	reply, err := eng.RunTurn(context.Background(), req, out)
	close(out)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimPrefix(reply.Text, "on it. ")
	var seen struct {
		New     map[string]any `json:"new"`
		Prompt  []any          `json:"prompt"`
		Execute string         `json:"execute"`
		Outside string         `json:"outside"`
		Inside  string         `json:"inside"`
	}
	if err := json.Unmarshal([]byte(text), &seen); err != nil {
		t.Fatalf("the answer is the message chunks: %q", reply.Text)
	}
	if seen.Execute != "yes" || asked != 1 {
		t.Errorf("a command waits for your y, then is allowed once (never always): %q, asked %d", seen.Execute, asked)
	}
	if seen.Outside != "no" || seen.Inside != "yes" {
		t.Errorf("edits: outside the worktree %q (want no), inside %q (want yes)", seen.Outside, seen.Inside)
	}
	if seen.New["cwd"] != wt {
		t.Errorf("the agent works in its worktree: %v", seen.New["cwd"])
	}
	servers, _ := seen.New["mcpServers"].([]any)
	if len(servers) != 1 || !strings.Contains(fmt.Sprint(servers[0]), "TUBAS_HARNESS value:acp") {
		t.Errorf("the tubas gateway is the agent's MCP server: %v", servers)
	}
	if !strings.Contains(fmt.Sprint(seen.Prompt), "You fix the front end.") || !strings.Contains(fmt.Sprint(seen.Prompt), "add the region filter") {
		t.Errorf("the instructions come before the task in the prompt: %v", seen.Prompt)
	}
	if strings.Join(calls, "|") != "Run npm test" || strings.Join(results, "|") != "8 passed" {
		t.Errorf("events: calls %v, results %v", calls, results)
	}
}
