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
