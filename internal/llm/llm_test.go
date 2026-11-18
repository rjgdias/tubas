package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() { retryBase = time.Millisecond } // tests don't wait seconds between tries

// sse writes server-sent events, as the Messages API streams them.
func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var probe struct {
			Type string `json:"type"`
		}
		json.Unmarshal([]byte(e), &probe)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", probe.Type, e)
	}
}

// anthropicTurn is a streamed answer: thinking with its signature, text,
// then a tool call whose input comes in pieces.
var anthropicTurn = []string{
	`{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0,"cache_read_input_tokens":90,"cache_creation_input_tokens":5}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me look."}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG-123"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Vou "}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ler."}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu1","name":"read","input":{}}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":42}}`,
	`{"type":"message_stop"}`,
}

type recorder struct {
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
}

func (r *recorder) add(req *http.Request) map[string]any {
	data, _ := io.ReadAll(req.Body)
	var body map[string]any
	json.Unmarshal(data, &body)
	r.mu.Lock()
	r.bodies = append(r.bodies, body)
	r.headers = append(r.headers, req.Header.Clone())
	r.mu.Unlock()
	return body
}

func TestAnthropicStreamsAndKeepsThinking(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		sse(w, anthropicTurn...)
	}))
	defer srv.Close()
	c := &Anthropic{Base: srv.URL, Key: "gw-token", Headers: map[string]string{"X-Team": "dados"}}
	var pieces []string
	req := Request{Model: "claude-sonnet-5-5", System: "be brief", Effort: "high",
		Messages: []Message{UserText("leia a.go")},
		Tools:    []Tool{{Name: "read", Description: "read a file", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}}}
	resp, err := c.Stream(context.Background(), req, Events{Text: func(s string) { pieces = append(pieces, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pieces, "") != "Vou ler." || len(pieces) != 2 {
		t.Errorf("text arrives in pieces as written: %q", pieces)
	}
	if resp.Stop != StopToolUse || len(resp.Message.Calls()) != 1 || string(resp.Message.Calls()[0].Input) != `{"path":"a.go"}` {
		t.Errorf("the tool call, its input joined: %+v", resp)
	}
	if resp.Usage != (Usage{Input: 105, Output: 42, CacheRead: 90, CacheWrite: 5}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
	thinking := resp.Message.Blocks[0]
	if thinking.Type != "thinking" || thinking.Text != "Let me look." || !strings.Contains(string(thinking.Raw), "SIG-123") {
		t.Errorf("thinking kept with its signature: %+v", thinking)
	}

	// What was sent: adaptive thinking, effort, the cache, the gateway's
	// bearer token and headers, the schema.
	body, h := rec.bodies[0], rec.headers[0]
	if th, _ := body["thinking"].(map[string]any); th["type"] != "adaptive" || th["display"] != "summarized" {
		t.Errorf("adaptive thinking, summarized: %v", body["thinking"])
	}
	if oc, _ := body["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Errorf("effort sent: %v", body["output_config"])
	}
	if body["cache_control"] == nil || !strings.Contains(fmt.Sprint(body["system"]), "cache_control") {
		t.Errorf("history and system cached: %v / %v", body["cache_control"], body["system"])
	}
	if body["stream"] != true {
		t.Error("it streams")
	}
	if h.Get("Authorization") != "Bearer gw-token" || h.Get("X-Api-Key") != "gw-token" || h.Get("X-Team") != "dados" {
		t.Errorf("a gateway gets the token both ways, and its headers: %v", h)
	}

	// Sent back next turn: the thinking block exactly as it came.
	req.Messages = append(req.Messages, resp.Message, Message{Role: "user", Blocks: []Block{{Type: "tool_result", ID: "tu1", Content: "package main"}}})
	if _, err := c.Stream(context.Background(), req, Events{}); err != nil {
		t.Fatal(err)
	}
	sent, _ := json.Marshal(rec.bodies[1]["messages"])
	if !strings.Contains(string(sent), `"signature":"SIG-123"`) || !strings.Contains(string(sent), `"tool_use_id":"tu1"`) {
		t.Errorf("the next request carries the signed thinking and the tool result:\n%s", sent)
	}
}

func TestAnthropicOfficialAndUnknownModels(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		sse(w, anthropicTurn...)
	}))
	defer srv.Close()
	(&Anthropic{Base: srv.URL, Key: "sk-key", Official: true}).Stream(context.Background(),
		Request{Model: "deepseek-chat", Effort: "high", Messages: []Message{UserText("oi")}}, Events{})
	body, h := rec.bodies[0], rec.headers[0]
	if h.Get("Authorization") != "Bearer sk-key" || h.Get("X-Api-Key") != "" {
		t.Errorf("Anthropic's own API: the key as the SDK sends it, no extra header: %v", h)
	}
	if body["thinking"] != nil || body["output_config"] != nil {
		t.Errorf("a model not in the table gets no thinking or effort: %v %v", body["thinking"], body["output_config"])
	}
	if body["max_tokens"].(float64) != 8192 {
		t.Errorf("unknown model: a safe max_tokens, got %v", body["max_tokens"])
	}
	// A gateway's own name for sonnet takes sonnet's settings.
	(&Anthropic{Base: srv.URL, Key: "k", Like: "sonnet"}).Stream(context.Background(),
		Request{Model: "acme-claude-sonnet", Messages: []Message{UserText("oi")}}, Events{})
	if rec.bodies[1]["thinking"] == nil {
		t.Error("acme-claude-sonnet stands for sonnet: thinking on")
	}
}

