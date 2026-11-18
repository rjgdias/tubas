package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Anthropic is the Messages API, through the official SDK: Anthropic's
// own, or a company gateway in front of it.
type Anthropic struct {
	Base     string
	Key      string
	Official bool              // Anthropic's API: the key the SDK's way; a gateway also gets it as x-api-key
	Headers  map[string]string // ANTHROPIC_CUSTOM_HEADERS
	Like     string            // the Claude model the request's model stands for, on a gateway that renames them
	HTTP     *http.Client
}

// refused are the request fields a gateway answered 400 to, by base URL:
// a gateway in front of an older API may not know thinking or effort, and
// the next requests go without them.
var refused sync.Map // base URL → map[string]bool

func (c *Anthropic) refusedFields() map[string]bool {
	if v, ok := refused.Load(c.Base); ok {
		return v.(map[string]bool)
	}
	return map[string]bool{}
}

// optionalFields are what a request may go without.
var optionalFields = []string{"thinking", "output_config", "cache_control"}

// Stream asks for the next answer, streaming its text.
func (c *Anthropic) Stream(ctx context.Context, req Request, ev Events) (Response, error) {
	for {
		resp, err := withRetries(ctx, ev, func() (Response, bool, error) { return c.once(ctx, req, ev) })
		if err != nil && StatusOf(err) == 400 {
			if field := c.blame(err); field != "" {
				continue // the same request, without that field
			}
		}
		return resp, err
	}
}

// blame finds the optional field a 400 is about and leaves it out from
// now on, for this gateway.
func (c *Anthropic) blame(err error) string {
	msg := strings.ToLower(err.Error())
	have := c.refusedFields()
	for _, f := range optionalFields {
		if !have[f] && (strings.Contains(msg, f) || (f == "output_config" && strings.Contains(msg, "effort"))) {
			next := map[string]bool{f: true}
			for k := range have {
				next[k] = true
			}
			refused.Store(c.Base, next)
			return f
		}
	}
	return ""
}

func (c *Anthropic) client() anthropic.Client {
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(), // tubas resolves keys and URLs itself
		option.WithBaseURL(c.Base),
		option.WithMaxRetries(0), // tubas retries, and says so on screen
		option.WithMiddleware(streamOrJSON),
	}
	if c.HTTP != nil {
		opts = append(opts, option.WithHTTPClient(c.HTTP))
	}
	if c.Official {
		opts = append(opts, option.WithAPIKey(c.Key))
	} else if c.Key != "" {
		opts = append(opts, option.WithAuthToken(c.Key), option.WithHeader("x-api-key", c.Key))
	}
	for k, v := range c.Headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	return anthropic.NewClient(opts...)
}

// params builds the request: cached system and history, adaptive
// thinking and effort where the model takes them.
func (c *Anthropic) params(req Request) (anthropic.MessageNewParams, error) {
	caps := Lookup(req.Model, c.Like)
	skip := c.refusedFields()
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = caps.MaxOutput
	}
	msgs, err := toAnthropic(req.Messages)
	if err != nil {
		return anthropic.MessageNewParams{}, err
	}
	p := anthropic.MessageNewParams{Model: anthropic.Model(req.Model), MaxTokens: int64(maxTokens), Messages: msgs}
	if req.System != "" {
		sys := anthropic.TextBlockParam{Text: req.System}
		if !skip["cache_control"] {
			sys.CacheControl = anthropic.NewCacheControlEphemeralParam() // tools and system: they don't change in a conversation
		}
		p.System = []anthropic.TextBlockParam{sys}
	}
	if !skip["cache_control"] {
		p.CacheControl = anthropic.NewCacheControlEphemeralParam() // and the history, as it grows
	}
	for _, t := range req.Tools {
		p.Tools = append(p.Tools, toolParam(t))
	}
	if caps.Adaptive && !skip["thinking"] {
		p.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{
			Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized}}
	}
	if caps.Effort && req.Effort != "" && !skip["output_config"] {
		p.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(req.Effort)}
	}
	return p, nil
}

