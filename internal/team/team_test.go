package team

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleTeamLoads(t *testing.T) {
	tm, err := Load(filepath.Join("..", "..", "examples", "dados"))
	if err != nil {
		t.Fatal(err)
	}
	if tm.Name != "dados" || len(tm.Agents) != 3 || tm.MaxHops != 10 {
		t.Errorf("team = %s, %d agents, %d hops", tm.Name, len(tm.Agents), tm.MaxHops)
	}
	if got := strings.Join(tm.ConnectorNames(), ","); got != "data,reports,skills,team,workspace" {
		t.Errorf("connectors = %s", got)
	}
}

// makeTeam writes a team folder with one agent that uses the given tools.
func makeTeam(t *testing.T, teamYAML, tools string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "team.yaml"), teamYAML)
	write(t, filepath.Join(dir, "agents", "ana.md"), "---\nname: ana\ntools:\n"+tools+"---\nHelp.\n")
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name, yaml, tools, contains string
	}{
		{"unknown connector", "name: x\n", "  data: read\n", `no connector with that name`},
		{"unknown builtin", "connectors:\n  data:\n    builtin: excel\n", "  data: read\n", "unknown builtin"},
		{"neither", "connectors:\n  data:\n    db: x.db\n", "  data: read\n", "needs builtin: or command:"},
		{"both", "connectors:\n  data:\n    builtin: sqlite\n    command: x\n", "  data: read\n", "not both"},
		{"sqlite without db", "connectors:\n  data:\n    builtin: sqlite\n", "  data: read\n", "needs db"},
		{"bad name", "connectors:\n  Data Base:\n    builtin: reports\n", "", "lowercase"},
		{"bad hops", "max_hops: 99\n", "", "max_hops"},
		{"unknown lead", "lead: zeca\n", "", "lead"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(makeTeam(t, c.yaml, c.tools))
			if err == nil || !strings.Contains(err.Error(), c.contains) {
				t.Errorf("expected an error containing %q, got %v", c.contains, err)
			}
		})
	}
}

func TestInitCreatesALoadableTeam(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "suporte")
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	tm, err := Load(dir)
	if err != nil {
		t.Fatalf("the new team should load: %v", err)
	}
	// A plain chat to start from: one assistant, teammates when you add them.
	if tm.Name != "suporte" || len(tm.Agents) != 1 || tm.Agents[0].Name != "tubas" || tm.Lead != "tubas" {
		t.Errorf("team = %s with %d agents, lead %s", tm.Name, len(tm.Agents), tm.Lead)
	}
	if err := Init(dir); err == nil {
		t.Error("init over an existing team should fail")
	}
}

func TestMissingTeamYAML(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "no team.yaml") {
		t.Errorf("expected a helpful error, got %v", err)
	}
}

func TestDefaultsAndPrepare(t *testing.T) {
	dir := makeTeam(t, "connectors:\n  data:\n    builtin: sqlite\n    db: data/x.db\n    seed: delivery\n", "  data: read\n")
	tm, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tm.Name != filepath.Base(dir) || tm.MaxHops != 6 {
		t.Errorf("defaults not applied: name %q, hops %d", tm.Name, tm.MaxHops)
	}
	if err := tm.Prepare(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "x.db")); err != nil {
		t.Errorf("Prepare should create the seeded database: %v", err)
	}
}
