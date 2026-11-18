package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// detectSetup is a computer with nothing on it: no settings, no keys, no
// programs, nothing on Ollama's port.
func detectSetup(t *testing.T) (installed map[string]string, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	for _, k := range []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
		"OPENROUTER_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"DEEPSEEK_API_KEY", "ZAI_API_KEY", "MOONSHOT_API_KEY", "DASHSCOPE_API_KEY",
	} {
		t.Setenv(k, "")
	}
	installed = map[string]string{}
	oldLook, oldURL, oldModels, oldHome := lookPath, ollamaURL, gatewayModels, claudeHome
	t.Cleanup(func() { lookPath, ollamaURL, gatewayModels, claudeHome = oldLook, oldURL, oldModels, oldHome })
	gatewayModels = func(context.Context, Provider) ([]string, error) { return nil, nil }
	claudeHome = func() string { return filepath.Join(home, ".claude") }
	lookPath = func(name string) (string, error) {
		if p, ok := installed[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	closed := httptest.NewServer(http.NotFoundHandler()) // a server that is gone
	ollamaURL = closed.URL
	closed.Close()
	return installed, home
}

func names(found []Detected) string {
	var n []string
	for _, d := range found {
		n = append(n, d.Name)
	}
	return strings.Join(n, ",")
}

func TestNothingFoundOnANewComputer(t *testing.T) {
	detectSetup(t)
	if found := DetectProviders(context.Background(), t.TempDir()); len(found) != 0 {
		t.Errorf("nothing: %+v", found)
	}
}

func TestDetectEverythingInOrder(t *testing.T) {
	installed, home := detectSetup(t)
	writeSettings(t, filepath.Join(home, ".claude", "settings.json"), map[string]any{"ANTHROPIC_BASE_URL": "https://llm.acme.internal"})
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600)
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	t.Setenv("DEEPSEEK_API_KEY", "ds-key")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"llama"}]}`)
	}))
	defer srv.Close()
	ollamaURL = srv.URL
	installed["ollama"] = "/bin/ollama"

	found := DetectProviders(context.Background(), t.TempDir())
	if got := names(found); got != "gateway,deepseek,openrouter,ollama,claude-login" {
		t.Errorf("order: %s", got)
	}
	for _, d := range found {
		if strings.Contains(d.Detail, "or-key") || strings.Contains(d.Detail, "ds-key") {
			t.Errorf("a key on screen: %s", d.Detail)
		}
		if d.Name == "claude-login" && (d.Usable || !strings.Contains(d.Detail, "/connect")) {
			t.Errorf("a Claude Code login is noticed, not used: %+v", d)
		}
	}
}

func TestDetectBedrockIsNotUsable(t *testing.T) {
	detectSetup(t)
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	found := DetectProviders(context.Background(), t.TempDir())
	if len(found) != 1 || found[0].Name != "gateway" || found[0].Usable || !strings.Contains(found[0].Detail, "Bedrock") {
		t.Errorf("Bedrock is named but tubas can't use it: %+v", found)
	}
}

func TestDetectOllamaNotRunning(t *testing.T) {
	installed, _ := detectSetup(t)
	installed["ollama"] = "/bin/ollama"
	found := DetectProviders(context.Background(), t.TempDir())
	if len(found) != 1 || found[0].Ready || !strings.Contains(found[0].Detail, "ollama serve") {
		t.Errorf("installed, not running: %+v", found)
	}
}
