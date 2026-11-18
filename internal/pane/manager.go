package pane

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"tubas/internal/repo"
)

var isWindows = runtime.GOOS == "windows"

var lookPath = exec.LookPath

// Host is where panes live: the background server, or this process with
// -local. The screen talks to either the same way.
type Host interface {
	Open(spec Spec, w, h int) (Info, error)
	List() ([]Info, error)
	Screen(id string, since int, wait time.Duration) (Screen, error)
	Send(id string, keys []Key) error
	Resize(id string, w, h int) error
	Close(id string) error
	Seen(id string) error
	History(id string) (lines, plain []string, err error)
	Layout() (string, error)
	SetLayout(layout string) error
}

// Manager keeps a team's panes.
type Manager struct {
	TeamDir string
	Env     []string // added to every pane's environment

	mu       sync.Mutex
	panes    map[string]*Pane
	order    []string
	specs    map[string]Spec // what each pane runs, to bring it back after a restart
	nextID   int
	layout   string
	stopping bool // the server is going down: panes ending now come back next time
}

var _ Host = (*Manager)(nil)

// NewManager makes an empty manager.
func NewManager(teamDir string) *Manager {
	return &Manager{TeamDir: teamDir, panes: map[string]*Pane{}, specs: map[string]Spec{}}
}

// Open starts a program in a new pane.
func (m *Manager) Open(spec Spec, w, h int) (Info, error) {
	m.mu.Lock()
	m.nextID++
	id := fmt.Sprintf("p%d", m.nextID)
	m.mu.Unlock()

	var wt *repo.Worktree
	if spec.Worktree {
		if spec.Dir == "" {
			return Info{}, errors.New("a worktree needs the repository's folder")
		}
		task := id + "-" + repo.Slug(orDefault(spec.Title, spec.Command, "shell"))
		made, err := repo.Create(spec.Dir, m.TeamDir, spec.Repo, task, spec.Base)
		if err != nil {
			return Info{}, err
		}
		if err := repo.Setup(made, spec.Setup); err != nil {
			made.SetupErr = err.Error()
		}
		wt, spec.Dir = &made, made.Path
	}
	spec.Worktree = false // made: next time it is just a folder
	info, err := m.launch(id, spec, spec.Args, w, h, wt)
	if err != nil && wt != nil {
		repo.Discard(*wt)
	}
	return info, err
}

// launch starts a pane and keeps it, with the spec it is saved under.
func (m *Manager) launch(id string, spec Spec, args []string, w, h int, wt *repo.Worktree) (Info, error) {
	run := spec
	run.Args = args
	p, err := start(id, run, w, h, m.Env)
	if err != nil {
		return Info{}, err
	}
	p.mu.Lock()
	p.info.Worktree = wt
	p.onExit = m.save
	p.mu.Unlock()
	m.mu.Lock()
	m.panes[id] = p
	m.order = append(m.order, id)
	m.specs[id] = spec
	m.mu.Unlock()
	m.save()
	return p.Info(), nil
}

// ── coming back after a restart ─────────────────────────────────────────
//
// The panes and their layout are written to .tubas/panes.json. When the
// server starts again, Restore starts each program again in its folder.
// A pane you closed, or whose program ended on its own, stays gone.

type savedPane struct {
	ID       string         `json:"id"`
	Spec     Spec           `json:"spec"`
	Title    string         `json:"title"`
	Worktree *repo.Worktree `json:"worktree,omitempty"`
	W        int            `json:"w"`
	H        int            `json:"h"`
}

type savedPanes struct {
	Panes  []savedPane `json:"panes"`
	Layout string      `json:"layout,omitempty"`
}

func (m *Manager) file() string {
	if m.TeamDir == "" {
		return ""
	}
	return filepath.Join(m.TeamDir, ".tubas", "panes.json")
}

// save writes the panes that should come back.
func (m *Manager) save() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping || m.file() == "" {
		return
	}
	var out savedPanes
	out.Layout = m.layout
	for _, id := range m.order {
		p := m.panes[id]
		info := p.Info()
		if info.Exit != "" {
			continue // ended on its own
		}
		out.Panes = append(out.Panes, savedPane{ID: id, Spec: m.specs[id], Title: info.Title, Worktree: info.Worktree, W: info.W, H: info.H})
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	os.MkdirAll(filepath.Dir(m.file()), 0o755)
	os.WriteFile(m.file(), data, 0o644)
}

