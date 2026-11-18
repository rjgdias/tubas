// Package llm talks to the models: one message format for tubas, and two
// wire formats underneath, Anthropic's Messages API (Anthropic, company
// gateways, and the Anthropic-compatible DeepSeek, GLM, Kimi, Qwen) and
// OpenAI's chat completions (OpenRouter, Ollama, any OpenAI-compatible
// URL). Answers stream: text reaches the screen as it is written. A
// failing provider is tried again, each attempt said (Retry), and a turn
// cut short (max tokens) carries on.
package llm

import (
	"context"
	"encoding/json"
	"time"
)

// Block is one piece of a message.
type Block struct {
	Type string `json:"type"` // text, thinking, tool_use, tool_result, or another provider block

	Text string `json:"text,omitempty"` // text; thinking (its summary)

	// tool_use: the call; tool_result: the answer to ID.
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Content string          `json:"content,omitempty"`
	IsError bool            `json:"is_error,omitempty"`

	// Raw is the block exactly as Anthropic sent it (thinking with its
	// signature, compaction, server tools): sent back unchanged, as the
	// API requires. Other providers skip what they can't read.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Message is one turn of the conversation: "user" or "assistant".
type Message struct {
	Role   string  `json:"role"`
	Blocks []Block `json:"blocks"`
}

// Text is the message's text, its pieces joined.
func (m Message) Text() string {
	var s string
	for _, b := range m.Blocks {
		if b.Type == "text" && b.Text != "" {
			if s != "" {
				s += "\n"
			}
			s += b.Text
		}
	}
	return s
}

// Calls are the tool calls in an assistant message.
func (m Message) Calls() []Block {
	var out []Block
	for _, b := range m.Blocks {
		if b.Type == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}

// UserText is a user message of plain text.
func UserText(s string) Message {
	return Message{Role: "user", Blocks: []Block{{Type: "text", Text: s}}}
}

// Tool is a tool the model may call: the same JSON Schema serves both
// wire formats.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"input_schema"`
}

// Request is one call to the model.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int    // 0: what the model allows (models.go)
	Effort    string // low, medium, high, xhigh, max; "" the model's own
}

// Usage is what a call read and wrote, in tokens. Input counts all of
// the prompt, cached parts included.
type Usage struct {
	Input, Output, CacheRead, CacheWrite int
}

// Add sums two usages.
func (u Usage) Add(o Usage) Usage {
	return Usage{u.Input + o.Input, u.Output + o.Output, u.CacheRead + o.CacheRead, u.CacheWrite + o.CacheWrite}
}

// Stop reasons, as both formats are read into.
const (
	StopEnd       = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
	StopRefusal   = "refusal"
	StopPause     = "pause_turn"
)

// Response is the model's answer to one Request.
type Response struct {
	Message Message
	Stop    string
	Detail  string // why it stopped, when it says (a refusal's explanation)
	Usage   Usage
	Model   string
}

// Retry says the provider failed and the call is tried again.
type Retry struct {
	Attempt, Max int
	Status       int // HTTP status, 0 when the provider could not be reached
	Reason       string
	Delay        time.Duration
	Partial      bool // text was already shown for this attempt: drop it
}

// Events are what a call reports while it runs.
type Events struct {
	Text     func(string) // a piece of the answer
	Thinking func(string) // a piece of the thinking summary
	Retry    func(Retry)
}

func (e Events) text(s string) {
	if e.Text != nil && s != "" {
		e.Text(s)
	}
}

func (e Events) thinking(s string) {
	if e.Thinking != nil && s != "" {
		e.Thinking(s)
	}
}

// Client is a wire format with its provider: it streams one answer.
type Client interface {
	Stream(ctx context.Context, req Request, ev Events) (Response, error)
}
