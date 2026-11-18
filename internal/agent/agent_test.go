package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// In Go, a test is a TestXxx(t *testing.T) function in a _test.go file.
// Run with: go test ./...

func TestParseValidFile(t *testing.T) {
	content := "---\nname: iris\nrole: investigator\ntools:\n  data: read\n---\nInvestigate with SQL.\n"

	a, err := Parse(content)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if a.Name != "iris" || a.Role != "investigator" {
		t.Errorf("wrong fields: %+v", a)
	}
	if a.Tools["data"] != "read" {
		t.Errorf("tool data should be read, got %q", a.Tools["data"])
	}
	if a.Instructions != "Investigate with SQL." {
		t.Errorf("wrong instructions: %q", a.Instructions)
	}
	// Defaults applied to what the file left out:
	if a.Harness != "" || a.Model != "sonnet" || a.TokenLimit != 50000 {
		t.Errorf("defaults not applied: %+v", a)
	}
}

func TestParseWindowsLineEndings(t *testing.T) {
	content := "---\r\nname: leo\r\n---\r\nCheck the pipeline.\r\n"
	a, err := Parse(content)
	if err != nil {
		t.Fatalf("a file with \\r\\n should work, got error: %v", err)
	}
	if a.Name != "leo" {
		t.Errorf("wrong name: %q", a.Name)
	}
}

// Table-driven test: several error cases in a list, run in a loop.
// It is the most common testing style in Go.
func TestParseErrors(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		contains string // expected fragment of the error message
	}{
		{"no header", "name: iris\n", "must start with a --- line"},
		{"unclosed header", "---\nname: iris\n", "closes the header"},
		{"no name", "---\nrole: x\n---\nhi\n", "missing the name field"},
		{"name with space", "---\nname: a b\n---\nhi\n", "invalid name"},
		{"no instructions", "---\nname: iris\n---\n", "has no instructions"},
		{"invalid access", "---\nname: iris\ntools:\n  data: all\n---\nhi\n", "access"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(c.content)
			if err == nil {
				t.Fatal("expected an error, got success")
			}
			if !strings.Contains(err.Error(), c.contains) {
				t.Errorf("error %q does not contain %q", err, c.contains)
			}
		})
	}
}

func TestLoadTeamChecksCanCall(t *testing.T) {
	// t.TempDir creates a temporary folder that is removed when the test ends.
	dir := t.TempDir()
	write(t, dir, "a.md", "---\nname: ana\ncan_call: [bia]\n---\nhi\n")
	write(t, dir, "b.md", "---\nname: bia\ncan_call: [zeca]\n---\nhi\n")

	_, err := LoadTeam(dir)
	if err == nil || !strings.Contains(err.Error(), "zeca") {
		t.Fatalf("expected an error about the missing agent zeca, got: %v", err)
	}
}

func TestLoadTeamDuplicateName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.md", "---\nname: ana\n---\nhi\n")
	write(t, dir, "b.md", "---\nname: ana\n---\nhi\n")

	_, err := LoadTeam(dir)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected a duplicate name error, got: %v", err)
	}
}

func TestSampleTeamLoads(t *testing.T) {
	// Makes sure the project's sample agents stay valid.
	team, err := LoadTeam(filepath.Join("..", "..", "examples", "dados", "agents"))
	if err != nil {
		t.Fatalf("the sample agents should load: %v", err)
	}
	if len(team) != 3 {
		t.Errorf("expected 3 agents, got %d", len(team))
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper() // on failure, the error points at the caller, not here
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A file from before keeps loading: its harness line is read and ignored.
func TestAnOldHarnessLineIsIgnored(t *testing.T) {
	a, err := Parse("---\nname: iris\nharness: codex\n---\nhi\n")
	if err != nil || a.Harness != "codex" {
		t.Errorf("loads: %+v %v", a, err)
	}
}
