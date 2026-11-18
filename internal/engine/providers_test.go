package engine

import (
	"strings"
	"testing"

	"tubas/internal/agent"
	"tubas/internal/llm"
)

func TestAnAgentRunsOnDeepSeek(t *testing.T) {
	t.Setenv("TUBAS_DATA_DIR", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "ds-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key") // must not reach DeepSeek
	eng := NativeEngine{Providers: Providers(nil)}
	client, model, _, setup, err := eng.clientFor(agent.Agent{Name: "leo", Model: "sonnet", Provider: "deepseek"})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := client.(*llm.Anthropic)
	if !ok || c.Base != "https://api.deepseek.com/anthropic" || c.Key != "ds-key" || setup {
		t.Errorf("DeepSeek's Anthropic endpoint with its own key: %+v", client)
	}
	if model != "deepseek-v4-pro" {
		t.Errorf("a Claude alias becomes the provider's default model: %s", model)
	}
}

func TestTheTeamProviderAndOpenAIOnes(t *testing.T) {
	t.Setenv("TUBAS_DATA_DIR", t.TempDir())
	t.Setenv("ZAI_API_KEY", "z-key")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	eng := NativeEngine{Providers: Providers(nil), DefaultProvider: "glm"}
	_, model, _, _, err := eng.clientFor(agent.Agent{Name: "iris", Model: "glm-5.3-flash"})
	if err != nil || model != "glm-5.3-flash" {
		t.Errorf("an agent with no provider uses the team's, with its own model: %s %v", model, err)
	}
	client, model, _, _, err := eng.clientFor(agent.Agent{Name: "o", Provider: "openrouter", Model: "x-ai/grok-5"})
	if c, ok := client.(*llm.OpenAI); err != nil || !ok || c.Key != "or-key" || model != "x-ai/grok-5" {
		t.Errorf("a provider with only an OpenAI endpoint: %+v %s %v", client, model, err)
	}
}

func TestProviderErrorsSayWhatToDo(t *testing.T) {
	t.Setenv("TUBAS_DATA_DIR", t.TempDir())
	t.Setenv("MOONSHOT_API_KEY", "")
	eng := NativeEngine{Providers: Providers(nil)}
	_, _, _, _, err := eng.clientFor(agent.Agent{Name: "a", Provider: "kimi"})
	if err == nil || !strings.Contains(err.Error(), "MOONSHOT_API_KEY") || !strings.Contains(err.Error(), "/connect") {
		t.Errorf("a missing key says /connect and names its variable: %v", err)
	}
	_, _, _, _, err = eng.clientFor(agent.Agent{Name: "a", Provider: "nope"})
	if err == nil || !strings.Contains(err.Error(), "/connect") {
		t.Errorf("an unknown provider points to /connect: %v", err)
	}
	_, _, _, _, err = eng.clientFor(agent.Agent{Name: "a", Provider: "openrouter"})
	if err == nil {
		t.Error("openrouter has no default model: the agent must say one")
	}
}
