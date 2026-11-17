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
	"sync"
	"time"
)

// Detection looks around this computer for ways to reach a model, for
// the settings screen and the first run: is Claude Code installed and
// signed in, does your Claude setup point to a work gateway, are there
// API keys in your shell, is Codex or Ollama installed. It only looks:
// it never prints, stores or sends a key.

// Detected is one way to reach a model that tubas found (or, for Claude
// Code, did not find).
type Detected struct {
	Name      string   // "claude", "gateway", "codex", "openrouter", "openai", "anthropic-key", "gemini", "ollama"
	Ready     bool     // can be used now
	Detail    string   // one line for people, e.g. "Claude Code 2.1.140 · signed in with a Claude Max plan"
	Usable    bool     // tubas can run agents with it
	Models    []string // the models it serves, when it says (a gateway's /v1/models)
	ModelsErr string   // why they could not be listed
}

// The outside world, as variables so tests can stand in for it.
var (
	// runCommand runs a program and returns what it printed (stdout and
	// stderr together). env nil means: this process's environment.
	runCommand = func(ctx context.Context, env []string, name string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	lookPath      = exec.LookPath
	ollamaURL     = "http://127.0.0.1:11434"
	gatewayModels = GatewayModels
)

const (
	commandTimeout = 15 * time.Second // a cold `claude` start can take a few seconds
	ollamaTimeout  = time.Second      // a local server answers fast or not at all
)

// DetectProviders checks every way to reach a model at once and returns
// them in a stable order: claude, the gateway, then the others. Things
// that are not there are left out, except Claude Code, so the screen can
// say how to install it.
func DetectProviders(ctx context.Context, workDir string) []Detected {
	provider := FindProvider(workDir)

	// One slot per probe keeps the order stable whatever finishes first.
	var claude, codex, ollama *Detected
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); claude = detectClaude(ctx, provider) }()
	go func() { defer wg.Done(); codex = detectCodex(ctx) }()
	go func() { defer wg.Done(); ollama = detectOllama(ctx) }()

	// The quick ones run here while the commands start.
	gateway := detectGateway(provider)
	keys := detectKeys(provider)
	if gateway != nil {
		models, err := gatewayModels(ctx, provider)
		switch {
		case len(models) > 0:
			gateway.Models = models
			gateway.Detail += fmt.Sprintf(" · %d models", len(models))
		case err != nil:
			gateway.ModelsErr = err.Error()
			gateway.Detail += " · models not listed: " + err.Error()
		}
	}
	wg.Wait()

	var found []Detected
	for _, d := range []*Detected{claude, gateway, codex} {
		if d != nil {
			found = append(found, *d)
		}
	}
	found = append(found, detectACP()...)
	found = append(found, keys...)
	if ollama != nil {
		found = append(found, *ollama)
	}
	return found
}

// ── Claude Code ─────────────────────────────────────────────────────────

// authStatus holds the fields tubas reads from `claude auth status`.
type authStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	APIProvider      string `json:"apiProvider"`
	SubscriptionType string `json:"subscriptionType"`
}

func detectClaude(ctx context.Context, provider Provider) *Detected {
	d := &Detected{Name: "claude", Usable: true}
	path, err := FindClaude()
	if err != nil {
		d.Detail = "Claude Code not found · install it from claude.com/claude-code"
		if provider.Label() != "" || provider.KeyHelper != "" {
			d.Detail = "Claude Code not found · claude agents run on tubas's own loop, through your Claude settings"
		}
		return d
	}
	// The same environment agents get, so a gateway in your settings is
	// seen here the way agents will see it.
	env := provider.Environ()

	out, err := probe(ctx, env, path, "--version")
	if err != nil {
		d.Detail = "Claude Code is installed but did not start: " + failReason(out, err)
		return d
	}
	name := "Claude Code"
	if v := versionIn(out); v != "" {
		name += " " + v
	}

	out, _ = probe(ctx, env, path, "auth", "status")
	status, parsed := parseAuthStatus(out)
	behindGateway := provider.Label() != ""
	switch {
	case parsed && status.LoggedIn:
		d.Ready = true
		d.Detail = name + " · " + signedInAs(status)
	case behindGateway:
		// A gateway brings its own token: no Claude login needed.
		d.Ready = true
		d.Detail = name + " · through your gateway"
	case parsed:
		d.Detail = name + " · not signed in · run claude and type /login"
	default:
		// Older versions have no `auth status`: the first turn will tell.
		d.Ready = true
		d.Detail = name + " · sign-in not checked (update Claude Code to see it)"
	}
	return d
}

