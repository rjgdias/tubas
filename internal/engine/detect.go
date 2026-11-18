package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Detection looks around this computer for ways to reach a model, for
// the settings screen and the first run: does your Claude setup point to
// a work gateway, are there API keys in your shell, is Ollama running,
// is there a Claude Code login (which tubas can't use, and says why). It
// only looks: it never prints, stores or sends a key.

// Detected is one way to reach a model that tubas found.
type Detected struct {
	Name      string   // "gateway", "openrouter", "openai", "anthropic-key", "gemini", "ollama", "claude-login"
	Ready     bool     // can be used now
	Detail    string   // one line for people
	Usable    bool     // tubas can run agents with it
	Models    []string // the models it serves, when it says (a gateway's /v1/models)
	ModelsErr string   // why they could not be listed
}

// The outside world, as variables so tests can stand in for it.
var (
	lookPath      = exec.LookPath
	ollamaURL     = "http://127.0.0.1:11434"
	gatewayModels = GatewayModels
	claudeHome    = func() string { home, _ := os.UserHomeDir(); return filepath.Join(home, ".claude") }
)

const ollamaTimeout = time.Second // a local server answers fast or not at all

// DetectProviders checks every way to reach a model and returns them in a
// stable order: the gateway, the keys, Ollama, then a Claude Code login.
func DetectProviders(ctx context.Context, workDir string) []Detected {
	provider := FindProvider(workDir)
	var found []Detected
	if gateway := detectGateway(provider); gateway != nil {
		models, err := gatewayModels(ctx, provider)
		switch {
		case len(models) > 0:
			gateway.Models = models
			gateway.Detail += fmt.Sprintf(" · %d models", len(models))
		case err != nil:
			gateway.ModelsErr = err.Error()
			gateway.Detail += " · models not listed: " + err.Error()
		}
		found = append(found, *gateway)
	}
	found = append(found, detectKeys(provider)...)
	if ollama := detectOllama(ctx); ollama != nil {
		found = append(found, *ollama)
	}
	if login := detectClaudeLogin(); login != nil {
		found = append(found, *login)
	}
	return found
}

// detectClaudeLogin notices a Claude Code subscription login. tubas can't
// use it: Anthropic allows it only in Claude Code and claude.ai. The first
// screen says so, and points to an API key.
func detectClaudeLogin() *Detected {
	_, onPath := lookPath("claude")
	_, creds := os.Stat(filepath.Join(claudeHome(), ".credentials.json"))
	if onPath != nil && creds != nil {
		return nil
	}
	return &Detected{Name: "claude-login", Detail: "Claude Code is here: its login works only inside Claude Code · /connect an API key"}
}

// ── your Claude settings: a gateway, Bedrock, Vertex ────────────────────

func detectGateway(provider Provider) *Detected {
	label := provider.Label()
	if label == "" {
		return nil
	}
	var through string
	switch {
	case label == "bedrock":
		through = "through Amazon Bedrock"
	case label == "vertex":
		through = "through Google Vertex AI"
	case label == "foundry":
		through = "through Microsoft Foundry"
	case strings.HasPrefix(label, "gateway "):
		through = "through " + strings.TrimPrefix(label, "gateway ")
	default:
		through = "through a gateway"
	}
	from := "from your shell" // Label also reads this process's environment
	if len(provider.Sources) > 0 {
		paths := make([]string, len(provider.Sources))
		for i, p := range provider.Sources {
			paths[i] = shortPath(p)
		}
		from = "from " + strings.Join(paths, ", ")
	}
	if provider.KeyHelper != "" {
		from += " · token from your apiKeyHelper"
	}
	usable := label != "bedrock" && label != "vertex" && label != "foundry"
	return &Detected{Name: "gateway", Ready: usable, Usable: usable, Detail: through + " · " + from}
}

// shortPath writes a path under your home folder as ~/..., the way
// people recognize it.
func shortPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && filepath.IsLocal(rel) {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

// ── API keys in your shell ──────────────────────────────────────────────

// detectKeys checks which API keys are set. Only names are used: values
// never leave this function.
func detectKeys(provider Provider) []Detected {
	var found []Detected
	key := func(name string, vars []string) {
		for _, v := range vars {
			where := ""
			switch {
			case strings.TrimSpace(os.Getenv(v)) != "":
				where = "in your shell"
			case strings.TrimSpace(provider.Env[v]) != "":
				where = "in your Claude settings"
			default:
				continue
			}
			found = append(found, Detected{Name: name, Ready: true, Usable: true, Detail: v + " " + where + " · provider: " + name})
			return
		}
	}
	for _, p := range []string{"deepseek", "glm", "kimi", "qwen"} {
		key(p, []string{BuiltinProviders[p].KeyEnv})
	}
	key("openrouter", []string{"OPENROUTER_API_KEY"})
	key("openai", []string{"OPENAI_API_KEY"})
	key("anthropic-key", []string{"ANTHROPIC_API_KEY"})
	key("gemini", []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"})
	return found
}

// ── Ollama ──────────────────────────────────────────────────────────────

func detectOllama(ctx context.Context) *Detected {
	_, lookErr := lookPath("ollama")
	installed := lookErr == nil

	models, running := ollamaModels(ctx)
	switch {
	case running:
		plural := "s"
		if models == 1 {
			plural = ""
		}
		return &Detected{Name: "ollama", Ready: true, Usable: true, Detail: fmt.Sprintf("running · %d model%s · provider: ollama", models, plural)}
	case installed:
		return &Detected{Name: "ollama", Detail: "installed, not running · ollama serve starts it"}
	}
	return nil
}

// ollamaModels asks a local Ollama server for its models.
func ollamaModels(ctx context.Context) (count int, running bool) {
	ctx, cancel := context.WithTimeout(ctx, ollamaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(ollamaURL, "/")+"/api/tags", nil)
	if err != nil {
		return 0, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var tags struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return 0, true // something answers on Ollama's port; the list is a bonus
	}
	return len(tags.Models), true
}