func TestRetriesAndSayingSo(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			w.Header().Set("retry-after-ms", "1")
			w.WriteHeader(529)
			fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
		case 2: // text, then the stream breaks
			sse(w, anthropicTurn[:7]...)
			w.(http.Flusher).Flush()
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
		default:
			sse(w, anthropicTurn...)
		}
	}))
	defer srv.Close()
	var retries []Retry
	var text strings.Builder
	resp, err := (&Anthropic{Base: srv.URL, Key: "k"}).Stream(context.Background(),
		Request{Model: "claude-sonnet-5-5", Messages: []Message{UserText("oi")}},
		Events{Text: func(s string) { text.WriteString(s) }, Retry: func(r Retry) { retries = append(retries, r) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(retries) != 2 || retries[0].Status != 529 || retries[0].Reason != "overloaded" || retries[0].Partial {
		t.Fatalf("each retry said, the first an overload: %+v", retries)
	}
	if !retries[1].Partial {
		t.Error("a stream that broke after text says so, to drop that text")
	}
	if resp.Message.Text() != "Vou ler." {
		t.Errorf("the answer of the try that worked: %q", resp.Message.Text())
	}

	// A refused key is not retried.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer bad.Close()
	retries = nil
	_, err = (&Anthropic{Base: bad.URL, Key: "k"}).Stream(context.Background(),
		Request{Model: "claude-sonnet-5-5", Messages: []Message{UserText("oi")}}, Events{Retry: func(r Retry) { retries = append(retries, r) }})
	if err == nil || StatusOf(err) != 401 || len(retries) != 0 {
		t.Errorf("401 at once, no retry: %v %v", err, retries)
	}
}

func TestAGatewayThatRefusesAField(t *testing.T) {
	var seen []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		_, has := body["thinking"]
		seen = append(seen, has)
		if has {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking: Extra inputs are not permitted"}}`)
			return
		}
		sse(w, anthropicTurn...)
	}))
	defer srv.Close()
	c := &Anthropic{Base: srv.URL, Key: "k"}
	req := Request{Model: "claude-sonnet-5-5", Messages: []Message{UserText("oi")}}
	if _, err := c.Stream(context.Background(), req, Events{}); err != nil {
		t.Fatal(err)
	}
	c.Stream(context.Background(), req, Events{})
	if fmt.Sprint(seen) != "[true false false]" {
		t.Errorf("thinking dropped after the 400, and remembered for this gateway: %v", seen)
	}
}