// parseAuthStatus reads the JSON that `claude auth status` prints. Older
// versions, or a failed run, print something else: then ok is false.
func parseAuthStatus(out string) (s authStatus, ok bool) {
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return s, false
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &s); err != nil {
		return authStatus{}, false
	}
	return s, true
}

// signedInAs says how Claude Code is signed in, in words.
func signedInAs(s authStatus) string {
	if plan := strings.TrimSpace(s.SubscriptionType); plan != "" {
		return "signed in with a Claude " + titleCase(plan) + " plan"
	}
	method := strings.ToLower(s.AuthMethod)
	switch {
	case strings.Contains(method, "key"):
		return "signed in with an API key"
	case strings.Contains(method, "console"):
		return "signed in with an Anthropic Console account"
	case strings.Contains(method, "claude.ai"):
		return "signed in with a Claude account"
	}
	switch p := strings.ToLower(s.APIProvider); p {
	case "", "firstparty":
	default:
		return "using " + p
	}
	if s.AuthMethod != "" {
		return "signed in (" + s.AuthMethod + ")"
	}
	return "signed in"
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
	return &Detected{Name: "gateway", Ready: true, Usable: true, Detail: through + " · " + from}
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
	key := func(name string, vars []string, usable bool, why string) {
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
			found = append(found, Detected{Name: name, Ready: true, Usable: usable, Detail: v + " " + where + " · " + why})
			return
		}
	}
	for _, p := range []string{"deepseek", "glm", "kimi", "qwen"} {
		key(p, []string{BuiltinProviders[p].KeyEnv}, true, "provider: "+p+" on claude or api")
	}
	key("openrouter", []string{"OPENROUTER_API_KEY"}, true, "provider: openrouter on harness: api")
	key("openai", []string{"OPENAI_API_KEY"}, true, "provider: openai on harness: api")
	key("anthropic-key", []string{"ANTHROPIC_API_KEY"}, true, "Claude Code uses it, and harness: api")
	key("gemini", []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, true, "provider: gemini on harness: api")
	return found
}

// detectACP lists the ACP agents installed here.
func detectACP() []Detected {
	var found []Detected
	for _, a := range KnownACP {
		if _, err := lookPath(a.Command[0]); err == nil {
			found = append(found, Detected{Name: a.Name, Ready: true, Usable: true,
				Detail: "speaks ACP (" + strings.Join(a.Command, " ") + ") · harness: " + a.Name})
		}
	}
	return found
}

// ── Codex and Ollama ────────────────────────────────────────────────────

func detectCodex(ctx context.Context) *Detected {
	path, err := lookPath("codex")
	if err != nil {
		return nil
	}
	name := "Codex CLI"
	if out, err := probe(ctx, nil, path, "--version"); err == nil {
		if v := versionIn(out); v != "" {
			name += " " + v
		}
	}
	out, err := probe(ctx, nil, path, "login", "status")
	text := strings.ToLower(out)
	switch {
	case err == nil && strings.Contains(text, "logged in") && !strings.Contains(text, "not logged in"):
		how := "signed in with an API key"
		if strings.Contains(text, "chatgpt") {
			how = "signed in with ChatGPT"
		}
		return &Detected{Name: "codex", Ready: true, Usable: true, Detail: name + " · " + how + " · harness: codex"}
	}
	return &Detected{Name: "codex", Usable: true, Detail: name + " · not signed in · run codex login"}
}

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
		return &Detected{Name: "ollama", Ready: true, Usable: true, Detail: fmt.Sprintf("running · %d model%s · provider: ollama on harness: api", models, plural)}
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

// ── helpers ─────────────────────────────────────────────────────────────

func probe(ctx context.Context, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return runCommand(ctx, env, name, args...)
}

// versionIn finds the version number in a --version line:
// "2.1.140 (Claude Code)" → "2.1.140", "codex-cli 0.50.0" → "0.50.0".
func versionIn(out string) string {
	for _, field := range strings.Fields(out) {
		field = strings.TrimPrefix(field, "v")
		if field != "" && field[0] >= '0' && field[0] <= '9' && strings.Contains(field, ".") {
			return field
		}
	}
	return ""
}

// failReason is a short reason for a failed command.
func failReason(out string, err error) string {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if line = strings.TrimSpace(line); line == "" {
		return err.Error()
	}
	if len(line) > 120 {
		line = line[:120] + "…"
	}
	return line
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