// once is one attempt; partial says whether text was already shown.
func (c *Anthropic) once(ctx context.Context, req Request, ev Events) (Response, bool, error) {
	p, err := c.params(req)
	if err != nil {
		return Response{}, false, err
	}
	client := c.client()
	stream := client.Messages.NewStreaming(ctx, p)
	defer stream.Close()
	var acc anthropic.Message
	partial := false
	for stream.Next() {
		event := stream.Current()
		if err := acc.Accumulate(event); err != nil {
			return Response{}, partial, err
		}
		if delta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			switch d := delta.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				partial = partial || d.Text != ""
				ev.text(d.Text)
			case anthropic.ThinkingDelta:
				ev.thinking(d.Thinking)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return Response{}, partial, err
	}
	return fromAnthropic(acc), partial, nil
}

// toAnthropic turns tubas's messages into the API's. A block Anthropic
// sent (thinking, compaction) goes back exactly as it came.
func toAnthropic(msgs []Message) ([]anthropic.MessageParam, error) {
	var out []anthropic.MessageParam
	for _, m := range msgs {
		var blocks []anthropic.ContentBlockParamUnion
		for _, b := range m.Blocks {
			if len(b.Raw) > 0 {
				var u anthropic.ContentBlockParamUnion
				if err := json.Unmarshal(b.Raw, &u); err != nil {
					return nil, err
				}
				blocks = append(blocks, u)
				continue
			}
			switch b.Type {
			case "text":
				if b.Text != "" {
					blocks = append(blocks, anthropic.NewTextBlock(b.Text))
				}
			case "tool_use":
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, orEmptyObject(b.Input), b.Name))
			case "tool_result":
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ID, orNone(b.Content), b.IsError))
			}
		}
		if len(blocks) == 0 {
			continue
		}
		if m.Role == "assistant" {
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		} else {
			out = append(out, anthropic.NewUserMessage(blocks...))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("nothing to send")
	}
	return out, nil
}

// fromAnthropic reads the API's answer into tubas's message.
func fromAnthropic(acc anthropic.Message) Response {
	msg := Message{Role: "assistant"}
	for _, blk := range acc.Content {
		switch v := blk.AsAny().(type) {
		case anthropic.TextBlock:
			msg.Blocks = append(msg.Blocks, Block{Type: "text", Text: v.Text})
		case anthropic.ToolUseBlock:
			msg.Blocks = append(msg.Blocks, Block{Type: "tool_use", ID: v.ID, Name: v.Name, Input: v.Input})
		default:
			// Thinking, server tools, compaction: kept as sent.
			raw, _ := json.Marshal(blk.ToParam())
			b := Block{Type: blk.Type, Raw: raw}
			if t, ok := v.(anthropic.ThinkingBlock); ok {
				b.Text = t.Thinking
			}
			msg.Blocks = append(msg.Blocks, b)
		}
	}
	u := acc.Usage
	return Response{
		Message: msg,
		Stop:    string(acc.StopReason),
		Detail:  acc.StopDetails.Explanation,
		Model:   string(acc.Model),
		Usage: Usage{
			Input:      int(u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens),
			Output:     int(u.OutputTokens),
			CacheRead:  int(u.CacheReadInputTokens),
			CacheWrite: int(u.CacheCreationInputTokens),
		},
	}
}

// toolParam turns a JSON Schema tool into the API's.
func toolParam(t Tool) anthropic.ToolUnionParam {
	var schema map[string]any
	json.Unmarshal(orEmptyObject(t.Schema), &schema)
	s := anthropic.ToolInputSchemaParam{Properties: schema["properties"], ExtraFields: map[string]any{}}
	if s.Properties == nil {
		s.Properties = map[string]any{}
	}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if name, ok := r.(string); ok {
				s.Required = append(s.Required, name)
			}
		}
	}
	for k, v := range schema {
		if k != "properties" && k != "required" && k != "type" {
			s.ExtraFields[k] = v
		}
	}
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{Name: t.Name, Description: anthropic.String(t.Description), InputSchema: s}}
}

func orEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func orNone(s string) string {
	if s == "" {
		return "(no output)"
	}
	return s
}
