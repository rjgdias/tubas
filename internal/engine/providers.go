package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"
)

// ── model providers ─────────────────────────────────────────────────────
//
// An agent's harness is what runs its loop (claude, codex…); its provider
// is where its model is served. DeepSeek, GLM, Kimi and Qwen serve
// Anthropic-compatible endpoints, so the claude harness runs on them by
// pointing Claude Code there (ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN and
// the model names). Their OpenAI-compatible endpoints are for the api
// harness. Keys are read from environment variables, never from files.

// ProviderSpec is where models are served.
type ProviderSpec struct {
	Name      string
	Anthropic string   // Anthropic-compatible base URL: the claude harness can use it
	OpenAI    string   // OpenAI-compatible base URL: the api harness uses it
	KeyEnv    string   // the environment variable holding the key ("" = none needed)
	Models    []string // the first is the default, the last the cheap one for routing
	Prices    map[string]Price
}

// Anthropic is your own Claude setup: Claude's login, or the gateway,
// Bedrock or Vertex your Claude settings name.
const Anthropic = "anthropic"

// BuiltinProviders are known out of the box (base URLs and models from
// each provider's Claude Code guide, late 2026).
var BuiltinProviders = map[string]ProviderSpec{
	"deepseek": {Anthropic: "https://api.deepseek.com/anthropic", OpenAI: "https://api.deepseek.com",
		KeyEnv: "DEEPSEEK_API_KEY", Models: []string{"deepseek-v4-pro", "deepseek-flash"}},
	"glm": {Anthropic: "https://api.z.ai/api/anthropic", OpenAI: "https://api.z.ai/api/paas/v4",
		KeyEnv: "ZAI_API_KEY", Models: []string{"glm-5.3", "glm-5.3-flash"}},
	"kimi": {Anthropic: "https://api.moonshot.ai/anthropic", OpenAI: "https://api.moonshot.ai/v1",
		KeyEnv: "MOONSHOT_API_KEY", Models: []string{"kimi-k3"}},
	"qwen": {Anthropic: "https://dashscope-intl.aliyuncs.com/apps/anthropic", OpenAI: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		KeyEnv: "DASHSCOPE_API_KEY", Models: []string{"qwen3.7-max", "qwen3.7-plus"}},
	"openrouter": {OpenAI: "https://openrouter.ai/api/v1", KeyEnv: "OPENROUTER_API_KEY"},
	"openai":     {OpenAI: "https://api.openai.com/v1", KeyEnv: "OPENAI_API_KEY"},
	"gemini":     {OpenAI: "https://generativelanguage.googleapis.com/v1beta/openai", KeyEnv: "GEMINI_API_KEY"},
	"ollama":     {OpenAI: "http://localhost:11434/v1"},
}

// Providers are the built-in providers with the team's on top.
func Providers(team map[string]ProviderSpec) map[string]ProviderSpec {
	out := map[string]ProviderSpec{}
	for name, p := range BuiltinProviders {
		p.Name = name
		out[name] = p
	}
	for name, p := range team {
		p.Name = name
		out[name] = p
	}
	return out
}

// ProviderNames are the providers, sorted, anthropic first.
func ProviderNames(all map[string]ProviderSpec) []string {
	names := []string{Anthropic}
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names[1:])
	return names
}

// Key is the provider's key from the environment, and whether one is
// needed and missing.
func (p ProviderSpec) Key() (key string, missing bool) {
	if p.KeyEnv == "" {
		return "", false
	}
	key = os.Getenv(p.KeyEnv)
	return key, key == ""
}

// FastModel is the cheap model, for the router.
func (p ProviderSpec) FastModel() string {
	if len(p.Models) == 0 {
		return ""
	}
	return p.Models[len(p.Models)-1]
}

// ClaudeProvider points Claude Code at this provider's Anthropic-compatible
// endpoint, with model for every model name Claude Code may ask for.
func (p ProviderSpec) ClaudeProvider(model string) (Provider, error) {
	if p.Anthropic == "" {
		return Provider{}, fmt.Errorf("%s has no Anthropic-compatible endpoint: give the agent harness: api", p.Name)
	}
	key, missing := p.Key()
	if missing {
		return Provider{}, fmt.Errorf("%s needs its key: set the environment variable %s", p.Name, p.KeyEnv)
	}
	if model == "" && len(p.Models) > 0 {
		model = p.Models[0]
	}
	env := map[string]string{
		"ANTHROPIC_BASE_URL":   p.Anthropic,
		"ANTHROPIC_AUTH_TOKEN": key,
		"ANTHROPIC_API_KEY":    "", // an Anthropic key would win over the token
		// Your Claude settings may point somewhere else: not for this agent.
		"CLAUDE_CODE_USE_BEDROCK": "", "CLAUDE_CODE_USE_VERTEX": "", "CLAUDE_CODE_USE_FOUNDRY": "",
	}
	if model != "" {
		for _, k := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
			env[k] = model
		}
	}
	return Provider{Env: env}, nil
}

// ClaudeAuth asks claude how you are logged in. plan is true for a
// Claude subscription (turns are billed to the plan, not per token), as
// tucano reads it.
func ClaudeAuth(command string) (loggedIn, plan bool, detail string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "auth", "status").Output()
	if err != nil {
		return false, false, ""
	}
	var s struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		APIProvider      string `json:"apiProvider"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal(out, &s) != nil {
		return false, false, ""
	}
	plan = s.AuthMethod == "claude.ai" && (s.APIProvider == "" || s.APIProvider == "firstParty")
	detail = s.AuthMethod
	if s.SubscriptionType != "" {
		detail = "plan " + s.SubscriptionType
	}
	return s.LoggedIn, plan, detail
}
