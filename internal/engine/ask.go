package engine

import (
	"context"
	"errors"
	"strings"

	"tubas/internal/agent"
	"tubas/internal/llm"
)

// Ask is one call to a's model, with no tools: the router's scoring, a
// senior's advice. It returns the answer and what it cost.
func (e NativeEngine) Ask(ctx context.Context, a agent.Agent, system, prompt string) (string, float64, error) {
	client, model, like, _, err := e.clientFor(a)
	if err != nil {
		return "", 0, err
	}
	caps := llm.Lookup(model, like)
	resp, err := client.Stream(ctx, llm.Request{Model: model, System: system, MaxTokens: 4096, Effort: "low",
		Messages: []llm.Message{llm.UserText(prompt)}}, llm.Events{})
	if err != nil {
		return "", 0, explainProvider(err)
	}
	text := strings.TrimSpace(resp.Message.Text())
	if text == "" {
		return "", 0, errors.New("the model gave no answer")
	}
	return text, caps.Price.Cost(resp.Usage), nil
}
