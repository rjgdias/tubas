package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tubas/internal/engine"
	"tubas/internal/repo"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "app.py"), []byte("TIMEOUT = 45\n"), 0o644)
	exec.Command("git", "-C", dir, "add", ".").Run()
	if out, err := exec.Command("git", "-C", dir, "commit", "-q", "-m", "first").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return dir
}

func TestReviewAndMerge(t *testing.T) {
	checkout := gitRepo(t)
	m := teamModel(t, map[string]engine.Script{"leo": {Summary: "subi o timeout"}})
	m.cfg.Repos = map[string]repo.Spec{"api": {Path: checkout}}
	m = m.Ask("@leo corrige o timeout").Settle(0)

	// What leo would have done in the session's worktree.
	path, err := m.code.Workspace("api")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(path, "app.py"), []byte("TIMEOUT = 60\n"), 0o644)
	m = m.loadChanges()

	contains(t, m.Snapshot(128, 32), "✎ api", "1 files", "+1", "v review")
	m = m.PressKeys("v")
	if !m.review {
		t.Fatal("v should open the review")
	}
	contains(t, m.Snapshot(128, 32), "review · api", "app.py", "+TIMEOUT = 60", "-TIMEOUT = 45", "merge into main", "untouched")

	next, cmd := m.reviewKey("m")
	m = drive(next, cmd, 5)
	if data, _ := os.ReadFile(filepath.Join(checkout, "app.py")); strings.TrimSpace(string(data)) != "TIMEOUT = 60" {
		t.Fatalf("m should merge into main: %q · %s", data, m.notice)
	}
	if len(m.changes) != 0 || m.review {
		t.Error("after merging, nothing is left to review")
	}
}

func TestDiscardAsksTwice(t *testing.T) {
	checkout := gitRepo(t)
	m := teamModel(t, map[string]engine.Script{"leo": {Summary: "ok"}})
	m.cfg.Repos = map[string]repo.Spec{"api": {Path: checkout}}
	m = m.Ask("@leo tenta algo").Settle(0)
	path, _ := m.code.Workspace("api")
	os.WriteFile(filepath.Join(path, "new.py"), []byte("x = 1\n"), 0o644)
	m = m.loadChanges().PressKeys("v")
	next, cmd := m.reviewKey("d")
	if cmd != nil {
		t.Fatal("the first d only asks")
	}
	next, cmd = next.reviewKey("d")
	m = drive(next, cmd, 5)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the second d should remove the worktree")
	}
}
