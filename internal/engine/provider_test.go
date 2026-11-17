package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tubas/internal/agent"
)

// TestMain lets the test binary stand in for claude: started with
// TUBAS_FAKE_CLAUDE=1, it answers one turn with what it sees in its
// environment, so a test can check what tubas passed on.
func TestMain(m *testing.M) {
	if os.Getenv("TUBAS_FAKE_CLAUDE") == "args" { // answers with how it was started
		wd, _ := os.Getwd()
		seen, _ := json.Marshal(map[string]any{"args": os.Args[1:], "dir": wd})
		line, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": string(seen)})
		fmt.Println(string(line))
		os.Exit(0)
	}
	if os.Getenv("TUBAS_FAKE_MCP") != "" {
		fakeMCP()
		os.Exit(0)
	}
	if os.Getenv("TUBAS_FAKE_ACP") != "" {
		fakeACP()
		os.Exit(0)
	}
	if mode := os.Getenv("TUBAS_FAKE_CODEX"); mode != "" {
		fakeCodex(mode)
		os.Exit(0)
	}
	if os.Getenv("TUBAS_FAKE_CLAUDE") == "provider" { // answers with the provider variables and the model asked
		model := ""
		for i, a := range os.Args {
			if a == "--model" && i+1 < len(os.Args) {
				model = os.Args[i+1]
			}
		}
		seen, _ := json.Marshal(map[string]string{"base": os.Getenv("ANTHROPIC_BASE_URL"), "token": os.Getenv("ANTHROPIC_AUTH_TOKEN"),
			"apikey": os.Getenv("ANTHROPIC_API_KEY"), "envmodel": os.Getenv("ANTHROPIC_DEFAULT_SONNET_MODEL"), "model": model})
		line, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": string(seen), "total_cost_usd": 0.5})
		fmt.Println(string(line))
		os.Exit(0)
	}
	if os.Getenv("TUBAS_FAKE_CLAUDE") == "1" {
		seen := fmt.Sprintf("base=%s token=%s other=%s",
			os.Getenv("ANTHROPIC_BASE_URL"), os.Getenv("ANTHROPIC_AUTH_TOKEN"), os.Getenv("SOME_OTHER_SETTING"))
		line, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "result": seen})
		fmt.Println(string(line))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func writeSettings(t *testing.T, path string, env map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"env": env, "hooks": map[string]any{"Stop": []any{}}})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A work setup: the gateway lives in the user's settings, the project
// overrides the token. Only provider variables come through.
func workSetup(t *testing.T) (project string) {
	t.Helper()
	config := t.TempDir()
	project = t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeSettings(t, filepath.Join(config, "settings.json"), map[string]any{
		"ANTHROPIC_BASE_URL":   "https://llm.acme.internal/claude",
		"ANTHROPIC_AUTH_TOKEN": "user-token",
		"NODE_EXTRA_CA_CERTS":  "/etc/acme-ca.pem",
		"SOME_OTHER_SETTING":   "stays out",
	})
	writeSettings(t, filepath.Join(project, ".claude", "settings.local.json"), map[string]any{
		"ANTHROPIC_AUTH_TOKEN": "project-token",
	})
	return project
}

func TestFindProviderReadsTheGateway(t *testing.T) {
	p := FindProvider(workSetup(t))

	// The whole env block comes through, as tucano does: a company setup
	// may need variables of its own.
	want := map[string]string{
		"ANTHROPIC_BASE_URL":   "https://llm.acme.internal/claude",
		"ANTHROPIC_AUTH_TOKEN": "project-token", // the local file wins
		"NODE_EXTRA_CA_CERTS":  "/etc/acme-ca.pem",
		"SOME_OTHER_SETTING":   "stays out",
	}
	if len(p.Env) != len(want) {
		t.Fatalf("env = %v, want %v", p.Env, want)
	}
	for k, v := range want {
		if p.Env[k] != v {
			t.Errorf("%s = %q, want %q", k, p.Env[k], v)
		}
	}
	if len(p.Sources) != 2 {
		t.Errorf("sources = %v, want both files", p.Sources)
	}
	if got := p.Label(); got != "gateway llm.acme.internal" {
		t.Errorf("label = %q", got)
	}
}

func TestFindProviderWithoutSettings(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_BASE_URL", "")
	p := FindProvider(t.TempDir())
	if len(p.Env) != 0 || p.Label() != "" || p.KeyHelper != "" {
		t.Errorf("no settings should mean no provider: %+v", p)
	}
}

func TestProviderLabels(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "")
	cases := map[string]Provider{
		"bedrock": {Env: map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"}},
		"vertex":  {Env: map[string]string{"CLAUDE_CODE_USE_VERTEX": "true"}},
		"":        {Env: map[string]string{"CLAUDE_CODE_USE_VERTEX": "0"}},
		" ":       {Env: map[string]string{"ANTHROPIC_BASE_URL": "https://api.anthropic.com"}},
	}
	for want, p := range cases {
		want = strings.TrimSpace(want) // " " is a second case that wants ""
		if got := p.Label(); got != want {
			t.Errorf("%v: label = %q, want %q", p.Env, got, want)
		}
	}
}

// A boolean in the JSON ("CLAUDE_CODE_USE_BEDROCK": true) still counts.
func TestSettingsBooleansBecomeStrings(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeSettings(t, filepath.Join(config, "settings.json"), map[string]any{"CLAUDE_CODE_USE_BEDROCK": true, "AWS_REGION": "us-east-1"})
	p := FindProvider("")
	if p.Env["CLAUDE_CODE_USE_BEDROCK"] != "true" || p.Label() != "bedrock" || p.Env["AWS_REGION"] != "us-east-1" {
		t.Errorf("got %v, label %q", p.Env, p.Label())
	}
}

func TestEnvironOverridesThisProcess(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://old.example.com")
	t.Setenv("TUBAS_KEEP_ME", "yes")
	env := Provider{Env: map[string]string{"ANTHROPIC_BASE_URL": "https://llm.acme.internal"}}.Environ()
	joined := "\n" + strings.Join(env, "\n") + "\n"
	if !strings.Contains(joined, "\nANTHROPIC_BASE_URL=https://llm.acme.internal\n") || strings.Contains(joined, "old.example.com") {
		t.Errorf("the settings' value should replace the shell's")
	}
	if !strings.Contains(joined, "\nTUBAS_KEEP_ME=yes\n") {
		t.Errorf("the rest of the environment should stay")
	}
}

// End to end: the claude process an agent turn starts gets the gateway
// from the settings, and nothing else from them.
func TestAgentTurnReachesTheGateway(t *testing.T) {
	project := workSetup(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUBAS_FAKE_CLAUDE", "1")

	eng := ClaudeEngine{Command: exe, WorkDir: project, Provider: FindProvider(project)}
	out := make(chan Event, 16)
	reply, err := eng.RunTurn(context.Background(), TurnRequest{Agent: agent.Agent{Name: "iris", Model: "haiku"}, Prompt: "hi"}, out)
	if err != nil {
		t.Fatal(err)
	}
	want := "base=https://llm.acme.internal/claude token=project-token other=stays out"
	if reply.Text != want {
		t.Errorf("claude saw %q, want %q", reply.Text, want)
	}
	if got := eng.Name(); got != "claude · gateway llm.acme.internal" {
		t.Errorf("name = %q", got)
	}
}
