package ui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tubas/internal/engine"
	"tubas/internal/pane"
	"tubas/internal/repo"
)

// fakeHost keeps panes in memory and records what the screen asks.
type fakeHost struct {
	mu      sync.Mutex
	n       int
	specs   []pane.Spec
	sent    []pane.Key
	resized map[string][2]int
	closed  []string
	seen    []string
	layout  string
	infos   []pane.Info
}

func (f *fakeHost) Open(spec pane.Spec, w, h int) (pane.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.specs = append(f.specs, spec)
	info := pane.Info{ID: fmt.Sprintf("p%d", f.n), Title: orDefault(spec.Title, orDefault(spec.Command, "shell")), Dir: spec.Dir, Repo: spec.Repo, State: pane.Idle, W: w, H: h}
	f.infos = append(f.infos, info)
	return info, nil
}
func (f *fakeHost) List() ([]pane.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pane.Info(nil), f.infos...), nil
}
func (f *fakeHost) Screen(id string, since int, wait time.Duration) (pane.Screen, error) {
	return pane.Screen{Version: since + 1, Lines: []string{"$ ready"}, State: pane.Idle}, nil
}
func (f *fakeHost) Send(id string, keys []pane.Key) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, keys...)
	return nil
}
func (f *fakeHost) Resize(id string, w, h int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resized == nil {
		f.resized = map[string][2]int{}
	}
	f.resized[id] = [2]int{w, h}
	return nil
}
func (f *fakeHost) Close(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id)
	return nil
}
func (f *fakeHost) Seen(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, id)
	return nil
}
func (f *fakeHost) History(id string) ([]string, []string, error) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%d", i))
	}
	return lines, lines, nil
}
func (f *fakeHost) Layout() (string, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.layout, nil }
func (f *fakeHost) SetLayout(l string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.layout = l
	return nil
}

func (f *fakeHost) sentText() string {
	time.Sleep(50 * time.Millisecond) // the sender goroutine
	f.mu.Lock()
	defer f.mu.Unlock()
	var parts []string
	for _, k := range f.sent {
		parts = append(parts, k.Text+k.Name+k.Paste)
	}
	return strings.Join(parts, "|")
}

func paneModelSized(t *testing.T, w, h int, repos map[string]repo.Spec) (Model, *fakeHost) {
	t.Helper()
	local := teamModel(t, nil)
	host := &fakeHost{}
	cfg := local.cfg
	cfg.Panes = host
	cfg.Repos = repos
	m := New(local.team, engine.FakeEngine{}, cfg)
	return feed(m, tea.WindowSizeMsg{Width: w, Height: h}), host
}

func paneModel(t *testing.T) (Model, *fakeHost) { return paneModelSized(t, 120, 32, nil) }

// key presses one key and returns the model and the command it gave.
func key(m Model, k string) (Model, tea.Cmd) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	switch k {
	case "ctrl+b":
		msg = tea.KeyMsg{Type: tea.KeyCtrlB}
	case "ctrl+c":
		msg = tea.KeyMsg{Type: tea.KeyCtrlC}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	}
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func keys(m Model, ks ...string) Model {
	for _, k := range ks {
		var cmd tea.Cmd
		m, cmd = key(m, k)
		for _, msg := range run(cmd) {
			if _, ok := msg.(copyLoadedMsg); ok {
				m = feed(m, msg)
			}
		}
	}
	return m
}

