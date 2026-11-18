package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// box makes a worktree and a read-only repository in temp folders.
func box(t *testing.T) (*Box, string, string, *[]string) {
	t.Helper()
	wt, repo := t.TempDir(), t.TempDir()
	var asked []string
	var mu sync.Mutex
	b := &Box{Write: []string{wt}, Read: []string{wt, repo}, Ask: func(ctx context.Context, call, detail string) (bool, string, error) {
		mu.Lock()
		asked = append(asked, call)
		mu.Unlock()
		return !strings.Contains(detail, "deny"), "", nil
	}}
	return b, wt, repo, &asked
}

func call(t *testing.T, b *Box, name string, args map[string]any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	return b.Call(context.Background(), name, raw)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadNumbersLinesAndComesInParts(t *testing.T) {
	b, wt, repo, _ := box(t)
	write(t, filepath.Join(repo, "a.txt"), "one\r\ntwo\nthree\nfour\n")
	out, failed := call(t, b, "read", map[string]any{"path": filepath.Join(repo, "a.txt"), "offset": 2, "limit": 2})
	if failed || out != "     2\ttwo\n     3\tthree\n… (1 more lines: read again with offset 4)\n" {
		t.Errorf("read: %q", out)
	}
	write(t, filepath.Join(wt, "bin.dat"), "PK\x00\x01")
	if out, failed := call(t, b, "read", map[string]any{"path": "bin.dat"}); !failed || !strings.Contains(out, "binary") {
		t.Errorf("a binary file: %q", out)
	}
	for _, p := range []string{"../outside.txt", filepath.Join(os.TempDir(), "x.txt"), ""} {
		if out, failed := call(t, b, "read", map[string]any{"path": p}); !failed {
			t.Errorf("%q should be refused: %q", p, out)
		}
	}
	if out, failed := call(t, b, "read", map[string]any{"path": "nope.go"}); !failed || !strings.Contains(out, "doesn't exist") {
		t.Errorf("a missing file: %q", out)
	}
}

func TestALinkOutOfTheWorktreeIsRefused(t *testing.T) {
	b, wt, _, _ := box(t)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	write(t, secret, "token")
	if err := os.Symlink(secret, filepath.Join(wt, "link.txt")); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	if out, failed := call(t, b, "read", map[string]any{"path": "link.txt"}); !failed || strings.Contains(out, "token") {
		t.Errorf("the link was followed out: %q", out)
	}
}

func TestWriteOnlyAfterReadingAndOnlyInTheWorktree(t *testing.T) {
	b, wt, repo, _ := box(t)
	if out, failed := call(t, b, "write", map[string]any{"path": "new/file.go", "content": "package x\n"}); failed || !strings.Contains(out, "created") {
		t.Errorf("a new file: %q", out)
	}
	write(t, filepath.Join(wt, "old.go"), "old\n")
	if out, failed := call(t, b, "write", map[string]any{"path": "old.go", "content": "new\n"}); !failed || !strings.Contains(out, "read") {
		t.Errorf("replacing a file it never read: %q", out)
	}
	call(t, b, "read", map[string]any{"path": "old.go"})
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(wt, "old.go"), "someone else\n")
	if out, failed := call(t, b, "write", map[string]any{"path": "old.go", "content": "new\n"}); !failed || !strings.Contains(out, "changed since") {
		t.Errorf("a file changed after the read: %q", out)
	}
	call(t, b, "read", map[string]any{"path": "old.go"})
	if out, failed := call(t, b, "write", map[string]any{"path": "old.go", "content": "new\n"}); failed {
		t.Errorf("after reading it again: %q", out)
	}
	write(t, filepath.Join(repo, "r.go"), "r\n")
	call(t, b, "read", map[string]any{"path": filepath.Join(repo, "r.go")})
	if out, failed := call(t, b, "write", map[string]any{"path": filepath.Join(repo, "r.go"), "content": "x"}); !failed || !strings.Contains(out, "read only") {
		t.Errorf("the read-only repository: %q", out)
	}
}

