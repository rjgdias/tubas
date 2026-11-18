package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// streamOrJSON lets a gateway that ignores "stream": true still work: a
// whole answer in JSON is played back as the stream it would have been.
func streamOrJSON(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err != nil || req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/messages") ||
		resp.StatusCode != http.StatusOK || strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		return resp, err
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	events, err := messageAsEvents(raw)
	if err != nil {
		return nil, fmt.Errorf("the provider's answer isn't a stream nor a message: %s", firstLine(string(raw)))
	}
	resp.Body = io.NopCloser(bytes.NewReader(events))
	resp.ContentLength = int64(len(events))
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Header.Del("Content-Length")
	return resp, nil
}

// messageAsEvents writes a whole Messages answer as its stream events.
// Text goes as a delta, so it reaches the screen; other blocks go whole.
func messageAsEvents(raw []byte) ([]byte, error) {
	var msg struct {
		ID      string            `json:"id"`
		Model   string            `json:"model"`
		Content []json.RawMessage `json:"content"`
		Stop    *string           `json:"stop_reason"`
		Details json.RawMessage   `json:"stop_details"`
		Usage   map[string]any    `json:"usage"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Content == nil {
		return nil, fmt.Errorf("not a message")
	}
	var out bytes.Buffer
	event := func(v map[string]any) {
		data, _ := json.Marshal(v)
		fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", v["type"], data)
	}
	start := map[string]any{}
	for k, v := range msg.Usage {
		if k != "output_tokens" {
			start[k] = v
		}
	}
	start["output_tokens"] = 0
	event(map[string]any{"type": "message_start", "message": map[string]any{"id": msg.ID, "type": "message", "role": "assistant",
		"model": msg.Model, "content": []any{}, "stop_reason": nil, "usage": start}})
	for i, block := range msg.Content {
		var b map[string]any
		json.Unmarshal(block, &b)
		if b["type"] == "text" {
			event(map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			event(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": b["text"]}})
		} else {
			event(map[string]any{"type": "content_block_start", "index": i, "content_block": json.RawMessage(block)})
		}
		event(map[string]any{"type": "content_block_stop", "index": i})
	}
	stop := "end_turn"
	if msg.Stop != nil {
		stop = *msg.Stop
	}
	delta := map[string]any{"stop_reason": stop}
	if len(msg.Details) > 0 && string(msg.Details) != "null" {
		delta["stop_details"] = msg.Details
	}
	event(map[string]any{"type": "message_delta", "delta": delta, "usage": map[string]any{"output_tokens": msg.Usage["output_tokens"]}})
	event(map[string]any{"type": "message_stop"})
	return out.Bytes(), nil
}