// Restore starts the panes saved last time. It returns how many came
// back, and why the others didn't.
func (m *Manager) Restore() (int, []error) {
	data, err := os.ReadFile(m.file())
	if err != nil {
		return 0, nil
	}
	var saved savedPanes
	if json.Unmarshal(data, &saved) != nil {
		return 0, nil
	}
	m.mu.Lock()
	m.layout = saved.Layout
	for _, sp := range saved.Panes {
		if n, err := strconv.Atoi(strings.TrimPrefix(sp.ID, "p")); err == nil && n > m.nextID {
			m.nextID = n
		}
	}
	m.mu.Unlock()
	var errs []error
	n := 0
	for _, sp := range saved.Panes {
		if _, err := os.Stat(sp.Spec.Dir); sp.Spec.Dir != "" && err != nil {
			errs = append(errs, fmt.Errorf("%s: its folder is gone (%s)", sp.Title, sp.Spec.Dir))
			continue
		}
		if _, err := m.launch(sp.ID, sp.Spec, sp.Spec.Args, sp.W, sp.H, sp.Worktree); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sp.Title, err))
			continue
		}
		n++
	}
	m.save()
	return n, errs
}

func (m *Manager) get(id string) (*Pane, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.panes[id]
	if !ok {
		return nil, fmt.Errorf("no pane %s", id)
	}
	return p, nil
}

// List is every pane, oldest first.
func (m *Manager) List() ([]Info, error) {
	m.mu.Lock()
	ps := make([]*Pane, 0, len(m.order))
	for _, id := range m.order {
		ps = append(ps, m.panes[id])
	}
	m.mu.Unlock()
	out := make([]Info, len(ps))
	for i, p := range ps {
		out[i] = p.Info()
	}
	return out, nil
}

// Screen waits for a pane's screen to change (see Pane.Wait).
func (m *Manager) Screen(id string, since int, wait time.Duration) (Screen, error) {
	p, err := m.get(id)
	if err != nil {
		return Screen{}, err
	}
	return p.Wait(since, wait), nil
}

// Send types keys into a pane.
func (m *Manager) Send(id string, keys []Key) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	return p.Send(keys)
}

// Resize changes a pane's size.
func (m *Manager) Resize(id string, w, h int) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	p.Resize(w, h)
	return nil
}

// Text is a pane's screen as plain text.
func (m *Manager) Text(id string) (string, error) {
	p, err := m.get(id)
	if err != nil {
		return "", err
	}
	lines := strings.Split(p.Text(), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \r")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n"), nil
}

// Close stops a pane's program and forgets it. Its worktree stays, for
// you to review.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	p, ok := m.panes[id]
	if ok {
		delete(m.panes, id)
		for i, o := range m.order {
			if o == id {
				m.order = append(m.order[:i:i], m.order[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pane %s", id)
	}
	p.Kill()
	m.mu.Lock()
	delete(m.specs, id)
	m.mu.Unlock()
	m.save()
	return nil
}

// CloseAll stops every pane, as the server goes down: they are still
// saved, and come back with Restore.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	m.stopping = true
	ps := make([]*Pane, 0, len(m.order))
	for _, id := range m.order {
		ps = append(ps, m.panes[id])
	}
	m.panes, m.order = map[string]*Pane{}, nil
	m.mu.Unlock()
	for _, p := range ps {
		p.Kill()
	}
}

// Seen marks a pane as looked at.
func (m *Manager) Seen(id string) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	p.Seen()
	return nil
}

// History is a pane's scrollback and screen.
func (m *Manager) History(id string) ([]string, []string, error) {
	p, err := m.get(id)
	if err != nil {
		return nil, nil, err
	}
	lines, plain := p.History()
	return lines, plain, nil
}

// Layout is how the screen last arranged the panes (the screen's own
// format), so a window that attaches later finds them the same way.
func (m *Manager) Layout() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.layout, nil
}

// SetLayout remembers the arrangement.
func (m *Manager) SetLayout(layout string) error {
	m.mu.Lock()
	m.layout = layout
	m.mu.Unlock()
	m.save()
	return nil
}

// Counts is how many panes are in each state.
func Counts(infos []Info) map[string]int {
	out := map[string]int{}
	for _, i := range infos {
		out[i.State]++
	}
	return out
}

// SplitCommand splits a command line into words, keeping "quoted parts"
// together.
func SplitCommand(s string) []string {
	var out []string
	var cur strings.Builder
	quote, inWord := rune(0), false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, inWord = r, true
		case quote == 0 && (r == ' ' || r == '\t'):
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

func orDefault(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
