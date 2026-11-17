package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func askPermission(t *testing.T, g *Gateway, tool string, input map[string]any) map[string]any {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"tool_name": tool, "input": input, "tool_use_id": "x"})
	text, isErr := testResultText(talk(t, g, call("permission", string(args)))[0])
	if isErr {
		t.Fatalf("permission tool failed: %s", text)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		t.Fatalf("not JSON: %s", text)
	}
	return answer
}

func TestPermissionRules(t *testing.T) {
	wt := t.TempDir()
	var asked atomic.Int32
	var lastCall atomic.Value
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		lastCall.Store(req)
		approved := strings.Contains(req["call"], "pytest")
		json.NewEncoder(w).Encode(map[string]bool{"approved": approved})
	}))
	defer broker.Close()
	t.Setenv("TUBAS_WRITE_DIRS", wt)
	g := gateway(t, testTeam(t), "writer", broker.URL, "secret")
	if !strings.Contains(strings.Join(g.ToolNames(), ","), "permission") {
		t.Fatal("with write dirs, the gateway should offer the permission tool")
	}

	if a := askPermission(t, g, "Edit", map[string]any{"file_path": filepath.Join(wt, "src", "app.py")}); a["behavior"] != "allow" {
		t.Errorf("an edit inside the worktree should be allowed: %v", a)
	}
	outside := filepath.Join(t.TempDir(), "secrets.env")
	if a := askPermission(t, g, "Write", map[string]any{"file_path": outside}); a["behavior"] != "deny" {
		t.Errorf("a write outside the worktree must be denied: %v", a)
	}
	if a := askPermission(t, g, "Edit", map[string]any{"file_path": filepath.Join(wt, "..", "escape.py")}); a["behavior"] != "deny" {
		t.Errorf("../ must not escape: %v", a)
	}
	if asked.Load() != 0 {
		t.Error("edits never ask a human")
	}

	a := askPermission(t, g, "Bash", map[string]any{"command": "uv run pytest -x", "description": "run the tests"})
	if a["behavior"] != "allow" || asked.Load() != 1 {
		t.Errorf("an approved command should be allowed after asking: %v", a)
	}
	req := lastCall.Load().(map[string]string)
	if !strings.Contains(req["detail"], "$ uv run pytest -x") || !strings.Contains(req["detail"], "why: run the tests") {
		t.Errorf("the inbox should get the whole command: %q", req["detail"])
	}
	if a := askPermission(t, g, "Bash", map[string]any{"command": "rm -rf /"}); a["behavior"] != "deny" {
		t.Errorf("a denied command must be denied: %v", a)
	}
	if a := askPermission(t, g, "WebFetch", map[string]any{"url": "https://example.com"}); a["behavior"] != "deny" {
		t.Errorf("other tools are denied: %v", a)
	}
}

func TestSymlinkOutOfTheWorktreeIsDenied(t *testing.T) {
	wt, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(wt, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks need privileges here")
	}
	if insideAny(filepath.Join(link, "x.py"), []string{wt}) {
		t.Error("a symlink out of the worktree must not count as inside")
	}
}

func TestNoPermissionToolWithoutWriteDirs(t *testing.T) {
	t.Setenv("TUBAS_WRITE_DIRS", "")
	g := gateway(t, testTeam(t), "writer", "", "")
	for _, n := range g.ToolNames() {
		if n == "permission" {
			t.Error("without worktrees there is nothing to answer")
		}
	}
}

func TestRunCommandWaitsForYou(t *testing.T) {
	wt := t.TempDir()
	var asked atomic.Int32
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		approved := strings.Contains(req["call"], "echo")
		json.NewEncoder(w).Encode(map[string]bool{"approved": approved})
	}))
	defer broker.Close()
	t.Setenv("TUBAS_WRITE_DIRS", wt)
	t.Setenv("TUBAS_HARNESS", "codex")
	g := gateway(t, testTeam(t), "writer", broker.URL, "secret")
	names := strings.Join(g.ToolNames(), ",")
	if !strings.Contains(names, "code__run_command") || strings.Contains(names, "permission") {
		t.Fatalf("another harness gets run_command instead of the permission tool: %s", names)
	}
	text, isErr := testResultText(talk(t, g, call("code__run_command", `{"command":"echo tubas-ran","why":"test"}`))[0])
	if isErr || !strings.Contains(text, "tubas-ran") || !strings.Contains(text, "exit 0") || asked.Load() != 1 {
		t.Errorf("an approved command runs in the worktree: %q (asked %d)", text, asked.Load())
	}
	text, isErr = testResultText(talk(t, g, call("code__run_command", `{"command":"del everything"}`))[0])
	if !isErr || !strings.Contains(text, "denied") || asked.Load() != 2 {
		t.Errorf("a denied command never runs: %q", text)
	}
}
