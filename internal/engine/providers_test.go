package engine

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"tubas/internal/agent"
)

// providerTurn runs one turn with the fake claude and returns what it saw.
func providerTurn(t *testing.T, eng ClaudeEngine, a agent.Agent) (map[string]string, Reply, error) {
	t.Helper()
	exe, _ := os.Executable()
	t.Setenv("TUBAS_FAKE_CLAUDE", "provider")
	eng.Command, eng.WorkDir = exe, t.TempDir()
	reply, err := eng.RunTurn(context.Background(), TurnRequest{Agent: a, Prompt: "hi"}, make(chan Event, 16))
	if err != nil {
		return nil, reply, err
	}
	var seen map[string]string
	if err := json.Unmarshal([]byte(reply.Text), &seen); err != nil {
		t.Fatalf("fake claude said %q", reply.Text)
	}
	return seen, reply, nil
}

func TestAnAgentRunsOnDeepSeek(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "ds-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key") // must not reach DeepSeek
	eng := ClaudeEngine{Providers: Providers(nil), Plan: true}
	a := agent.Agent{Name: "leo", Model: "sonnet", Provider: "deepseek", Instructions: "x"}
	seen, reply, err := providerTurn(t, eng, a)
	if err != nil {
		t.Fatal(err)
	}
	if seen["base"] != "https://api.deepseek.com/anthropic" || seen["token"] != "ds-key" || seen["apikey"] != "" {
		t.Errorf("claude should talk to DeepSeek with its key: %v", seen)
	}
	if seen["model"] != "deepseek-v4-pro" || seen["envmodel"] != "deepseek-v4-pro" {
		t.Errorf("a Claude alias becomes the provider's default model: %v", seen)
	}
	if reply.Plan || reply.Cost != 0 || reply.Model != "deepseek-v4-pro" {
		t.Errorf("a DeepSeek turn is not billed to the Claude plan, nor priced by claude: %+v", reply)
	}
}

func TestTheTeamProviderAndYourOwnSetup(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "z-key")
	eng := ClaudeEngine{Providers: Providers(nil), DefaultProvider: "glm", Plan: true}
	seen, _, err := providerTurn(t, eng, agent.Agent{Name: "iris", Model: "glm-5.3-flash", Instructions: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if seen["base"] != "https://api.z.ai/api/anthropic" || seen["model"] != "glm-5.3-flash" {
		t.Errorf("an agent with no provider uses the team's, with its own model: %v", seen)
	}
	if eng.Name() != "claude · glm" {
		t.Errorf("the header names the team's provider: %q", eng.Name())
	}
	// provider: anthropic is your Claude login, billed to the plan.
	_, reply, err := providerTurn(t, eng, agent.Agent{Name: "nico", Model: "haiku", Provider: "anthropic", Instructions: "x"})
	if err != nil || !reply.Plan || reply.Model != "haiku" {
		t.Errorf("your own setup: %+v, %v", reply, err)
	}
}

func TestProviderErrorsSayWhatToDo(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "")
	eng := ClaudeEngine{Providers: Providers(nil)}
	_, _, err := providerTurn(t, eng, agent.Agent{Name: "a", Provider: "kimi", Instructions: "x"})
	if err == nil || !strings.Contains(err.Error(), "MOONSHOT_API_KEY") {
		t.Errorf("a missing key names its variable: %v", err)
	}
	_, _, err = providerTurn(t, eng, agent.Agent{Name: "a", Provider: "nope", Instructions: "x"})
	if err == nil || !strings.Contains(err.Error(), "deepseek") {
		t.Errorf("an unknown provider lists the known ones: %v", err)
	}
	_, _, err = providerTurn(t, eng, agent.Agent{Name: "a", Provider: "openrouter", Instructions: "x"})
	if err == nil || !strings.Contains(err.Error(), "harness: api") {
		t.Errorf("a provider without an Anthropic endpoint points to the api harness: %v", err)
	}
}
