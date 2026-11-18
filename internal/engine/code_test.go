package engine

import (
	"strings"
	"testing"

	"tubas/internal/agent"
	"tubas/internal/tools"
)

func TestWriterWorksInItsWorktree(t *testing.T) {
	wt, front := t.TempDir(), t.TempDir()
	made := 0
	a := agent.Agent{Name: "leo", Tools: map[string]string{"api": "write", "front": "read", "data": "read"}}
	code, err := codePlaces(TurnRequest{Agent: a, Repos: map[string]string{"api": "/repos/api", "front": front},
		Workspace: func(repo string) (string, error) {
			made++
			if repo != "api" {
				t.Errorf("only the repo it writes gets a worktree, asked for %s", repo)
			}
			return wt, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if made != 1 || len(code.write) != 1 || code.write[0] != wt || code.all[1] != front {
		t.Errorf("a worktree to write, the other repo to read: %+v (made %d)", code, made)
	}
	box := &tools.Box{Read: code.all, Write: code.write}
	prompt := box.Prompt(code.names)
	if !strings.Contains(prompt, "api: your own worktree at "+wt) || !strings.Contains(prompt, "front (read only)") {
		t.Errorf("the agent is told where it works:\n%s", prompt)
	}
}

func TestReaderOnlyReads(t *testing.T) {
	front := t.TempDir()
	a := agent.Agent{Name: "nico", Tools: map[string]string{"front": "read"}}
	code, _ := codePlaces(TurnRequest{Agent: a, Repos: map[string]string{"front": front}, Workspace: func(string) (string, error) {
		t.Error("a reader needs no worktree")
		return "", nil
	}})
	box := &tools.Box{Read: code.all, Write: code.write}
	if box.Has("bash") || box.Has("edit") || !box.Has("read") {
		t.Errorf("a reader reads and searches, nothing more: %+v", code)
	}
}

func TestNoCodeNoFileTools(t *testing.T) {
	a := agent.Agent{Name: "iris", Tools: map[string]string{"data": "read"}}
	code, _ := codePlaces(TurnRequest{Agent: a, Repos: map[string]string{"api": "/repos/api"}})
	box := &tools.Box{Read: code.all, Write: code.write}
	if box.Has("read") || box.Has("bash") {
		t.Error("an agent without repo access gets no file tools")
	}
}
