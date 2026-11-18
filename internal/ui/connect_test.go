package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tubas/internal/agent"
	"tubas/internal/auth"
	"tubas/internal/engine"
)

// typeIn sends text as it would arrive from the keyboard, then any keys.
func typeIn(m Model, text string, then ...tea.KeyType) Model {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
	m = settle(next.(Model), cmd)
	for _, k := range then {
		next, cmd = m.Update(tea.KeyMsg{Type: k})
		m = settle(next.(Model), cmd)
	}
	return m
}

// connectWorld is a computer with nothing connected and one provider,
// acme, whose good key is sk-good.
func connectWorld(t *testing.T) (Model, string) {
	t.Helper()
	t.Setenv("TUBAS_DATA_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // not your real Claude settings
	for _, v := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ACME_KEY", "OPENAI_API_KEY",
		"OPENROUTER_API_KEY", "GEMINI_API_KEY", "DEEPSEEK_API_KEY"} {
		t.Setenv(v, "")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-good-key-1234" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"acme-large"},{"id":"acme-small"}]}`)
	}))
	t.Cleanup(srv.Close)
	m, _ := personalModel(t)
	m.cfg.ProviderSpecs = engine.Providers(map[string]engine.ProviderSpec{
		"acme": {OpenAI: srv.URL, KeyEnv: "ACME_KEY", Models: []string{"acme-large", "acme-small"}},
	})
	return m, srv.URL
}

func TestTheFirstScreenConnectsAProvider(t *testing.T) {
	m, _ := connectWorld(t)
	if !m.stranded() {
		t.Fatal("nothing is connected: the first screen should open")
	}
	m = feed(m, providersMsg(nil)) // detection finished
	onScreen(t, m, "Welcome to tubas", "acme", "not connected", "your own server")

	m = click(t, m, "acme")
	onScreen(t, m, "Paste acme's key")
	m = typeIn(m, "sk-wrong-key", tea.KeyEnter)
	onScreen(t, m, "acme refused this key")
	m = typeIn(m, "sk-good-key-1234")
	if screen := ansi.Strip(m.View()); strings.Contains(screen, "sk-good") || !strings.Contains(screen, "1234") {
		t.Fatalf("the key is hidden but its end:\n%s", screen)
	}
	m = typeIn(m, "", tea.KeyEnter)
	if m.connect != nil {
		t.Fatalf("connected: the chat is ready:\n%s", ansi.Strip(m.View()))
	}
	onScreen(t, m, "acme connected · 2 models")
	if auth.Get("acme") != "sk-good-key-1234" {
		t.Error("the key is saved")
	}
	a := m.agentByName(m.pickerAgent())
	if a.Provider != "acme" || a.Model != "acme-large" {
		t.Errorf("the assistant now uses it: %+v", a)
	}
	data, _ := os.ReadFile(filepath.Join(m.cfg.TeamDir, "agents", a.Name+".md"))
	if strings.Contains(string(data), "sk-good") {
		t.Error("no key in the team folder")
	}
	if m.stranded() {
		t.Error("not stranded any more")
	}
}

func TestConnectYourOwnServerAndForgetIt(t *testing.T) {
	m, url := connectWorld(t)
	m = m.editAgentForTest(t, func(a *agent.Agent) { a.Provider = "ollama" }) // already reachable: not moved
	m = typeIn(m, "/connect", tea.KeyEnter)
	onScreen(t, m, "connect", "your own server")
	m = click(t, m, "your own server")
	onScreen(t, m, "Your server's address")
	m = typeIn(m, "not a url", tea.KeyEnter)
	onScreen(t, m, "give the full address")
	for range "not a url" {
		m = typeIn(m, "", tea.KeyBackspace)
	}
	m = typeIn(m, url, tea.KeyEnter)
	m = typeIn(m, "acme-large", tea.KeyEnter)
	m = typeIn(m, "sk-good-key-1234", tea.KeyEnter)
	onScreen(t, m, "127-0-0-1 connected · 2 models", "127-0-0-1", "key saved")
	if c := auth.Customs()["127-0-0-1"]; c.OpenAI != url {
		t.Errorf("your server is saved: %+v", auth.Customs())
	}
	if a := m.agentByName(m.pickerAgent()); a.Provider != "ollama" {
		t.Errorf("a working assistant isn't moved: %+v", a)
	}

	m = typeIn(m, "d")
	onScreen(t, m, "d again forgets 127-0-0-1")
	m = typeIn(m, "d")
	onScreen(t, m, "127-0-0-1's key forgotten")
	if len(auth.Customs()) != 0 || auth.Get("127-0-0-1") != "" {
		t.Error("forgotten, server and key")
	}
	m = typeIn(m, "", tea.KeyEsc)
	if m.connect != nil {
		t.Error("esc closes it")
	}
}

// editAgentForTest changes the assistant's file and reloads the team.
func (m Model) editAgentForTest(t *testing.T, change func(*agent.Agent)) Model {
	t.Helper()
	if err := m.editAgent(m.pickerAgent(), change); err != nil {
		t.Fatal(err)
	}
	return m.reloadTeam()
}