func TestOpenAIStreamsToolCalls(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"model":"deepseek-chat","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
			`{"choices":[{"delta":{"content":"Olha"}}]}`,
			`{"choices":[{"delta":{"content":" isso"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"grep","arguments":"{\"pat"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"tern\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":64}}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	var text, thinking strings.Builder
	msgs := []Message{UserText("acha x"),
		{Role: "assistant", Blocks: []Block{{Type: "thinking", Raw: json.RawMessage(`{"type":"thinking"}`)}, {Type: "tool_use", ID: "c0", Name: "glob", Input: json.RawMessage(`{}`)}}},
		{Role: "user", Blocks: []Block{{Type: "tool_result", ID: "c0", Content: "boom", IsError: true}}}}
	resp, err := (&OpenAI{Base: srv.URL, Key: "sk"}).Stream(context.Background(),
		Request{Model: "deepseek-chat", System: "s", Messages: msgs, Tools: []Tool{{Name: "grep", Schema: json.RawMessage(`{"type":"object"}`)}}},
		Events{Text: func(s string) { text.WriteString(s) }, Thinking: func(s string) { thinking.WriteString(s) }})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Message.Calls()
	if text.String() != "Olha isso" || thinking.String() != "hmm" || len(calls) != 1 || string(calls[0].Input) != `{"pattern":"x"}` || resp.Stop != StopToolUse {
		t.Errorf("streamed text, thinking and a call joined from pieces: %q %q %+v %s", text.String(), thinking.String(), calls, resp.Stop)
	}
	if resp.Usage != (Usage{Input: 100, Output: 20, CacheRead: 64}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
	sent, _ := json.Marshal(rec.bodies[0]["messages"])
	if !strings.Contains(string(sent), `"role":"tool"`) || !strings.Contains(string(sent), `error: boom`) || strings.Contains(string(sent), "thinking") {
		t.Errorf("tool results as tool messages, Anthropic's blocks left out:\n%s", sent)
	}
	if rec.headers[0].Get("Authorization") != "Bearer sk" {
		t.Error("the key as a bearer token")
	}
}

func TestOpenAIWithoutStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"llama3","choices":[{"message":{"content":"oi!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	defer srv.Close()
	resp, err := (&OpenAI{Base: srv.URL}).Stream(context.Background(), Request{Model: "llama3", Messages: []Message{UserText("oi")}}, Events{})
	if err != nil || resp.Message.Text() != "oi!" || resp.Stop != StopEnd {
		t.Errorf("a provider that doesn't stream still answers: %+v %v", resp, err)
	}
}

func TestPrices(t *testing.T) {
	c := Lookup("claude-sonnet-5-5", "")
	// 1M fresh input $2, 1M cache read $0.10, 1M output $10.
	got := c.Price.Cost(Usage{Input: 2_000_000, CacheRead: 1_000_000, Output: 1_000_000})
	if got < 12.09 || got > 12.11 {
		t.Errorf("cost = %v", got)
	}
	if Lookup("llama3", "").Known {
		t.Error("a model not in the table is not known")
	}
}

// A gateway that ignores "stream": true and answers in plain JSON.
func TestAGatewayThatAnswersWithoutStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"m1","model":"acme-sonnet","content":[{"type":"text","text":"vou ver"},`+
			`{"type":"tool_use","id":"t1","name":"read","input":{"path":"a.go"}}],"stop_reason":"tool_use","usage":{"input_tokens":7,"output_tokens":3}}`)
	}))
	defer srv.Close()
	var shown string
	c := &Anthropic{Base: srv.URL, Key: "k"}
	resp, err := c.Stream(context.Background(), Request{Model: "acme-sonnet", Messages: []Message{UserText("oi")}},
		Events{Text: func(s string) { shown += s }})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Message.Calls()
	if shown != "vou ver" || resp.Stop != StopToolUse || len(calls) != 1 || string(calls[0].Input) != `{"path":"a.go"}` ||
		resp.Usage.Input != 7 || resp.Usage.Output != 3 {
		t.Errorf("shown %q, resp %+v", shown, resp)
	}
}
