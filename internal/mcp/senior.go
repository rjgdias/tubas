package mcp

import (
	"fmt"
	"io"
	"os"
	"strings"

	"tubas/internal/agent"
	"tubas/internal/engine"
	"tubas/internal/team"
)

// ── escalation, in the gateway ──────────────────────────────────────────
//
// The gateway sees every connector call: it notes them for the senior
// (engine.Senior), brings its advice with a second failure, has it review
// a write before you approve it, and offers ask_senior.

// newSenior is a's escalation, running the stronger model natively on
// a's provider, as the team's agents do.
func newSenior(a agent.Agent, t team.Team) *engine.Senior {
	eng := engine.NativeEngine{Setup: engine.FindProvider(t.Dir), Providers: TeamProviders(t), DefaultProvider: t.Provider, WorkDir: t.Dir}
	return engine.NewSenior(a, t.Dir, eng)
}

// TeamProviders are the built-in model providers, yours, and team.yaml's.
func TeamProviders(t team.Team) map[string]engine.ProviderSpec {
	own := map[string]engine.ProviderSpec{}
	for name, p := range t.Providers {
		prices := map[string]engine.Price{}
		for model, pr := range p.Prices {
			prices[model] = engine.Price{Input: pr[0], Cached: pr[1], Output: pr[2]}
		}
		own[name] = engine.ProviderSpec{Anthropic: p.Anthropic, OpenAI: p.OpenAI, KeyEnv: p.KeyEnv, Models: p.Models, Prices: prices}
	}
	return engine.Providers(own)
}

func askTool(s *engine.Senior) tool {
	return textTool("ask_senior",
		fmt.Sprintf("Ask a stronger model (%s) for help on a hard step: when you are stuck, unsure what to try next, or about to do something risky. "+
			"It sees your calls so far. Use it rarely: it costs more.", s.Cfg.To),
		object(map[string]any{"question": str("what you need, with what you tried")}, "question"), true,
		func(args map[string]any) (string, error) {
			return s.Ask("The agent asks: " + text(args, "question")), nil
		})
}

// resultText is the text inside an MCP result, for the trail.
func resultText(result any) (string, bool) {
	m, ok := result.(map[string]any)
	if !ok {
		return "", false
	}
	failed, _ := m["isError"].(bool)
	var parts []string
	switch content := m["content"].(type) {
	case []map[string]any:
		for _, c := range content {
			if t, ok := c["text"].(string); ok {
				parts = append(parts, t)
			}
		}
	case []any:
		for _, c := range content {
			if cm, ok := c.(map[string]any); ok {
				if t, ok := cm["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
	}
	return strings.Join(parts, "\n"), failed
}

// withNote adds a text block to a result, keeping what was there.
func withNote(result any, note string) any {
	m, ok := result.(map[string]any)
	if !ok {
		return result
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	block := map[string]any{"type": "text", "text": note}
	switch content := m["content"].(type) {
	case []map[string]any:
		out["content"] = append(append([]map[string]any{}, content...), block)
	case []any:
		out["content"] = append(append([]any{}, content...), block)
	default:
		out["content"] = []map[string]any{block}
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readCapped reads at most n bytes of a text file.
func readCapped(path string, n int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return "", err
	}
	return string(data), nil
}