func TestEdit(t *testing.T) {
	b, wt, _, _ := box(t)
	path := filepath.Join(wt, "m.go")
	write(t, path, "a := 1\r\nb := 1\r\nc := 2\r\n")
	if out, failed := call(t, b, "edit", map[string]any{"path": "m.go", "old_string": "c := 2", "new_string": "c := 3"}); !failed || !strings.Contains(out, "read") {
		t.Errorf("an edit before reading: %q", out)
	}
	call(t, b, "read", map[string]any{"path": "m.go"})
	if out, failed := call(t, b, "edit", map[string]any{"path": "m.go", "old_string": ":= 1", "new_string": ":= 9"}); !failed || !strings.Contains(out, "2 times") {
		t.Errorf("two matches: %q", out)
	}
	if out, failed := call(t, b, "edit", map[string]any{"path": "m.go", "old_string": "b := 1\nc := 2", "new_string": "b := 1\nc := 3\nd := 4"}); failed || !strings.Contains(out, "line 2") {
		t.Errorf("a unique match over two lines: %q", out)
	}
	if out, failed := call(t, b, "edit", map[string]any{"path": "m.go", "old_string": ":= 1", "new_string": ":= 0", "replace_all": true}); failed {
		t.Errorf("replace_all: %q", out)
	}
	if out, failed := call(t, b, "edit", map[string]any{"path": "m.go", "old_string": "zzz", "new_string": "y"}); !failed || !strings.Contains(out, "isn't in") {
		t.Errorf("no match: %q", out)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "a := 0\r\nb := 0\r\nc := 3\r\nd := 4\r\n" {
		t.Errorf("the file keeps its Windows line ends: %q", data)
	}
}

func TestGlobAndGrep(t *testing.T) {
	b, wt, repo, _ := box(t)
	write(t, filepath.Join(repo, "cmd", "main.go"), "package main\nfunc Main() {}\n")
	write(t, filepath.Join(repo, "lib", "deep", "util.go"), "package deep\n// TODO: speed\nfunc util() {}\n")
	write(t, filepath.Join(repo, "README.md"), "# todo app\n")
	write(t, filepath.Join(repo, "node_modules", "x", "y.go"), "package y // TODO\n")
	write(t, filepath.Join(wt, "notes.txt"), "nothing\n")

	out, _ := call(t, b, "glob", map[string]any{"pattern": "**/*.go", "path": repo})
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "util.go") || strings.Contains(out, "node_modules") {
		t.Errorf("**/*.go: %q", out)
	}
	if out, _ := call(t, b, "glob", map[string]any{"pattern": "*.{md,txt}"}); !strings.Contains(out, "README.md") || !strings.Contains(out, "notes.txt") || strings.Contains(out, ".go") {
		t.Errorf("*.{md,txt} in every folder: %q", out)
	}
	for _, rg := range []bool{true, false} {
		useRipgrep = rg
		if rg && ripgrep() == "" {
			continue
		}
		out, failed := call(t, b, "grep", map[string]any{"pattern": "todo", "ignore_case": true, "path": repo})
		if failed || !strings.Contains(out, "util.go:2:") || !strings.Contains(out, "README.md:1:") || strings.Contains(out, "node_modules") {
			t.Errorf("rg=%v content: %q", rg, out)
		}
		if out, _ := call(t, b, "grep", map[string]any{"pattern": "func", "glob": "*.go", "mode": "files"}); strings.Count(out, "\n") != 1 || strings.Contains(out, ":") && strings.Contains(out, "func") {
			t.Errorf("rg=%v files: %q", rg, out)
		}
		if out, _ := call(t, b, "grep", map[string]any{"pattern": "zzz"}); out != "no matches" {
			t.Errorf("rg=%v nothing: %q", rg, out)
		}
	}
	useRipgrep = true
}

func TestGlobLeavesOutWhatGitIgnores(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	b, _, repo, _ := box(t)
	exec.Command("git", "-C", repo, "init", "-q").Run()
	write(t, filepath.Join(repo, ".gitignore"), "build/\n")
	write(t, filepath.Join(repo, "build", "out.go"), "package build\n")
	write(t, filepath.Join(repo, "src", "a.go"), "package src\n")
	out, _ := call(t, b, "glob", map[string]any{"pattern": "**/*.go", "path": repo})
	if !strings.Contains(out, "a.go") || strings.Contains(out, "out.go") {
		t.Errorf("git's list: %q", out)
	}
}

func TestBashKeepsItsFolderAndAsksEveryTime(t *testing.T) {
	b, wt, _, asked := box(t)
	os.MkdirAll(filepath.Join(wt, "sub"), 0o755)
	if out, failed := call(t, b, "bash", map[string]any{"command": "cd sub"}); failed {
		t.Fatalf("cd: %q", out)
	}
	out, failed := call(t, b, "bash", map[string]any{"command": "echo hi > here.txt && pwd"})
	if failed {
		t.Fatalf("echo: %q", out)
	}
	if _, err := os.Stat(filepath.Join(wt, "sub", "here.txt")); err != nil {
		t.Errorf("the second command ran in sub: %q", out)
	}
	if len(*asked) != 2 {
		t.Errorf("every command asks: %v", *asked)
	}
	if out, failed := call(t, b, "bash", map[string]any{"command": "echo deny-me > gone.txt", "why": "deny"}); !failed || !strings.Contains(out, "denied") {
		t.Errorf("denied: %q", out)
	}
	if _, err := os.Stat(filepath.Join(wt, "sub", "gone.txt")); err == nil {
		t.Error("a denied command ran")
	}
	if out, failed := call(t, b, "bash", map[string]any{"command": "cd " + filepath.ToSlash(os.TempDir())}); failed || !strings.Contains(out, "back in") {
		t.Errorf("leaving the worktree: %q", out)
	}
	if out, failed := call(t, b, "bash", map[string]any{"command": "exit 3"}); !failed || !strings.HasPrefix(out, "exit 3") {
		t.Errorf("a failing command: %q", out)
	}
}

