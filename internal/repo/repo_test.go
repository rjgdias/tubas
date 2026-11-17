package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepo makes a git repository with one commit on main.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(dir, "app.py"), []byte("TIMEOUT = 45\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "first")
	return dir
}

func TestWorktreeLifecycle(t *testing.T) {
	checkout := newRepo(t)
	team := t.TempDir()
	set := NewSet(team, "s3", "Corrige o timeout!", map[string]Spec{"api": {Path: checkout}})
	path, err := set.Workspace("api")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := set.Workspace("api"); again != path {
		t.Error("the same session should reuse its worktree")
	}
	wt := set.Worktrees()[0]
	if wt.Branch != "tubas/s3-corrige-o-timeout" || wt.Base != "main" {
		t.Errorf("worktree = %+v", wt)
	}

	// An agent edits one file and adds another.
	os.WriteFile(filepath.Join(path, "app.py"), []byte("TIMEOUT = 60\n"), 0o644)
	os.WriteFile(filepath.Join(path, "notes.md"), []byte("why\n"), 0o644)
	ch, patch, err := Diff(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Files) != 2 || ch.Added != 2 || ch.Removed != 1 || !strings.Contains(patch, "+TIMEOUT = 60") {
		t.Fatalf("changes = %+v\n%s", ch, patch)
	}
	if data, _ := os.ReadFile(filepath.Join(checkout, "app.py")); string(data) != "TIMEOUT = 45\n" {
		t.Error("your checkout must stay untouched")
	}

	if err := Merge(wt, "tubas: corrige o timeout"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(checkout, "app.py")); strings.TrimSpace(string(data)) != "TIMEOUT = 60" {
		log, _ := exec.Command("git", "-C", checkout, "log", "--oneline", "--all").CombinedOutput()
		t.Errorf("after merge, the change should be in main: %q\n%s", data, log)
	}
	if err := Discard(wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("discard should remove the worktree")
	}
}

func TestMergeRefusesADirtyCheckout(t *testing.T) {
	checkout := newRepo(t)
	wt, err := Create(checkout, t.TempDir(), "api", "s1-x", "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt.Path, "app.py"), []byte("TIMEOUT = 60\n"), 0o644)
	os.WriteFile(filepath.Join(checkout, "app.py"), []byte("TIMEOUT = 50\n"), 0o644) // yours, not committed
	if err := Merge(wt, "x"); err == nil || !strings.Contains(err.Error(), "not committed") {
		t.Errorf("merge must not touch a checkout with your changes: %v", err)
	}
}

func TestNamesNeverCollide(t *testing.T) {
	checkout := newRepo(t)
	team := t.TempDir()
	a, err := Create(checkout, team, "api", "s1-x", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create(checkout, team, "api", "s1-x", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Path == b.Path || a.Branch == b.Branch {
		t.Errorf("two worktrees share a name: %s %s", a.Branch, b.Branch)
	}
}

func TestSetupRunsInTheWorktree(t *testing.T) {
	checkout := newRepo(t)
	set := NewSet(t.TempDir(), "s1", "x", map[string]Spec{"api": {Path: checkout, Setup: "echo ok> setup.txt"}})
	path, err := set.Workspace("api")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "setup.txt")); err != nil {
		t.Error("setup should run inside the new worktree")
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Corrige o timeout!": "corrige-o-timeout", "  ": "task", "Ação nº 1": "acao-n-1"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
