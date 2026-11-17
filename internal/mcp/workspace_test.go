package mcp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func newWorkspace(t *testing.T, agentName string) (workspace, string) {
	t.Helper()
	dir := t.TempDir()
	return workspace{root: filepath.Join(dir, "workspaces"), shared: filepath.Join(dir, "shared"), agent: agentName}, dir
}

func TestWorkspaceOwnFolderAndShared(t *testing.T) {
	w, dir := newWorkspace(t, "iris")
	if _, err := w.write("notas/marco.md", "queda no sul"); err != nil {
		t.Fatal(err)
	}
	if got, err := w.read("notas/marco.md"); err != nil || got != "queda no sul" {
		t.Errorf("read own = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "workspaces", "iris", "notas", "marco.md")); err != nil {
		t.Errorf("the file should live in iris's folder: %v", err)
	}
	if _, err := w.write("shared/decisoes.md", "usar média por dia"); err != nil {
		t.Fatal(err)
	}
	leo := workspace{root: w.root, shared: w.shared, agent: "leo"}
	if got, _ := leo.read("shared/decisoes.md"); got != "usar média por dia" {
		t.Errorf("leo should read the shared folder, got %q", got)
	}
	if got, _ := leo.read("@iris/notas/marco.md"); got != "queda no sul" {
		t.Errorf("leo should read iris's folder, got %q", got)
	}
	if list, _ := leo.list("@iris"); !strings.Contains(list, "notas/") {
		t.Errorf("list of @iris = %q", list)
	}
}

func TestWorkspaceRefusesEscapes(t *testing.T) {
	w, dir := newWorkspace(t, "leo")
	w.write("ok.md", "x")
	cases := map[string]string{
		"../iris/x.md":          "leaves the workspace",
		"notas/../../x.md":      "leaves the workspace",
		"/etc/passwd":           "absolute",
		filepath.Join(dir, "x"): "absolute",
		"@../x":                 "unknown agent folder",
	}
	for path, want := range cases {
		if _, err := w.read(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("read(%q) = %v, want an error about %q", path, err, want)
		}
	}
	if _, err := w.write("@iris/x.md", "invasão"); err == nil || !strings.Contains(err.Error(), "only write in your own folder") {
		t.Errorf("writing in another agent's folder should fail, got %v", err)
	}
}

func TestWorkspaceRefusesSymlinksOut(t *testing.T) {
	w, _ := newWorkspace(t, "leo")
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("segredo"), 0o644)
	own := filepath.Join(w.root, "leo")
	os.MkdirAll(own, 0o755)
	if err := os.Symlink(outside, filepath.Join(own, "fuga")); err != nil {
		t.Skip("cannot create symlinks here:", err) // Windows without developer mode
	}
	if _, err := w.read("fuga/secret.txt"); err == nil {
		t.Error("a symlink out of the workspace must be refused")
	}
}

// Writing in your own folder never asks for approval; it is your space.
func TestWorkspaceWritesNeedNoApproval(t *testing.T) {
	var asked atomic.Int32
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Write([]byte(`{"approved":false}`))
	}))
	defer broker.Close()

	g := gateway(t, testTeam(t), "writer", broker.URL, "secret")
	text, isErr := testResultText(talk(t, g, call("workspace__write_file", `{"path":"plano.md","content":"passo 1"}`))[0])
	if isErr || !strings.HasPrefix(text, "wrote plano.md") {
		t.Errorf("write = %q (error=%v)", text, isErr)
	}
	if asked.Load() != 0 {
		t.Errorf("writing in the own workspace asked for approval %d times", asked.Load())
	}
}