func TestAllowedCommandsRunWithoutAsking(t *testing.T) {
	b, _, _, asked := box(t)
	b.Allow = []string{"echo"}
	if out, failed := call(t, b, "bash", map[string]any{"command": "echo ok"}); failed || out != "ok" {
		t.Errorf("allowed: %q", out)
	}
	if len(*asked) != 0 {
		t.Errorf("an allowed command asked: %v", *asked)
	}
	for _, c := range []string{"echo ok; rm -rf x", "echo $(whoami)", "echo ok && ls", "echoes", "echo ok | cat"} {
		if b.allowed(c) {
			t.Errorf("%q chains something after the allowed prefix", c)
		}
	}
}

func TestATimedOutCommandIsStopped(t *testing.T) {
	b, _, _, _ := box(t)
	start := time.Now()
	out, failed := call(t, b, "bash", map[string]any{"command": "sleep 20", "timeout": 1})
	if !failed || !strings.Contains(out, "stopped") || time.Since(start) > 10*time.Second {
		t.Errorf("after %s: %q", time.Since(start), out)
	}
}

func TestTodosReachTheScreen(t *testing.T) {
	b, _, _, _ := box(t)
	var got []Todo
	b.Todos = func(t []Todo) { got = t }
	out, failed := call(t, b, "todo_write", map[string]any{"todos": []map[string]string{{"content": "read", "status": "completed"}, {"content": "fix", "status": "in_progress"}}})
	if failed || len(got) != 2 || out != "checklist saved: 1 of 2 done" {
		t.Errorf("%q %v", out, got)
	}
	if out, failed := call(t, b, "todo_write", map[string]any{"todos": []map[string]string{{"content": "a", "status": "in_progress"}, {"content": "b", "status": "in_progress"}}}); !failed {
		t.Errorf("two at once: %q", out)
	}
}

func TestWebFetchAsksOncePerSite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>x</title><script>evil()</script></head><body><h2>Install</h2><p>Run <code>go get</code> &amp; go.</p><ul><li>one</li></ul></body></html>`)
	}))
	defer srv.Close()
	b, _, _, asked := box(t)
	sites := &sync.Map{}
	b.Domains = sites
	out, failed := call(t, b, "web_fetch", map[string]any{"url": srv.URL + "/docs"})
	if failed || !strings.Contains(out, "## Install") || !strings.Contains(out, "Run go get & go.") || !strings.Contains(out, "- one") || strings.Contains(out, "evil") {
		t.Errorf("the page as text: %q", out)
	}
	call(t, b, "web_fetch", map[string]any{"url": srv.URL + "/other"})
	if len(*asked) != 1 {
		t.Errorf("one question per site: %v", *asked)
	}
	b2, _, _, asked2 := box(t)
	b2.Domains = sites // the same conversation, the next turn
	call(t, b2, "web_fetch", map[string]any{"url": srv.URL})
	if len(*asked2) != 0 {
		t.Errorf("the next turn asked again: %v", *asked2)
	}
	if out, failed := call(t, b, "web_fetch", map[string]any{"url": "file:///etc/passwd"}); !failed {
		t.Errorf("not the web: %q", out)
	}
}

func TestToolsFollowTheFolders(t *testing.T) {
	names := func(b *Box) string {
		var n []string
		for _, t := range b.Tools() {
			n = append(n, t.Name)
		}
		return strings.Join(n, " ")
	}
	if got := names(&Box{}); got != "todo_write web_fetch" {
		t.Errorf("no folders: %s", got)
	}
	if got := names(&Box{Read: []string{"r"}}); got != "read glob grep todo_write web_fetch" {
		t.Errorf("read only: %s", got)
	}
	if got := names(&Box{Write: []string{"w"}}); !strings.Contains(got, "edit") || !strings.Contains(got, "bash") {
		t.Errorf("a worktree: %s", got)
	}
	if runtime.GOOS == "windows" {
		if exe, kind := Shell(); kind == "sh" && !strings.HasSuffix(strings.ToLower(exe), "bash.exe") {
			t.Errorf("Git Bash on Windows: %s", exe)
		}
	}
}
