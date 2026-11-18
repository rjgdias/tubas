package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tubas/internal/agent"
)

// The router is one call to the team provider's fast model, which answers
// with a JSON object; it can't add anyone.
func TestTheNativeRouterScoresTheAgents(t *testing.T) {
	srv, bodies := historyProvider(t, func(body map[string]any, last string) string {
		return `{"content":[{"type":"text","text":"Sure: {\"scores\": {\"iris\": 0.9, \"leo\": 0.2, \"mallory\": 1}, \"for_everyone\": 0.1, \"reason\": \"dados de vendas\"}"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":30,"output_tokens":20}}`
	})
	t.Setenv("TUBAS_DATA_DIR", t.TempDir())
	t.Setenv("ACME_KEY", "k")
	eng := NativeEngine{Providers: Providers(map[string]ProviderSpec{"acme": {Anthropic: srv.URL, KeyEnv: "ACME_KEY", Models: []string{"acme-big", "acme-fast"}}})}
	r := NativeRouter{Engine: eng, Provider: "acme"}
	d, err := r.Route(context.Background(), "como foram as vendas?", "", []agent.Agent{{Name: "iris", Role: "analyst"}, {Name: "leo", Role: "engineer"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Scores["iris"] != 0.9 || d.Scores["leo"] != 0.2 || len(d.Scores) != 2 || d.Reason != "dados de vendas" {
		t.Errorf("decision: %+v", d)
	}
	b := bodies()[0]
	if b["model"] != "acme-fast" || !strings.Contains(jsonText(b["messages"]), "como foram as vendas?") || b["tools"] != nil {
		t.Errorf("the fast model, the message, no tools: %v", b)
	}
}

// before_write: a stronger model reviews each command before you see it.
// tool_fails: the second failure of a tool comes with its advice.
func TestTheSeniorInTheNativeLoop(t *testing.T) {
	step := 0
	srv, _ := historyProvider(t, func(body map[string]any, last string) string {
		if body["model"] == "acme-big" {
			return `{"content":[{"type":"text","text":"check the path with glob first"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
		}
		step++
		if step <= 2 {
			return `{"content":[{"type":"tool_use","id":"t` + string(rune('0'+step)) + `","name":"read","input":{"path":"nope.txt"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`
		}
		return `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
	})
	t.Setenv("TUBAS_DATA_DIR", t.TempDir())
	t.Setenv("ACME_KEY", "k")
	team := t.TempDir()
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644)
	eng := NativeEngine{Providers: Providers(map[string]ProviderSpec{"acme": {Anthropic: srv.URL, KeyEnv: "ACME_KEY", Models: []string{"acme-small"}}}),
		DefaultProvider: "acme", WorkDir: team}
	a := agent.Agent{Name: "dev", Model: "acme-small", Instructions: "x", Tools: map[string]string{"app": "read"},
		Escalate: &agent.Escalation{To: "acme-big", On: []string{"tool_fails"}}}
	events, reply := turn(t, eng, TurnRequest{Agent: a, Prompt: "read nope", Repos: map[string]string{"app": repo}})
	if reply.Text != "done" {
		t.Fatalf("reply %q", reply.Text)
	}
	var results []string
	for _, e := range events {
		if r, ok := e.(ToolResult); ok {
			results = append(results, r.Text)
		}
	}
	if len(results) != 2 || strings.Contains(results[0], "↑") || !strings.Contains(results[1], "↑ acme-big") || !strings.Contains(results[1], "glob first") {
		t.Errorf("advice with the second failure only: %q", results)
	}
	if EscalationsToday(team, "dev") != 1 {
		t.Errorf("counted: %d", EscalationsToday(team, "dev"))
	}
}
