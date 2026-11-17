package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"tubas/internal/agent"
)

// fakeMCP stands in for the tubas gateway: one tool, data__run_sql.
func fakeMCP() {
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		json.Unmarshal(line, &m)
		var result any
		switch m.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "data__run_sql", "description": "Run SQL. harness=" + os.Getenv("TUBAS_HARNESS"),
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}}
		case "tools/call":
			args, _ := json.Marshal(m.Params["arguments"])
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "1 row for " + string(args)}}}
		default:
			continue // notifications
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		fmt.Println(string(data))
	}
}

func fakeGateway(t *testing.T) GatewayFunc {
	exe, _ := os.Executable()
	t.Setenv("TUBAS_FAKE_MCP", "1")
	return func(a agent.Agent, turn string, writeDirs []string, harness string) (GatewayLaunch, bool) {
		return GatewayLaunch{Command: exe, Env: map[string]string{"TUBAS_HARNESS": harness}}, true
	}
}

const (
	anthropicFirst = `{"content":[{"type":"text","text":"let me look"},{"type":"tool_use","id":"tu1","name":"data__run_sql","input":{"query":"SELECT 1"}}],` +
		`"stop_reason":"tool_use","usage":{"input_tokens":400,"cache_read_input_tokens":600,"output_tokens":20}}`
	anthropicLast = `{"content":[{"type":"text","text":"the drop is in the south"}],"stop_reason":"end_turn","usage":{"input_tokens":500,"output_tokens":30}}`
	openAIFirst   = `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"data__run_sql","arguments":"{\"query\":\"SELECT 1\"}"}}]}}],` +
		`"usage":{"prompt_tokens":1000,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":600}}}`
	openAILast = `{"choices":[{"message":{"content":"the drop is in the south"}}],"usage":{"prompt_tokens":1100,"completion_tokens":30}}`
)

// fakeProvider answers like a model: first a tool call, then the answer.
func fakeProvider(t *testing.T, first, last string) (*httptest.Server, func() []map[string]any) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		body["_auth"] = r.Header.Get("Authorization")
		body["_path"] = r.URL.Path
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			fmt.Fprint(w, first)
		} else {
			fmt.Fprint(w, last)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any { mu.Lock(); defer mu.Unlock(); return bodies }
}

func runAPI(t *testing.T, eng APIEngine, a agent.Agent) (Reply, []Event) {
	t.Helper()
	out := make(chan Event, 64)
	reply, err := eng.RunTurn(context.Background(), TurnRequest{Agent: a, Prompt: "why did orders drop?"}, out)
	if err != nil {
		t.Fatal(err)
	}
	close(out)
	var events []Event
	for e := range out {
		events = append(events, e)
	}
	return reply, events
}

func TestAPIHarnessOnAnAnthropicCompatibleProvider(t *testing.T) {
	srv, bodies := fakeProvider(t, anthropicFirst, anthropicLast)
	t.Setenv("DEEPSEEK_API_KEY", "ds-key")
	providers := Providers(map[string]ProviderSpec{"deepseek": {Anthropic: srv.URL + "/anthropic", KeyEnv: "DEEPSEEK_API_KEY", Models: []string{"deepseek-v4-pro"}}})
	eng := APIEngine{Providers: providers, WorkDir: t.TempDir(), Gateway: fakeGateway(t),
		Prices: map[string]Price{"deepseek-v4-pro": {Input: 1, Cached: 0.1, Output: 2}}}
	a := agent.Agent{Name: "iris", Provider: "deepseek", Model: "sonnet", Instructions: "You investigate.", Tools: map[string]string{"data": "read"}}
	reply, events := runAPI(t, eng, a)
	if reply.Text != "the drop is in the south" || reply.Model != "deepseek-v4-pro" {
		t.Errorf("reply = %+v", reply)
	}
	if reply.Tokens != 1000+20+500+30 || reply.Cached != 600 {
		t.Errorf("tokens add up over the steps: %+v", reply)
	}
	want := (400*1 + 600*0.1 + 20*2 + 500*1 + 30*2) / 1e6
	if d := reply.Cost - want; d > 1e-12 || d < -1e-12 {
		t.Errorf("cost from the price table: %v, want %v", reply.Cost, want)
	}
	b := bodies()
	if b[0]["_path"] != "/anthropic/v1/messages" || b[0]["_auth"] != "Bearer ds-key" || b[0]["model"] != "deepseek-v4-pro" {
		t.Errorf("the Messages call: %v", b[0])
	}
	if !strings.Contains(fmt.Sprint(b[0]["system"]), "You investigate.") || !strings.Contains(fmt.Sprint(b[0]["tools"]), "harness=api") {
		t.Errorf("the system prompt and the gateway's tools go with it: %v", b[0])
	}
	if !strings.Contains(fmt.Sprint(b[1]["messages"]), "1 row for") {
		t.Errorf("the tool result goes back to the model: %v", b[1]["messages"])
	}
	var calls []string
	for _, e := range events {
		if c, ok := e.(ToolCalled); ok {
			calls = append(calls, c.Call)
		}
	}
	if strings.Join(calls, "|") != "run_sql(SELECT 1)" {
		t.Errorf("calls %v", calls)
	}
}

func TestAPIHarnessOnAnOpenAICompatibleProvider(t *testing.T) {
	srv, bodies := fakeProvider(t, openAIFirst, openAILast)
	providers := Providers(map[string]ProviderSpec{"local": {OpenAI: srv.URL + "/v1", Models: []string{"qwen3"}}})
	eng := APIEngine{Providers: providers, DefaultProvider: "local", WorkDir: t.TempDir(), Gateway: fakeGateway(t)}
	reply, _ := runAPI(t, eng, agent.Agent{Name: "leo", Instructions: "x", Tools: map[string]string{"data": "read"}})
	if reply.Text != "the drop is in the south" || reply.Tokens != 1000+20+1100+30 || reply.Cached != 600 || reply.Cost != 0 {
		t.Errorf("reply = %+v", reply)
	}
	b := bodies()
	if b[0]["_path"] != "/v1/chat/completions" || b[0]["model"] != "qwen3" || b[0]["_auth"] != "" {
		t.Errorf("the chat call, with no key for a local model: %v", b[0])
	}
	if !strings.Contains(fmt.Sprint(b[1]["messages"]), "tool_call_id:c1") {
		t.Errorf("the tool result answers its call: %v", b[1]["messages"])
	}
}