// run runs a command and every command it batches, once, and returns
// the messages they produced.
func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, run(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func openPane(t *testing.T, m Model, splitKey string) Model {
	t.Helper()
	m = keys(m, "ctrl+b", splitKey)
	if m.form == nil || m.form.kind != "pane" {
		t.Fatalf("ctrl+b %s should open the new pane menu", splitKey)
	}
	m, cmd := key(m, "enter")
	for _, msg := range run(cmd) {
		if opened, ok := msg.(paneOpenedMsg); ok {
			next, cmd := m.Update(opened)
			run(cmd) // the app runs them: sizes, the saved layout
			return next.(Model)
		}
	}
	t.Fatal("enter should open the pane")
	return m
}

func leaves(m Model) string { return strings.Join(m.panes.cur().Root.leaves(), ",") }

func TestPaneSplitsFocusesAndTakesKeys(t *testing.T) {
	m, host := paneModel(t)
	m = openPane(t, m, "v")
	if !m.split() || m.panes.focus() != "p1" || leaves(m) != "chat,p1" {
		t.Fatalf("the new pane should sit right of the chat, focused: %s", leaves(m))
	}
	if host.specs[0].Command != "" || host.specs[0].Dir != m.cfg.TeamDir {
		t.Errorf("a shell in the team folder: %+v", host.specs[0])
	}
	m = feed(m, paneScreenMsg{id: "p1", scr: pane.Screen{Version: 1, Lines: []string{"PS E:\\work> hello-pane"}, State: pane.Idle}})
	frame := ansi.Strip(m.Snapshot(120, 32))
	contains(t, frame, "hello-pane", "shell", "keys go to", "│", "1 chat")

	// Keys go to the terminal, ctrl+c included; tubas keeps running.
	m, _ = key(m, "l")
	m, _ = key(m, "s")
	m, cmd := key(m, "ctrl+c")
	for _, msg := range run(cmd) {
		if _, quit := msg.(tea.QuitMsg); quit {
			t.Fatal("ctrl+c in a pane goes to the program, not to tubas")
		}
	}
	if got := host.sentText(); got != "l|s|ctrl+c" {
		t.Errorf("the pane got %q", got)
	}

	// ctrl+b h moves to the chat, where letters type a question.
	m = keys(m, "ctrl+b", "h")
	if m.panes.focus() != chatPane {
		t.Fatalf("ctrl+b h should focus the chat, focus is %s", m.panes.focus())
	}
	m, _ = key(m, "x")
	if string(m.input) != "x" {
		t.Errorf("the chat should get the letter, input = %q", string(m.input))
	}
	m.input = nil

	// A second pane below the first; then zoom and close.
	m = keys(m, "ctrl+b", "l")
	m = openPane(t, m, "-")
	if leaves(m) != "chat,p1,p2" || m.panes.focus() != "p2" {
		t.Fatalf("leaves = %s, focus %s", leaves(m), m.panes.focus())
	}
	m = keys(m, "ctrl+b", "k")
	if m.panes.focus() != "p1" {
		t.Errorf("ctrl+b k should go up to p1, focus %s", m.panes.focus())
	}
	m = keys(m, "ctrl+b", "z")
	if !m.panes.cur().Zoom || !strings.Contains(ansi.Strip(m.Snapshot(120, 32)), "zoom") {
		t.Error("ctrl+b z zooms the focused pane")
	}
	m = keys(m, "ctrl+b", "x")
	if m.panes.closing != "p1" {
		t.Fatal("closing a running program asks first")
	}
	m, cmd = key(m, "y")
	run(cmd)
	if m.panes.cur().Root.has("p1") || len(host.closed) != 1 || host.closed[0] != "p1" {
		t.Errorf("y should close p1: leaves %s, closed %v", leaves(m), host.closed)
	}
	if !strings.Contains(host.layout, `"p2"`) || strings.Contains(host.layout, `"p1"`) {
		t.Errorf("the server keeps the layout for the next window: %s", host.layout)
	}
}

func TestPanesGetTheirSize(t *testing.T) {
	m, host := paneModelSized(t, 100, 30, nil)
	m = openPane(t, m, "v")
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	run(cmd)
	got := host.resized["p1"]
	// 100 columns minus the margins = 98; the chat takes 48, the border 1.
	if got[0] != 49 || got[1] < 10 {
		t.Errorf("p1 should be resized to its half of the screen, got %v", got)
	}
}

func TestPanesComeBackFromTheServer(t *testing.T) {
	m, host := paneModel(t)
	host.infos = []pane.Info{{ID: "p1", Title: "claude", State: pane.Blocked}, {ID: "p2", Title: "shell"}}
	stale := (&layoutNode{Split: "right", A: &layoutNode{Pane: chatPane},
		B: &layoutNode{Split: "down", A: &layoutNode{Pane: "p1"}, B: &layoutNode{Pane: "p9"}}}).encode()
	if kept, _ := m.panesLoaded(panesLoadedMsg{layout: stale, err: fmt.Errorf("server busy")}); len(kept.panes.spaces) != 0 {
		t.Error("a failed list must not rebuild the layout")
	}
	m, _ = m.panesLoaded(panesLoadedMsg{infos: host.infos, layout: stale})
	if leaves(m) != "chat,p1" {
		t.Errorf("p9 is gone: leaves %s", leaves(m))
	}
	if ws := m.panes.space(); len(ws.Tabs) != 2 || ws.Tabs[1].Root.Pane != "p2" {
		t.Errorf("p2, opened elsewhere, gets its own tab: %+v", ws.Tabs)
	}
	// The answer to an open that the list already brought adds nothing.
	m, _ = m.paneOpened(paneOpenedMsg{info: host.infos[1], target: chatPane, dir: "right"})
	if len(m.panes.space().Tabs) != 2 {
		t.Error("no duplicate tab")
	}
	m.panes.space().Tab = 0
	frame := ansi.Strip(m.Snapshot(140, 32))
	contains(t, frame, "claude", "blocked", "panes 1 blocked")
}

func TestTabsAndWorkspaces(t *testing.T) {
	m, host := paneModelSized(t, 140, 32, map[string]repo.Spec{"api": {Path: t.TempDir()}})
	m, _ = m.panesLoaded(panesLoadedMsg{})
	if len(m.panes.spaces) != 2 || m.panes.spaces[1].Repo != "api" {
		t.Fatalf("the team and each repository are workspaces: %+v", m.panes.spaces)
	}
	m = openPane(t, m, "c")
	if ws := m.panes.space(); len(ws.Tabs) != 2 || ws.Tab != 1 || leaves(m) != "p1" {
		t.Fatalf("ctrl+b c opens a new tab: %+v", ws.Tabs)
	}
	frame := ansi.Strip(m.Snapshot(140, 32))
	contains(t, frame, "2 shell", "api", "ctrl+b b hide") // the sidebar
	m = keys(m, "ctrl+b", "1")
	if m.panes.space().Tab != 0 || m.panes.focus() != chatPane {
		t.Error("ctrl+b 1 shows the first tab")
	}
	m = keys(m, "ctrl+b", "n")
	if m.panes.space().Tab != 1 {
		t.Error("ctrl+b n goes to the next tab")
	}
	m = keys(m, "ctrl+b", "w")
	if m.panes.space().Name != "api" || m.panes.cur() != nil {
		t.Fatal("ctrl+b w shows the next workspace")
	}
	contains(t, ansi.Strip(m.Snapshot(140, 32)), "nothing open here yet")
	m, _ = key(m, "z")
	if host.sentText() != "" {
		t.Error("an empty workspace sends keys nowhere")
	}
	m = keys(m, "ctrl+b", "c")
	if got := m.form.get("where"); got != "repo:api" {
		t.Errorf("a shell in the api workspace starts in its checkout, where = %q", got)
	}
	m.form.set("run", "claude")
	m = m.paneFormRunChanged()
	if got := m.form.get("where"); got != "wt:api" {
		t.Errorf("an agent in the api workspace gets a new worktree, where = %q", got)
	}
	m.form = nil
	m = keys(m, "ctrl+b", "b")
	if m.sidebarShown() {
		t.Error("ctrl+b b hides the sidebar")
	}
	if !strings.Contains(host.layout, `"v":2`) || !strings.Contains(host.layout, `"api"`) {
		t.Errorf("workspaces and tabs are saved: %s", host.layout)
	}
}

func TestCopyMode(t *testing.T) {
	m, _ := paneModel(t)
	m = openPane(t, m, "v")
	m, _ = key(m, "ctrl+b")
	m, cmd := key(m, "[")
	for _, msg := range run(cmd) {
		if l, ok := msg.(copyLoadedMsg); ok {
			m = feed(m, l)
		}
	}
	c := m.panes.copy
	if c == nil || c.cy != 29 {
		t.Fatalf("ctrl+b [ opens the history at its end: %+v", c)
	}
	m = keys(m, "k", "k", "v", "$", "j")
	if got := c.text(); got != "line-28\nline-29" {
		t.Errorf("v then moves selects: %q", got)
	}
	m = keys(m, "esc", "/", "l", "i", "n", "e", "-", "5", "enter")
	if c.cy != 4 {
		t.Errorf("/line-5 finds it back in time, at line %d", c.cy)
	}
	contains(t, ansi.Strip(m.Snapshot(120, 32)), "copy", "line-5")
	m = keys(m, "q")
	if m.panes.copy != nil {
		t.Error("q goes back to the live terminal")
	}
}

func TestMouseFocusesResizesAndScrolls(t *testing.T) {
	m, host := paneModelSized(t, 100, 30, nil)
	m = openPane(t, m, "v")
	click := func(x, y int, b tea.MouseButton, a tea.MouseAction) tea.Cmd {
		next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Button: b, Action: a})
		m = next.(Model)
		return cmd
	}
	click(bodyX+10, 10, tea.MouseButtonLeft, tea.MouseActionPress)
	click(bodyX+10, 10, tea.MouseButtonLeft, tea.MouseActionRelease)
	if m.panes.focus() != chatPane {
		t.Error("a click on the chat focuses it")
	}
	click(bodyX+60, 10, tea.MouseButtonLeft, tea.MouseActionPress)
	click(bodyX+60, 10, tea.MouseButtonLeft, tea.MouseActionRelease)
	if m.panes.focus() != "p1" {
		t.Error("a click on a terminal focuses it")
	}
	// The border between them is at column 48 of the body.
	click(bodyX+48, 10, tea.MouseButtonLeft, tea.MouseActionPress)
	click(bodyX+30, 10, tea.MouseButtonNone, tea.MouseActionMotion)
	run(click(bodyX+30, 10, tea.MouseButtonLeft, tea.MouseActionRelease))
	if r := m.panes.cur().Root.Ratio; r < 0.28 || r > 0.33 {
		t.Errorf("dragging the border resizes: ratio %.2f", r)
	}
	if !strings.Contains(host.layout, `"ratio"`) {
		t.Error("the new size is saved")
	}
	for _, msg := range run(click(bodyX+70, 10, tea.MouseButtonWheelUp, tea.MouseActionPress)) {
		if l, ok := msg.(copyLoadedMsg); ok {
			m = feed(m, l)
		}
	}
	if m.panes.copy == nil || m.panes.copy.top >= 30-m.copyRows() {
		t.Errorf("the wheel scrolls back through the history: %+v", m.panes.copy)
	}
}

