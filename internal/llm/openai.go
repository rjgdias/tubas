package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAI is the chat completions API: OpenRouter, Ollama, LM Studio, and
// the OpenAI-compatible side of DeepSeek, Kimi, Qwen, GLM.
type OpenAI struct {
	Base    string
	Key     string
	Headers map[string]string
	HTTP    *http.Client
}

// Stream asks for the next answer, streaming its text.
func (c *OpenAI) Stream(ctx context.Context, req Request, ev Events) (Response, error) {
	return withRetries(ctx, ev, func() (Response, bool, error) { return c.once(ctx, req, ev) })
}

func (c *OpenAI) body(req Request) map[string]any {
	msgs := []map[string]any{}
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, toOpenAI(m)...)
	}
	body := map[string]any{"model": req.Model, "messages": msgs, "stream": true,
		"stream_options": map[string]any{"include_usage": true}}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": orEmptyObject(t.Schema)}})
		}
		body["tools"] = tools
	}
	return body
}

// toOpenAI turns one tubas message into the API's: tool results become
// "tool" messages; thinking and other provider blocks are left out.
func toOpenAI(m Message) []map[string]any {
	var out []map[string]any
	if m.Role == "assistant" {
		msg := map[string]any{"role": "assistant", "content": m.Text()}
		var calls []map[string]any
		for _, b := range m.Calls() {
			calls = append(calls, map[string]any{"id": b.ID, "type": "function",
				"function": map[string]string{"name": b.Name, "arguments": string(orEmptyObject(b.Input))}})
		}
		if len(calls) > 0 {
			msg["tool_calls"] = calls
		}
		return append(out, msg)
	}
	var texts []string
	for _, b := range m.Blocks {
		switch b.Type {
		case "tool_result":
			content := orNone(b.Content)
			if b.IsError {
				content = "error: " + content
			}
			out = append(out, map[string]any{"role": "tool", "tool_call_id": b.ID, "content": content})
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
	}
	if len(texts) > 0 {
		out = append(out, map[string]any{"role": "user", "content": strings.Join(texts, "\n")})
	}
	return out
}

type openAIChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning_content"` // DeepSeek, Qwen: the thinking
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		Message *struct { // an answer that didn't stream
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		Finish string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
		Details    struct {
			Cached int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func (c *OpenAI) once(ctx context.Context, req Request, ev Events) (Response, bool, error) {
	data, _ := json.Marshal(c.body(req))
	httpReq, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Base, "/")+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return Response{}, false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("X-Title", "tubas") // how OpenRouter and others name the app
	if c.Key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Key)
	}
	for k, v := range c.Headers {
		httpReq.Header.Set(k, v)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return Response{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Response{}, false, &HTTPError{Status: resp.StatusCode, Body: firstLine(string(body)), RetryAfter: retryAfter(resp.Header)}
	}

	type call struct{ id, name, args string }
	var calls []*call
	var text, thinking strings.Builder
	out := Response{Message: Message{Role: "assistant"}}
	partial := false
	read := func(chunk openAIChunk) {
		if chunk.Model != "" {
			out.Model = chunk.Model
		}
		if u := chunk.Usage; u != nil {
			out.Usage = Usage{Input: u.Prompt, Output: u.Completion, CacheRead: u.Details.Cached}
		}
		for _, ch := range chunk.Choices {
			if ch.Message != nil { // the whole answer at once
				text.WriteString(ch.Message.Content)
				ev.text(ch.Message.Content)
				for _, tc := range ch.Message.ToolCalls {
					calls = append(calls, &call{tc.ID, tc.Function.Name, tc.Function.Arguments})
				}
			}
			if ch.Delta.Content != "" {
				partial = true
				text.WriteString(ch.Delta.Content)
				ev.text(ch.Delta.Content)
			}
			if ch.Delta.Reasoning != "" {
				thinking.WriteString(ch.Delta.Reasoning)
				ev.thinking(ch.Delta.Reasoning)
			}
			for _, tc := range ch.Delta.ToolCalls {
				for len(calls) <= tc.Index {
					calls = append(calls, &call{})
				}
				c := calls[tc.Index]
				if tc.ID != "" {
					c.id = tc.ID
				}
				c.name += tc.Function.Name
				c.args += tc.Function.Arguments
			}
			if ch.Finish != "" {
				out.Stop = openAIStop(ch.Finish)
			}
		}
	}

	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// A provider that answered without streaming.
		var chunk openAIChunk
		if err := json.NewDecoder(io.LimitReader(resp.Body, 20<<20)).Decode(&chunk); err != nil {
			return Response{}, false, err
		}
		read(chunk)
	} else {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		done := false
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				done = true
				break
			}
			var chunk openAIChunk
			if json.Unmarshal([]byte(payload), &chunk) == nil {
				read(chunk)
			}
		}
		if err := sc.Err(); err != nil {
			return Response{}, partial, err
		}
		if !done && out.Stop == "" {
			return Response{}, partial, errors.New("the answer stopped half-way")
		}
	}

	if thinking.Len() > 0 {
		out.Message.Blocks = append(out.Message.Blocks, Block{Type: "thinking", Text: thinking.String()})
	}
	if text.Len() > 0 {
		out.Message.Blocks = append(out.Message.Blocks, Block{Type: "text", Text: text.String()})
	}
	for _, c := range calls {
		if c.name == "" {
			continue
		}
		out.Message.Blocks = append(out.Message.Blocks, Block{Type: "tool_use", ID: c.id, Name: c.name, Input: json.RawMessage(orEmptyObject(json.RawMessage(c.args)))})
	}
	if out.Stop == "" {
		out.Stop = StopEnd
		if len(out.Message.Calls()) > 0 {
			out.Stop = StopToolUse
		}
	}
	return out, partial, nil
}

// openAIStop reads finish_reason as tubas's stop reasons.
func openAIStop(reason string) string {
	switch reason {
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	case "content_filter":
		return StopRefusal
	}
	return StopEnd
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
