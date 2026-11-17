package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeWorld stands in for the commands, PATH and Ollama during a test.
type fakeWorld struct {
	mu         sync.Mutex
	authStatus string            // what `claude auth status` prints
	installed  map[string]string // name → path for lookPath
	claudeEnv  []string          // the environment claude was run with
}

func detectSetup(t *testing.T) *fakeWorld {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("TUBAS_CLAUDE", "fake-claude")
	for _, k := range []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
		"OPENROUTER_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"DEEPSEEK_API_KEY", "ZAI_API_KEY", "MOONSHOT_API_KEY", "DASHSCOPE_API_KEY",
	} {
		t.Setenv(k, "")
	}

	w := &fakeWorld{installed: map[string]string{}}
	oldRun, oldLook, oldURL, oldModels := runCommand, lookPath, ollamaURL, gatewayModels
	t.Cleanup(func() { runCommand, lookPath, ollamaURL, gatewayModels = oldRun, oldLook, oldURL, oldModels })
	gatewayModels = func(context.Context, Provider) []string { return nil }

	runCommand = func(ctx context.Context, env []string, name string, args ...string) (string, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("%s %v ran without a timeout", name, args)
		}
		switch name + " " + strings.Join(args, " ") {
		case "fake-claude --version":
			w.claudeEnv = env
			return "2.1.140 (Claude Code)\n", nil
		case "fake-claude auth status":
			return w.authStatus, nil
		case "/bin/codex --version":
			return "codex-cli 0.50.0\n", nil
		case "/bin/codex login status":
			return "Logged in using ChatGPT\n", nil
		}
		return "", errors.New("unexpected command")
	}
	lookPath = func(name string) (string, error) {
		if p, ok := w.installed[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	// A server that is gone: nothing answers there.
	closed := httptest.NewServer(http.NotFoundHandler())
	ollamaURL = closed.URL
	closed.Close()
	return w
}

func names(found []Detected) string {
	var n []string
	for _, d := range found {
		n = append(n, d.Name)
	}
	return strings.Join(n, ",")
}

func TestDetectSignedInWithASubscription(t *testing.T) {
	w := detectSetup(t)
	w.authStatus = `{"loggedIn": true, "authMethod": "claude.ai", "apiProvider": "firstParty", "subscriptionType": "max"}`

	found := DetectProviders(context.Background(), t.TempDir())
	if names(found) != "claude" {
		t.Fatalf("found %s", names(found))
	}
	want := Detected{Name: "claude", Ready: true, Usable: true, Detail: "Claude Code 2.1.140 · signed in with a Claude Max plan"}
	if !reflect.DeepEqual(found[0], want) {
		t.Fatalf("got %+v", found[0])
	}
	for _, kv := range w.claudeEnv {
		if strings.HasPrefix(kv, "ANTHROPIC_BASE_URL=") && kv != "ANTHROPIC_BASE_URL=" {
			t.Errorf("without a provider claude gets no gateway: %s", kv)
		}
	}
}

func TestDetectSignedInWithAnAPIKey(t *testing.T) {
	w := detectSetup(t)
	w.authStatus = "warning: something\n{\"loggedIn\": true, \"authMethod\": \"api_key\"}\n"

	found := DetectProviders(context.Background(), t.TempDir())
	if len(found) != 1 || !found[0].Ready || found[0].Detail != "Claude Code 2.1.140 · signed in with an API key" {
		t.Fatalf("got %+v", found)
	}
}

func TestDetectNotSignedIn(t *testing.T) {
	w := detectSetup(t)
	w.authStatus = `{"loggedIn": false}`

	found := DetectProviders(context.Background(), t.TempDir())
	if len(found) != 1 || found[0].Ready || !strings.Contains(found[0].Detail, "not signed in") {
		t.Fatalf("got %+v", found)
	}
}

func TestDetectOldClaudeWithoutAuthStatus(t *testing.T) {
	w := detectSetup(t)
	w.authStatus = "error: unknown command 'auth'"

	found := DetectProviders(context.Background(), t.TempDir())
	if len(found) != 1 || !found[0].Ready || !strings.Contains(found[0].Detail, "not checked") {
		t.Fatalf("got %+v", found)
	}
}

func TestDetectClaudeNotFound(t *testing.T) {
	detectSetup(t)
	t.Setenv("TUBAS_CLAUDE", "")
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	found := DetectProviders(context.Background(), t.TempDir())
	if len(found) != 1 || found[0].Name != "claude" || found[0].Ready || !strings.Contains(found[0].Detail, "not found") {
		t.Fatalf("got %+v", found)
	}
}

func TestDetectGatewayFromSettings(t *testing.T) {
	w := detectSetup(t)
	w.authStatus = `{"loggedIn": false}` // the gateway brings its own token
	writeSettings(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json"), map[string]any{
		"ANTHROPIC_BASE_URL":   "https://llm.acme.internal/claude",
		"ANTHROPIC_AUTH_TOKEN": "user-token",
	})

	found := DetectProviders(context.Background(), t.TempDir())
	if names(found) != "claude,gateway" {
		t.Fatalf("found %s", names(found))
	}
	if !found[0].Ready || !strings.Contains(found[0].Detail, "through your gateway") {
		t.Errorf("claude %+v", found[0])
	}
	want := Detected{Name: "gateway", Ready: true, Usable: true, Detail: "through llm.acme.internal · from ~/.claude/settings.json"}
	if !reflect.DeepEqual(found[1], want) {
		t.Errorf("gateway %+v", found[1])
	}
	// claude was asked the way agents will run it: with the gateway.
	if !slices.Contains(w.claudeEnv, "ANTHROPIC_BASE_URL=https://llm.acme.internal/claude") {
		t.Errorf("claude did not get the gateway in its environment")
	}
	for _, d := range found {
		if strings.Contains(d.Detail, "user-token") {
			t.Errorf("a token leaked into %+v", d)
		}
	}
}

