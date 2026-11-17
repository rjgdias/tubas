package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tubas/internal/agent"
)

// A company setup: the gateway in the Claude settings, a key helper that
// prints a short-lived token. tubas finds both on its own.
func TestFindProviderReadsTheKeyHelper(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	data, _ := json.Marshal(map[string]any{
		"env":          map[string]any{"ANTHROPIC_BASE_URL": "https://acme.example.com/anthropic"},
		"apiKeyHelper": "echo helper-token-1",
	})
	os.WriteFile(filepath.Join(config, "settings.json"), data, 0o644)
	p := FindProvider(t.TempDir())
	if p.KeyHelper != "echo helper-token-1" || p.Label() != "gateway acme.example.com" {
		t.Fatalf("provider = %+v", p)
	}
	r, err := p.Resolved()
	if err != nil {
		t.Fatal(err)
	}
	if r.Env["ANTHROPIC_AUTH_TOKEN"] != "helper-token-1" || r.Env["ANTHROPIC_API_KEY"] != "helper-token-1" {
		t.Errorf("the helper's token goes where claude sends it: %v", r.Env)
	}
}

// Without Claude Code, an agent reaches the same gateway on the api
// harness: its URL, the helper's token, the company's headers and model
// names, all from the Claude settings.
func TestAPIHarnessGoesThroughYourClaudeSetup(t *testing.T) {
	var got struct{ path, auth, team, model string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		got.path, got.auth, got.team, got.model = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Team"), fmt.Sprint(body["model"])
		fmt.Fprint(w, `{"content":[{"type":"text","text":"ok via acme"}],"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	defer srv.Close()
	setup := Provider{Env: map[string]string{
		"ANTHROPIC_BASE_URL":             srv.URL + "/anthropic",
		"ANTHROPIC_CUSTOM_HEADERS":       "X-Team: dados\nX-Other: 1",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "acme-sonnet",
	}, KeyHelper: "echo helper-token-2"}
	eng := APIEngine{Setup: setup, Providers: Providers(nil), WorkDir: t.TempDir()}
	if eng.Name() == "api" {
		t.Error("the header names the gateway")
	}
	reply, err := eng.RunTurn(context.Background(), TurnRequest{Agent: agent.Agent{Name: "iris", Model: "sonnet", Instructions: "x"}, Prompt: "hi"},
		make(chan Event, 16))
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "ok via acme" || got.path != "/anthropic/v1/messages" || got.auth != "Bearer helper-token-2" ||
		got.team != "dados" || got.model != "acme-sonnet" {
		t.Errorf("reply %q, request %+v", reply.Text, got)
	}
}

// Agents get your setup, not the Claude Code session tubas may run inside;
// behind a gateway, claude's telemetry is off; your key helper goes to
// claude, which runs it (and runs it again on a 401).
func TestClaudeGetsACleanQuietEnvironment(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "host-session")
	t.Setenv("CLAUDE_CODE_GIT_BASH_PATH", "C:/git/bash.exe")
	p := Provider{Env: map[string]string{"ANTHROPIC_BASE_URL": "https://acme.example.com"}, KeyHelper: "get-token --team dados"}
	env := strings.Join(p.Environ(), "\n")
	for _, gone := range []string{"CLAUDECODE=", "CLAUDE_CODE_SESSION_ID="} {
		if strings.Contains(env, gone) {
			t.Errorf("%s belongs to the host session, not the agent", gone)
		}
	}
	for _, kept := range []string{"CLAUDE_CODE_GIT_BASH_PATH=C:/git/bash.exe", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "ANTHROPIC_BASE_URL=https://acme.example.com"} {
		if !strings.Contains(env, kept) {
			t.Errorf("missing %s", kept)
		}
	}
	if got := strings.Join(p.SettingsArgs(), " "); got != `--settings {"apiKeyHelper":"get-token --team dados"}` {
		t.Errorf("settings args = %s", got)
	}
}

// The gateway says which models it serves; the agent form offers them.
func TestGatewayModels(t *testing.T) {
	var auth, version string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, version = r.Header.Get("Authorization"), r.Header.Get("anthropic-version")
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"acme-sonnet"},{"id":"acme-haiku"},{"id":"acme-sonnet"}]}`)
	}))
	defer srv.Close()
	p := Provider{Env: map[string]string{"ANTHROPIC_BASE_URL": srv.URL + "/", "ANTHROPIC_AUTH_TOKEN": "tok"}}
	models, _ := GatewayModels(context.Background(), p)
	got := strings.Join(models, ",")
	if got != "acme-haiku,acme-sonnet" || auth != "Bearer tok" || version == "" {
		t.Errorf("models %q, auth %q, version %q", got, auth, version)
	}
	if none, _ := GatewayModels(context.Background(), Provider{}); none != nil {
		t.Error("no gateway, no models")
	}
}

func TestFunctionNames(t *testing.T) {
	if got := functionName("github__search.issues"); got != "github__search_issues" {
		t.Errorf("got %q", got)
	}
	long := functionName(strings.Repeat("x", 80))
	if len(long) != 64 || long == functionName(strings.Repeat("x", 81)) {
		t.Errorf("a long name is cut with a hash that keeps it unique: %q", long)
	}
}
