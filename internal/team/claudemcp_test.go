package team

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeCodeMCPServersBecomeConnectors(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	project := t.TempDir()
	state := `{
	  "mcpServers": {
	    "playwright": {"type": "stdio", "command": "npx", "args": ["@playwright/mcp"], "env": {"SECRET": "s3cr3t"}},
	    "ddl": {"type": "http", "url": "https://ddl.example.com/mcp", "headers": {"Authorization": "Bearer t0k3n"}},
	    "old": {"type": "sse", "url": "https://old.example.com/sse"}
	  },
	  "projects": {` + "\"" + filepath.ToSlash(project) + "\"" + `: {"mcpServers": {"blender": {"command": "uvx", "args": ["blender-mcp"]}}}}
	}`
	os.WriteFile(filepath.Join(cfg, ".claude.json"), []byte(state), 0o600)
	os.WriteFile(filepath.Join(project, ".mcp.json"), []byte(`{"mcpServers": {"shared": {"command": "node", "args": ["s.js"]}}}`), 0o600)

	got := map[string]ClaudeServer{}
	for _, s := range ClaudeServers(project) {
		got[s.Name] = s
	}
	if len(got) != 5 || got["ddl"].Type != "http" || got["blender"].From != "project "+filepath.Base(project) || got["shared"].From != ".mcp.json" {
		t.Fatalf("servers = %+v", got)
	}

	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if err := AddConnector(dir, Connector{Name: "playwright", Claude: "playwright"}); err != nil {
		t.Fatal(err)
	}
	if err := AddConnector(dir, Connector{Name: "ddl", Claude: "ddl"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "team.yaml"))
	text := string(data)
	if !strings.Contains(text, "claude: playwright") || strings.Contains(text, "s3cr3t") || strings.Contains(text, "t0k3n") {
		t.Errorf("team.yaml names the server, never its tokens:\n%s", text)
	}
	if !strings.Contains(text, "# Give the assistant tools") {
		t.Error("the file keeps its comments")
	}
	tm, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := tm.Connectors["ddl"]
	if c.Kind() != "mcp" || c.Runs() != "from Claude Code: ddl" {
		t.Errorf("ddl = %+v", c)
	}
	r, err := c.Resolved()
	if err != nil || r.URL != "https://ddl.example.com/mcp" || r.Headers["Authorization"] != "Bearer t0k3n" {
		t.Errorf("resolved from Claude Code when it starts: %+v %v", r, err)
	}
	if _, err := (Connector{Name: "x", Claude: "old"}).Resolved(); err == nil {
		t.Error("an SSE server says it isn't supported")
	}
	if err := AddConnector(dir, Connector{Name: "ddl", Claude: "ddl"}); err == nil {
		t.Error("a name already there is refused")
	}
	if err := RemoveConnector(dir, "ddl"); err != nil {
		t.Fatal(err)
	}
	if tm, _ := Load(dir); tm.Connectors["ddl"].Name != "" {
		t.Error("removed")
	}
}
