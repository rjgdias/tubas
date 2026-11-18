package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tubas/internal/agent"
)

// scriptedProvider answers each Messages call with the next answer.
func scriptedProvider(t *testing.T, answers ...string) (*httptest.Server, func() []map[string]any) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		writeAnthropicStream(w, answers[min(n, len(answers))-1])
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any { mu.Lock(); defer mu.Unlock(); return bodies }
}

// The native tools run inside the turn: a read of the checkout, a shell
// command in the worktree that waits for your yes, the checklist.
func TestNativeToolsRunInTheTurn(t *testing.T) {
	checkout, worktree := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("hello\nworld\n"), 0o644)
	srv, bodies := scriptedProvider(t,
		`{"content":[{"type":"tool_use","id":"t1","name":"read","input":{"path":"a.txt"}},`+
			`{"type":"tool_use","id":"t2","name":"todo_write","input":{"todos":[{"content":"look","status":"in_progress"}]}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`,
		`{"content":[{"type":"tool_use","id":"t3","name":"bash","input":{"command":"echo made > out.txt","why":"to test"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`,
		`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
	t.Setenv("ACME_KEY", "k")
	broker, err := NewBroker()
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	eng := NativeEngine{Providers: Providers(map[string]ProviderSpec{"acme": {Anthropic: srv.URL, KeyEnv: "ACME_KEY", Models: []string{"m1"}}}),
		DefaultProvider: "acme", WorkDir: t.TempDir(), Approvals: broker}
	a := agent.Agent{Name: "dev", Instructions: "You code.", Tools: map[string]string{"app": "write"}}
	req := TurnRequest{Agent: a, Prompt: "make out.txt", Repos: map[string]string{"app": checkout},
		Workspace: func(string) (string, error) { return worktree, nil }}

	out := make(chan Event, 64)
	var events []Event
	collected := make(chan struct{})
	go func() {
		for e := range out {
			if ar, ok := e.(ApprovalRequested); ok {
				ar.Reply <- strings.Contains(ar.Detail, "echo made")
			}
			events = append(events, e)
		}
		close(collected)
	}()
	reply, err := eng.RunTurn(context.Background(), req, out)
	close(out)
	<-collected
	if err != nil || reply.Text != "done" {
		t.Fatalf("reply %+v, %v", reply, err)
	}
	if data, err := os.ReadFile(filepath.Join(worktree, "out.txt")); err != nil || strings.TrimSpace(string(data)) != "made" {
		t.Errorf("the approved command ran in the worktree: %q %v", data, err)
	}
	var seen []string
	for _, e := range events {
		switch e := e.(type) {
		case ToolCalled:
			seen = append(seen, e.Call)
		case ToolResult:
			seen = append(seen, "→ "+e.Text)
		case ApprovalRequested:
			seen = append(seen, "? "+e.Call)
		case Todos:
			seen = append(seen, "☐ "+e.Items[0].Content)
		}
	}
	want := "read(a.txt)|→ 2 lines|todo_write(1 steps)|☐ look|→ checklist saved: 0 of 1 done|bash(echo made > out.txt)|? bash(echo made > out.txt)|→ (no output)"
	if got := strings.Join(seen, "|"); got != want {
		t.Errorf("events\n got %s\nwant %s", got, want)
	}
	b := bodies()
	tools := ""
	for _, tl := range b[0]["tools"].([]any) {
		tools += tl.(map[string]any)["name"].(string) + " "
	}
	if tools != "read glob grep write edit bash todo_write web_fetch " {
		t.Errorf("the tools offered: %s", tools)
	}
	if sys := strings.ToLower(jsonText(b[0]["system"])); !strings.Contains(sys, "app: your own worktree at") {
		t.Errorf("the prompt names the worktree: %s", sys)
	}
	if msgs := jsonText(b[1]["messages"]); !strings.Contains(msgs, `     1\thello`) {
		t.Errorf("the file goes back to the model: %s", msgs)
	}
}

func jsonText(v any) string { data, _ := json.Marshal(v); return string(data) }