func TestDetectBedrockFromTheShell(t *testing.T) {
	w := detectSetup(t)
	w.authStatus = `{"loggedIn": true, "authMethod": "third_party", "apiProvider": "bedrock"}`
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")

	found := DetectProviders(context.Background(), t.TempDir())
	if names(found) != "claude,gateway" || found[1].Detail != "through Amazon Bedrock · from your shell" {
		t.Fatalf("got %+v", found)
	}
	if found[0].Detail != "Claude Code 2.1.140 · using bedrock" {
		t.Errorf("claude %+v", found[0])
	}
}

func TestDetectEverythingInOrder(t *testing.T) {
	w := detectSetup(t)
	w.authStatus = `{"loggedIn": true, "subscriptionType": "pro"}`
	w.installed["codex"] = "/bin/codex"
	w.installed["ollama"] = "/bin/ollama"
	writeSettings(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json"), map[string]any{
		"ANTHROPIC_BASE_URL": "https://llm.acme.internal",
	})
	t.Setenv("OPENROUTER_API_KEY", "sk-or-secret")
	t.Setenv("OPENAI_API_KEY", "sk-openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret")
	t.Setenv("GOOGLE_API_KEY", "google-secret")

	ollama := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(rw, r)
			return
		}
		rw.Write([]byte(`{"models":[{"name":"llama3"},{"name":"qwen"}]}`))
	}))
	defer ollama.Close()
	ollamaURL = ollama.URL

	found := DetectProviders(context.Background(), t.TempDir())
	if got := names(found); got != "claude,gateway,codex,openrouter,openai,anthropic-key,gemini,ollama" {
		t.Fatalf("order %s", got)
	}
	byName := map[string]Detected{}
	for _, d := range found {
		byName[d.Name] = d
		if strings.Contains(d.Detail, "secret") {
			t.Errorf("a key leaked into %+v", d)
		}
		if !d.Ready {
			t.Errorf("%s is not ready", d.Name)
		}
		if !d.Usable {
			t.Errorf("%s should be usable now: every harness and provider runs agents", d.Name)
		}
	}
	for name, want := range map[string]string{
		"claude":        "Claude Code 2.1.140 · signed in with a Claude Pro plan",
		"codex":         "Codex CLI 0.50.0 · signed in with ChatGPT · harness: codex",
		"openrouter":    "OPENROUTER_API_KEY in your shell · provider: openrouter on harness: api",
		"anthropic-key": "ANTHROPIC_API_KEY in your shell · Claude Code uses it, and harness: api",
		"gemini":        "GOOGLE_API_KEY in your shell · provider: gemini on harness: api",
		"ollama":        "running · 2 models · provider: ollama on harness: api",
	} {
		if byName[name].Detail != want {
			t.Errorf("%s: %q, want %q", name, byName[name].Detail, want)
		}
	}
}

func TestDetectOllamaNotRunning(t *testing.T) {
	w := detectSetup(t)
	w.authStatus = `{"loggedIn": true}`
	w.installed["ollama"] = "/bin/ollama"

	found := DetectProviders(context.Background(), t.TempDir())
	if names(found) != "claude,ollama" {
		t.Fatalf("found %s", names(found))
	}
	want := Detected{Name: "ollama", Detail: "installed, not running · ollama serve starts it"}
	if !reflect.DeepEqual(found[1], want) {
		t.Fatalf("got %+v", found[1])
	}
}