func TestPaneAlertsInTheWindow(t *testing.T) {
	m, _ := paneModel(t)
	m = openPane(t, m, "v")
	m = keys(m, "ctrl+b", "h") // looking at the chat
	m, _ = m.reconcile([]pane.Info{{ID: "p1", Title: "claude", State: pane.Blocked}})
	if !strings.Contains(ansi.Strip(m.notice), "claude is waiting for you") {
		t.Errorf("a pane that starts waiting is announced: %q", m.notice)
	}
}

func TestLayoutNeighbors(t *testing.T) {
	root := splitPane(nil, chatPane, "right", "p1")
	root = splitPane(root, "p1", "down", "p2")
	rects := map[string]rect{}
	root.place(rect{0, 0, 99, 31}, rects)
	if neighbor(rects, chatPane, "right") != "p1" || neighbor(rects, "p2", "up") != "p1" || neighbor(rects, "p2", "left") != chatPane {
		t.Errorf("neighbors are wrong: %+v", rects)
	}
	if neighbor(rects, chatPane, "left") != "" {
		t.Error("nothing left of the chat")
	}
	if back := decodeLayout(root.encode()); strings.Join(back.leaves(), ",") != "chat,p1,p2" {
		t.Error("a layout survives the trip to the server")
	}
	if l := withoutPane(root, "p1").leaves(); strings.Join(l, ",") != "chat,p2" {
		t.Errorf("removing p1 leaves %v", l)
	}
}
