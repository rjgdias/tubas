// Package repo gives each task its own git worktree.
//
// A repository is a connector in team.yaml (repo: path). When an agent
// with write access works on it, the session gets a worktree of its own
// in .tubas/worktrees, on a branch of its own (tubas/<session>-<title>).
// Agents edit there freely; your checkout and its branch are never
// touched. At the end you review the diff and merge it, push it for a
// pull request, or discard it.
package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Worktree is one task's copy of a repository.
type Worktree struct {
	Repo     string `json:"repo"`     // the connector's name
	Checkout string `json:"checkout"` // your repository
	Path     string `json:"path"`     // the worktree
	Branch   string `json:"branch"`   // its branch
	Base     string `json:"base"`     // the branch it started from
	BaseSHA  string `json:"base_sha"` // the commit it started from
	SetupErr string `json:"setup_err,omitempty"`
}

// git runs a git command and returns its output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func ctx30() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// IsRepo says whether a folder is a git repository.
func IsRepo(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git")) // a folder, or a file in a worktree
	return err == nil
}

// CurrentBranch is the branch checked out in a repository.
func CurrentBranch(path string) (string, error) {
	ctx, cancel := ctx30()
	defer cancel()
	return git(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a title into a branch-friendly name: "Corrige o timeout!" ->
// "corrige-o-timeout".
func Slug(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("á", "a", "à", "a", "ã", "a", "â", "a", "é", "e", "ê", "e", "í", "i",
		"ó", "o", "õ", "o", "ô", "o", "ú", "u", "ç", "c").Replace(s)
	s = strings.Trim(notSlug.ReplaceAllString(s, "-"), "-")
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	if s == "" {
		s = "task"
	}
	return s
}

// Create makes a worktree of checkout for a task, on a new branch from
// base (the checkout's current branch when empty).
func Create(checkout, teamDir, connector, task, base string) (Worktree, error) {
	if !IsRepo(checkout) {
		return Worktree{}, fmt.Errorf("%s is not a git repository", checkout)
	}
	ctx, cancel := ctx30()
	defer cancel()
	if base == "" {
		b, err := git(ctx, checkout, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return Worktree{}, err
		}
		base = b
	}
	sha, err := git(ctx, checkout, "rev-parse", base)
	if err != nil {
		return Worktree{}, err
	}
	name := connector + "-" + task
	path := filepath.Join(teamDir, ".tubas", "worktrees", name)
	branch := "tubas/" + task
	for i := 2; ; i++ { // never reuse a folder or a branch
		_, statErr := os.Stat(path)
		_, branchErr := git(ctx, checkout, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
		if os.IsNotExist(statErr) && branchErr != nil {
			break
		}
		path = filepath.Join(teamDir, ".tubas", "worktrees", name+"-"+strconv.Itoa(i))
		branch = "tubas/" + task + "-" + strconv.Itoa(i)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Worktree{}, err
	}
	if _, err := git(ctx, checkout, "worktree", "add", "-b", branch, path, sha); err != nil {
		return Worktree{}, err
	}
	abs, _ := filepath.Abs(path)
	return Worktree{Repo: connector, Checkout: checkout, Path: abs, Branch: branch, Base: base, BaseSHA: sha}, nil
}

// Setup runs the repository's setup command (uv sync, npm install…) in a
// new worktree. It is your command, from team.yaml, so it doesn't ask.
func Setup(wt Worktree, command string) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = wt.Path
	out, err := cmd.CombinedOutput()
	if err != nil {
		last := strings.TrimSpace(string(out))
		if i := strings.LastIndex(last, "\n"); i >= 0 {
			last = last[i+1:]
		}
		return fmt.Errorf("setup %q: %v %s", command, err, last)
	}
	return nil
}

// FileChange is one file in a task's diff.
type FileChange struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// Changes is everything a task changed: committed or not, new files too.
type Changes struct {
	Files   []FileChange `json:"files"`
	Added   int          `json:"added"`
	Removed int          `json:"removed"`
}

// Diff is what the worktree changed since it started: the numbers and
// the patch. It stages everything in the worktree (its own index, not
// yours) so new files count.
func Diff(wt Worktree) (Changes, string, error) {
	ctx, cancel := ctx30()
	defer cancel()
	if _, err := git(ctx, wt.Path, "add", "-A"); err != nil {
		return Changes{}, "", err
	}
	stat, err := git(ctx, wt.Path, "diff", "--cached", "--numstat", wt.BaseSHA)
	if err != nil {
		return Changes{}, "", err
	}
	var ch Changes
	for _, line := range strings.Split(stat, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		a, _ := strconv.Atoi(f[0]) // "-" for binary files counts as 0
		r, _ := strconv.Atoi(f[1])
		ch.Files = append(ch.Files, FileChange{Path: f[2], Added: a, Removed: r})
		ch.Added += a
		ch.Removed += r
	}
	patch, err := git(ctx, wt.Path, "diff", "--cached", wt.BaseSHA)
	return ch, patch, err
}

// Commit records everything in the worktree on its branch.
func Commit(wt Worktree, message string) error {
	ctx, cancel := ctx30()
	defer cancel()
	if _, err := git(ctx, wt.Path, "add", "-A"); err != nil {
		return err
	}
	if out, _ := git(ctx, wt.Path, "status", "--porcelain"); out == "" {
		return nil // nothing new: the agents may have committed already
	}
	_, err := git(ctx, wt.Path, "commit", "-m", message)
	return err
}

// Merge commits the task and merges its branch into the base branch of
// your checkout. It refuses when your checkout is on another branch or
// has changes of its own, so nothing of yours is overwritten.
func Merge(wt Worktree, message string) error {
	if err := Commit(wt, message); err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	cur, err := git(ctx, wt.Checkout, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if cur != wt.Base {
		return fmt.Errorf("your checkout is on %s, not %s: switch to %s and merge again", cur, wt.Base, wt.Base)
	}
	if dirty, _ := git(ctx, wt.Checkout, "status", "--porcelain", "--untracked-files=no"); dirty != "" {
		return errors.New("your checkout has changes not committed: commit or stash them first")
	}
	_, err = git(ctx, wt.Checkout, "merge", "--no-ff", "--no-edit", wt.Branch)
	return err
}

// Push commits the task and pushes its branch, so you can open a pull
// request. It opens one with gh when gh is installed.
func Push(wt Worktree, title string) (string, error) {
	if err := Commit(wt, title); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := git(ctx, wt.Path, "push", "-u", "origin", wt.Branch); err != nil {
		return "", err
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return "pushed " + wt.Branch + ": open the pull request on your git host", nil
	}
	cmd := exec.CommandContext(ctx, "gh", "pr", "create", "--fill", "--head", wt.Branch, "--base", wt.Base)
	cmd.Dir = wt.Path
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "pushed " + wt.Branch + " · gh: " + strings.TrimSpace(string(out)), nil
	}
	return strings.TrimSpace(string(out)), nil
}

// Discard removes the worktree and its branch.
func Discard(wt Worktree) error {
	ctx, cancel := ctx30()
	defer cancel()
	if _, err := git(ctx, wt.Checkout, "worktree", "remove", "--force", wt.Path); err != nil && !os.IsNotExist(err) {
		if _, statErr := os.Stat(wt.Path); statErr == nil {
			return err
		}
	}
	git(ctx, wt.Checkout, "worktree", "prune")
	_, err := git(ctx, wt.Checkout, "branch", "-D", wt.Branch)
	return err
}

// Set is the worktrees of one session, made the first time an agent needs
// each repository.
type Set struct {
	TeamDir, Task string
	Repos         map[string]Spec // connector -> repository

	mu    sync.Mutex
	trees map[string]Worktree
}

// Spec is a repository connector, as team.yaml says.
type Spec struct{ Path, Base, Setup string }

// NewSet prepares a session's worktrees (none are made yet).
func NewSet(teamDir, sessionID, title string, repos map[string]Spec) *Set {
	return &Set{TeamDir: teamDir, Task: sessionID + "-" + Slug(title), Repos: repos, trees: map[string]Worktree{}}
}

// Workspace returns the session's worktree of a repository, making it
// (and running its setup) the first time.
func (s *Set) Workspace(connector string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if wt, ok := s.trees[connector]; ok {
		return wt.Path, nil
	}
	spec, ok := s.Repos[connector]
	if !ok {
		return "", fmt.Errorf("no repository named %s", connector)
	}
	wt, err := Create(spec.Path, s.TeamDir, connector, s.Task, spec.Base)
	if err != nil {
		return "", err
	}
	if err := Setup(wt, spec.Setup); err != nil {
		wt.SetupErr = err.Error() // the agent can still work; you see why tests may fail
	}
	s.trees[connector] = wt
	return wt.Path, nil
}

// Worktrees are the ones made so far.
func (s *Set) Worktrees() []Worktree {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Worktree, 0, len(s.trees))
	for _, wt := range s.trees {
		out = append(out, wt)
	}
	return out
}
