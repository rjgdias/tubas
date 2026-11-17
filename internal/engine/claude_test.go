package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// collectStream runs parseStream and returns the events it emitted.
func collectStream(t *testing.T, stream string, limit int) ([]Event, Reply, error) {
	t.Helper()
	var events []Event
	reply, err := parseStream(strings.NewReader(stream), "iris", limit, func(e Event) error {
		events = append(events, e)
		return nil
	})
	return events, reply, err
}

// A real stream recorded from `claude -p` (a one-word answer, no tools).
func TestParseRecordedTextStream(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "text_only.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events, reply, err := collectStream(t, string(data), 0)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "ok" {
		t.Errorf("reply text = %q", reply.Text)
	}
	if reply.Tokens != 2+1014+4 {
		t.Errorf("tokens = %d", reply.Tokens)
	}
	if len(events) != 1 || events[0] != (TextDelta{"iris", "ok"}) {
		t.Errorf("events = %#v", events)
	}
}

// A stream with a tool call, in the same shape claude writes it.
const toolStream = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"mcp__tubas__run_sql","input":{"query":"SELECT regiao,\n  COUNT(*) FROM pedidos"}}],"usage":{"input_tokens":10,"output_tokens":5}}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"5 rows · regiao, n\nsul | 10"}]}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"denied","is_error":true}]}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"queda no sul."}}}
{"type":"result","subtype":"success","is_error":false,"result":"queda no sul.\nHANDOFF @leo: confere?","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":30}}
`

func TestParseToolStream(t *testing.T) {
	events, reply, err := collectStream(t, toolStream, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{
		ToolCalled{"iris", "run_sql(SELECT regiao, COUNT(*) FROM pedidos)"},
		ToolResult{Agent: "iris", Text: "5 rows · regiao, n"},
		ToolResult{Agent: "iris", Text: "denied", Failed: true},
		TextDelta{"iris", "queda no sul."},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events: %#v", len(events), events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d = %#v, want %#v", i, events[i], want[i])
		}
	}
	if reply.Tokens != 150 || !strings.Contains(reply.Text, "HANDOFF @leo") {
		t.Errorf("reply = %+v", reply)
	}
}

func TestParseStopsAtTokenLimit(t *testing.T) {
	_, _, err := collectStream(t, toolStream, 12) // the first message already uses 15
	if err == nil || !strings.Contains(err.Error(), "token limit") {
		t.Errorf("expected a token limit error, got %v", err)
	}
}

func TestParseErrorResult(t *testing.T) {
	_, _, err := collectStream(t, `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"too many turns"}`+"\n", 0)
	if err == nil || !strings.Contains(err.Error(), "too many turns") {
		t.Errorf("expected the error to surface, got %v", err)
	}
}

func TestParseWithoutResultFails(t *testing.T) {
	if _, _, err := collectStream(t, `{"type":"system","subtype":"init"}`+"\n", 0); err == nil {
		t.Error("a stream that ends without a result should be an error")
	}
}

func TestDescribeCall(t *testing.T) {
	got := describeCall("mcp__tubas__publish_report", json.RawMessage(`{"channel":"#dados","title":"Queda"}`))
	if got != "publish_report(#dados, Queda)" {
		t.Errorf("got %q", got)
	}
}

// The broker turns an HTTP call into an ApprovalRequested event and
// answers with the human's decision.
func TestBrokerRoundTrip(t *testing.T) {
	b, err := NewBroker()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	out := make(chan Event, 1)
	detach := b.Attach(context.Background(), "nico", out)
	defer detach()

	go func() { // play the TUI: approve whatever arrives
		e := <-out
		e.(ApprovalRequested).Reply <- true
	}()

	body, _ := json.Marshal(ApprovalRequest{Agent: "nico", Call: "publish_report(#dados)"})
	req, _ := http.NewRequest("POST", b.URL, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+b.Token)
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var answer ApprovalAnswer
	json.NewDecoder(resp.Body).Decode(&answer)
	if !answer.Approved {
		t.Errorf("expected approved, got %+v", answer)
	}

	// Without the token, nobody can approve.
	bad, _ := http.NewRequest("POST", b.URL, bytes.NewReader(body))
	resp2, err := client.Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 without token, got %d", resp2.StatusCode)
	}
}
