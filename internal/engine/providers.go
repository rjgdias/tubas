package engine

import (
	"fmt"
	"os"
	"sort"
	"tubas/internal/auth"
)

// ── model providers ─────────────────────────────────────────────────────
//
// An agent's provider is where its model is served: your Anthropic key or
// company gateway (anthropic), DeepSeek, GLM, Kimi, Qwen, OpenRouter,
// OpenAI, Gemini, Ollama, or your own server. tubas speaks Anthropic's
// Messages API where a provider serves it, else OpenAI's. Keys come from
// an environment variable or /connect (the system's keychain), never from
// a team file.

// ProviderSpec is where models are served.
type ProviderSpec struct {
	Name      string
	Anthropic string   // Anthropic-compatible base URL: used first
	OpenAI    string   // OpenAI-compatible base URL
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

// Providers are the built-in providers, the ones you added with /connect
// and the team's, each on top of the one before.
func Providers(team map[string]ProviderSpec) map[string]ProviderSpec {
	out := map[string]ProviderSpec{}
	for name, p := range BuiltinProviders {
		p.Name = name
		out[name] = p
	}
	for name, c := range auth.Customs() {
		out[name] = ProviderSpec{Name: name, OpenAI: c.OpenAI, Models: c.Models}
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

// Key is the provider's key: from its environment variable, else the one
// you saved with /connect. missing says one is needed and there is none.
func (p ProviderSpec) Key() (key string, missing bool) {
	if p.KeyEnv != "" {
		if key = os.Getenv(p.KeyEnv); key != "" {
			return key, false
		}
	}
	if key = auth.Get(p.Name); key != "" {
		return key, false
	}
	return "", p.KeyEnv != ""
}

// NeedsKey says what to do about a missing key.
func (p ProviderSpec) NeedsKey() error {
	return fmt.Errorf("%s needs its key: /connect saves one, or set the environment variable %s", p.Name, p.KeyEnv)
}

// FastModel is the cheap model, for the router.
func (p ProviderSpec) FastModel() string {
	if len(p.Models) == 0 {
		return ""
	}
	return p.Models[len(p.Models)-1]
}
