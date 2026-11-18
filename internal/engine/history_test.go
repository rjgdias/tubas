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
	"time"

	"tubas/internal/agent"
	"tubas/internal/thread"
)

// historyProvider answers by what it's asked, and keeps every request.
func historyProvider(t *testing.T, answer func(body map[string]any, last string) string) (*httptest.Server, func() []map[string]any) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		msgs := body["messages"].([]any)
		writeAnthropicStream(w, answer(body, jsonText(msgs[len(msgs)-1])))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any { mu.Lock(); defer mu.Unlock(); return bodies }
}

func historyEngine(t *testing.T, url string) NativeEngine {
	t.Setenv("ACME_KEY", "k")
	return NativeEngine{Providers: Providers(map[string]ProviderSpec{"acme": {Anthropic: url, KeyEnv: "ACME_KEY", Models: []string{"m1"}}}),
		DefaultProvider: "acme", WorkDir: t.TempDir()}
}

func turn(t *testing.T, eng NativeEngine, req TurnRequest) ([]Event, Reply) {
	t.Helper()
	out := make(chan Event, 256)
	reply, err := eng.RunTurn(context.Background(), req, out)
	close(out)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	for e := range out {
		events = append(events, e)
	}
	return events, reply
}

func TestAFollowUpContinuesTheAgentsHistory(t *testing.T) {
	srv, bodies := historyProvider(t, func(_ map[string]any, last string) string {
		if strings.Contains(last, "q2") {
			return `{"content":[{"type":"text","text":"second answer"}],"stop_reason":"end_turn","usage":{"input_tokens":50,"output_tokens":5}}`
		}
		return `{"content":[{"type":"thinking","thinking":"let me see","signature":"SIG-1"},{"type":"text","text":"first answer"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":5}}`
	})
	eng := historyEngine(t, srv.URL)
	threads := t.TempDir()
	a := agent.Agent{Name: "dev", Instructions: "You help."}
	turn(t, eng, TurnRequest{Agent: a, Prompt: "q1", Followup: "q1", Conversation: "s1", Threads: threads})
	past := []Exchange{{Question: "what about sales?", Agent: "iris", Answer: "sales are fine", Ended: time.Now()}}
	_, reply := turn(t, eng, TurnRequest{Agent: a, Prompt: "Earlier in this conversation: LOTS\n\nNew question from the human: q2",
		Followup: "q2", Conversation: "s1", Threads: threads, Past: past})
	if reply.Text != "second answer" {
		t.Fatalf("reply %q", reply.Text)
	}
	msgs := jsonText(bodies()[1]["messages"])
	for _, want := range []string{`"q1"`, `"SIG-1"`, "first answer", "sales are fine", "q2"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("the second request lacks %s: %s", want, msgs)
		}
	}
	if strings.Contains(msgs, "LOTS") {
		t.Errorf("an agent with a history isn't told the summary again: %s", msgs)
	}
	if _, err := os.Stat(filepath.Join(threads, "s1", "dev.jsonl")); err != nil {
		t.Error("the history is kept per conversation and agent:", err)
	}
}

func TestALongTurnCompactsBetweenToolRounds(t *testing.T) {
	step := 0
	srv, bodies := historyProvider(t, func(_ map[string]any, last string) string {
		step++
		switch {
		case strings.Contains(last, "will be compacted"):
			return `{"content":[{"type":"text","text":"SUMMARY: checklist made, next is the answer"}],"stop_reason":"end_turn","usage":{"input_tokens":5000,"output_tokens":40}}`
		case step == 1:
			return `{"content":[{"type":"tool_use","id":"t1","name":"todo_write","input":{"todos":[{"content":"x","status":"in_progress"}]}}],` +
				`"stop_reason":"tool_use","usage":{"input_tokens":5000,"output_tokens":5}}`
		}
		return `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":300,"output_tokens":5}}`
	})
	eng := historyEngine(t, srv.URL)
	threads := t.TempDir()
	a := agent.Agent{Name: "dev", Instructions: "You help.", TokenLimit: 1000}
	events, reply := turn(t, eng, TurnRequest{Agent: a, Prompt: "do it", Followup: "do it", Conversation: "s1", Threads: threads})
	if reply.Text != "done" {
		t.Fatalf("reply %q", reply.Text)
	}
	b := bodies()
	if len(b) != 3 {
		t.Fatalf("a step, the summary, the next step: %d requests", len(b))
	}
	next := jsonText(b[2]["messages"])
	if !strings.Contains(next, "SUMMARY: checklist made") || strings.Contains(next, "todo_write") || strings.Contains(next, "do it") {
		t.Errorf("after compacting, the requests start from the summary alone: %s", next)
	}
	var seen []string
	for _, e := range events {
		if c, ok := e.(ToolCalled); ok {
			seen = append(seen, c.Call)
		}
	}
	if strings.Join(seen, "|") != "todo_write(1 steps)|compact(the conversation is long)" {
		t.Errorf("calls %v", seen)
	}
	th, unlock, _ := thread.Open(threads, "s1", "dev")
	defer unlock()
	if m := th.Messages("x"); !strings.Contains(m[0].Blocks[0].Text, "SUMMARY") {
		t.Errorf("the summary is in the history: %+v", m)
	}
}
