package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tubas/internal/agent"
)

// started runs one turn with the fake claude and returns its arguments
// and the folder it ran in.
func started(t *testing.T, a agent.Agent, repos map[string]string, workspace func(string) (string, error)) ([]string, string) {
	t.Helper()
	exe, _ := os.Executable()
	t.Setenv("TUBAS_FAKE_CLAUDE", "args")
	var gotDirs []string
	eng := ClaudeEngine{Command: exe, WorkDir: t.TempDir(),
		Tools: func(a agent.Agent, turn string, writeDirs []string) (string, []string, func(), error) {
			gotDirs = writeDirs
			return "", nil, func() {}, nil
		}}
	reply, err := eng.RunTurn(context.Background(), TurnRequest{Agent: a, Prompt: "hi", Repos: repos, Workspace: workspace}, make(chan Event, 16))
	if err != nil {
		t.Fatal(err)
	}
	var seen struct {
		Args []string `json:"args"`
		Dir  string   `json:"dir"`
	}
	if err := json.Unmarshal([]byte(reply.Text), &seen); err != nil {
		t.Fatalf("fake claude said %q", reply.Text)
	}
	if len(gotDirs) > 0 && gotDirs[0] != seen.Dir {
		t.Errorf("the gateway should get the worktree %s, got %v", seen.Dir, gotDirs)
	}
	return seen.Args, seen.Dir
}

func argAfter(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func TestWriterWorksInItsWorktree(t *testing.T) {
	wt, front := t.TempDir(), t.TempDir()
	made := 0
	a := agent.Agent{Name: "leo", Model: "haiku", Instructions: "x", Tools: map[string]string{"api": "write", "front": "read", "data": "read"}}
	args, dir := started(t, a, map[string]string{"api": "/repos/api", "front": front}, func(repo string) (string, error) {
		made++
		if repo != "api" {
			t.Errorf("only the repo it writes gets a worktree, asked for %s", repo)
		}
		return wt, nil
	})
	if made != 1 {
		t.Errorf("one worktree, made %d times", made)
	}
	if abs, _ := filepath.EvalSymlinks(dir); abs != mustEval(t, wt) {
		t.Errorf("claude should run in the worktree, ran in %s", dir)
	}
	if got := argAfter(args, "--tools"); got != "Read,Grep,Glob,Edit,Write,Bash,PowerShell" {
		t.Errorf("--tools = %q", got)
	}
	if argAfter(args, "--permission-prompts") != "host" || argAfter(args, "--permission-prompt-tool") != "mcp__tubas__permission" {
		t.Errorf("edits and commands should go to the gateway: %v", args)
	}
	if argAfter(args, "--add-dir") != front {
		t.Errorf("the read-only repo should be added: %v", args)
	}
	prompt := argAfter(args, "--system-prompt")
	if !strings.Contains(prompt, "## Code") || !strings.Contains(prompt, "your own worktree at "+wt) || !strings.Contains(prompt, "front (read only)") {
		t.Errorf("the agent should be told where it works:\n%s", prompt)
	}
}

func TestReaderOnlyReads(t *testing.T) {
	front := t.TempDir()
	a := agent.Agent{Name: "nico", Model: "haiku", Instructions: "x", Tools: map[string]string{"front": "read"}}
	args, _ := started(t, a, map[string]string{"front": front}, func(string) (string, error) {
		t.Error("a reader needs no worktree")
		return "", nil
	})
	if got := argAfter(args, "--tools"); got != "Read,Grep,Glob" {
		t.Errorf("--tools = %q", got)
	}
	if argAfter(args, "--permission-prompts") != "none" {
		t.Error("a reader's prompts are denied, never asked")
	}
}

func TestNoCodeNoTools(t *testing.T) {
	a := agent.Agent{Name: "iris", Model: "haiku", Instructions: "x", Tools: map[string]string{"data": "read"}}
	args, _ := started(t, a, map[string]string{"api": "/repos/api"}, nil)
	if argAfter(args, "--tools") != "" {
		t.Error("an agent without repo access keeps no built-in tools")
	}
}

func mustEval(t *testing.T, p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
