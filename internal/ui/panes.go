package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tubas/internal/pane"
	"tubas/internal/repo"
	"tubas/internal/server"
)

// ── panes: real terminals next to the chat ──────────────────────────────
//
// ctrl+b v (or -) opens a new pane to the right (or below) the focused
// one, ctrl+b c a new tab: your shell, claude, codex, any command, in the
// team folder, a repository or a new worktree of it. The program runs in
// the background server, in a real terminal; this screen draws it and
// sends it your keys. Keys go to the focused pane; ctrl+b is the only key
// tubas keeps. See workspace.go for tabs and workspaces, copymode.go for
// scrollback and mouse.go for the mouse.

// paneState is shared by every copy of the model (like a session's
// goroutine): Update is the only place that changes it.
type paneState struct {
	host    pane.Host
	spaces  []*workspace
	ws      int
	sidebar *bool // nil: shown by itself when there is room

	infos    map[string]pane.Info
	screens  map[string]pane.Screen
	watching map[string]bool
	sizes    map[string][2]int // the size last sent to each pane
	gone     map[string]bool   // closed here: ignore it if a late list still has it
	seen     map[string]bool   // panes told they were looked at, until their state changes
	closing  string            // the pane waiting for y to close
	keys     chan keyJob

	copy *copyState // scrolling back through a pane (copymode.go)
	sel  *selection // text being selected with the mouse
	drag *separator // a border being dragged

	lastClick struct { // for a double click on a pane's header
		id string
		at time.Time
	}
}

type keyJob struct {
	id   string
	keys []pane.Key
}

func newPaneState(host pane.Host) *paneState {
	ps := &paneState{host: host, infos: map[string]pane.Info{}, screens: map[string]pane.Screen{},
		watching: map[string]bool{}, sizes: map[string][2]int{}, gone: map[string]bool{}, seen: map[string]bool{},
		keys: make(chan keyJob, 512)}
	// One sender, so keys arrive in the order you typed them.
	go func() {
		for job := range ps.keys {
			host.Send(job.id, job.keys)
		}
	}()
	return ps
}

type (
	panesLoadedMsg struct {
		infos  []pane.Info
		layout string
		err    error
	}
	paneListMsg struct {
		infos []pane.Info
		err   error
	}
	paneListTick  struct{}
	paneScreenMsg struct {
		id  string
		scr pane.Screen
		err error
	}
	paneOpenedMsg struct {
		info   pane.Info
		target string // the pane it was split from
		dir    string // right, down or tab
		err    error
	}
)

const paneListEvery = 1500 * time.Millisecond

func loadPanes(host pane.Host) tea.Cmd {
	return func() tea.Msg {
		infos, err := host.List()
		layout, _ := host.Layout()
		return panesLoadedMsg{infos, layout, err}
	}
}

func listPanes(host pane.Host) tea.Cmd {
	return tea.Tick(paneListEvery, func(time.Time) tea.Msg { return paneListTick{} })
}

func watchPane(host pane.Host, id string, since int) tea.Cmd {
	return func() tea.Msg {
		scr, err := host.Screen(id, since, 20*time.Second)
		return paneScreenMsg{id, scr, err}
	}
}

func (ps *paneState) saveLayout() tea.Cmd {
	host, layout := ps.host, ps.encode()
	return func() tea.Msg {
		host.SetLayout(layout)
		return nil
	}
}

// split says whether the chat area shows panes, tabs or the sidebar
// instead of the chat alone.
func (m Model) split() bool {
	ps := m.panes
	if ps == nil || len(ps.spaces) == 0 {
		return false
	}
	t := ps.cur()
	return m.sidebarShown() || len(ps.space().Tabs) > 1 || t == nil || len(t.Root.leaves()) > 1 || !t.Root.has(chatPane)
}

// paneFocused says whether keys go to a terminal (or nowhere, in an
// empty workspace) instead of the chat.
func (m Model) paneFocused() bool {
	return m.split() && m.section == SecChat && m.panes.focus() != chatPane && !m.picker && !m.review
}

// ── what the server says ────────────────────────────────────────────────

func (m Model) panesLoaded(msg panesLoadedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m, listPanes(m.panes.host) // keep trying; the layout stays as saved
	}
	m.loadLayout(msg.layout)
	m, cmd := m.reconcile(msg.infos)
	return m, tea.Batch(cmd, listPanes(m.panes.host))
}

func (m Model) paneListTickMsg() (Model, tea.Cmd) {
	host := m.panes.host
	return m, func() tea.Msg {
		infos, err := host.List()
		return paneListMsg{infos, err}
	}
}

func (m Model) paneList(msg paneListMsg) (Model, tea.Cmd) {
	next := listPanes(m.panes.host)
	if msg.err != nil || len(m.panes.spaces) == 0 {
		return m, next
	}
	m, cmd := m.reconcile(msg.infos)
	return m, tea.Batch(cmd, next)
}

// reconcile makes the layout match the panes that exist: new ones (from
// another window, `tubas pane new` or a restart) get a tab, closed ones
// go away. State changes worth telling you about become notices.
func (m Model) reconcile(infos []pane.Info) (Model, tea.Cmd) {
	ps := m.panes
	exists := map[string]bool{chatPane: true}
	var cmds []tea.Cmd
	changed := false
	var alerts []string
	for _, i := range infos {
		if ps.gone[i.ID] {
			continue
		}
		exists[i.ID] = true
		if old, ok := ps.infos[i.ID]; ok && old.State != i.State {
			delete(ps.seen, i.ID)
			if a := m.paneAlert(old, i); a != "" {
				alerts = append(alerts, a)
			}
		}
		ps.infos[i.ID] = i
		if w, _ := ps.where(i.ID); w == nil {
			home := ps.home(i)
			home.Tabs = append(home.Tabs, &tab{Name: tabName(i.Title), Root: &layoutNode{Pane: i.ID}, Focus: i.ID})
			changed = true
		}
	}
	for _, w := range ps.spaces {
		for _, t := range w.Tabs {
			for _, id := range t.Root.leaves() {
				if !exists[id] {
					ps.removePane(id)
					delete(ps.infos, id)
					changed = true
				}
			}
		}
	}
	if changed {
		cmds = append(cmds, ps.saveLayout())
	}
	if len(alerts) > 0 {
		m.notice = strings.Join(alerts, dimStyle.Render(" · "))
		if m.cfg.Notify != "off" {
			cmds = append(cmds, bell())
		}
	}
	return m, tea.Batch(cmds...)
}

// paneAlert is the notice for a pane that started waiting or finished,
// unless you are looking at it.
func (m Model) paneAlert(old, now pane.Info) string {
	if m.section == SecChat && m.panes.focus() == now.ID {
		return ""
	}
	switch {
	case now.State == pane.Blocked:
		return warnStyle.Render("◐ "+now.Title) + dimStyle.Render(" is waiting for you")
	case now.State == pane.Done && old.State == pane.Working && now.Exit == "":
		return okStyle.Render("● "+now.Title) + dimStyle.Render(" finished")
	}
	return ""
}

func tabName(title string) string {
	if r := []rune(title); len(r) > 14 {
		return string(r[:13]) + "…"
	}
	return title
}

// bell rings the terminal's bell.
func bell() tea.Cmd {
	return func() tea.Msg {
		os.Stdout.WriteString("\a")
		return nil
	}
}

func (m Model) paneScreen(msg paneScreenMsg) (Model, tea.Cmd) {
	ps := m.panes
	if !slices.Contains(m.visiblePanes(), msg.id) {
		delete(ps.watching, msg.id) // not on screen any more: the list keeps its state
		return m, nil
	}
	if msg.err != nil {
		if strings.Contains(msg.err.Error(), "no pane") { // closed elsewhere
			delete(ps.watching, msg.id)
			ps.removePane(msg.id)
			return m, ps.saveLayout()
		}
		// The server may be restarting: try again in a moment.
		host, id := ps.host, msg.id
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg {
			scr, err := host.Screen(id, -1, 20*time.Second)
			return paneScreenMsg{id, scr, err}
		})
	}
	ps.screens[msg.id] = msg.scr
	return m, watchPane(ps.host, msg.id, msg.scr.Version)
}

func (m Model) paneOpened(msg paneOpenedMsg) (Model, tea.Cmd) {
	ps := m.panes
	if msg.err != nil {
		m.notice = badStyle.Render("pane: " + msg.err.Error())
		return m, nil
	}
	ps.infos[msg.info.ID] = msg.info
	m.notice = ""
	if w, t := ps.where(msg.info.ID); w != nil { // the list brought it first
		m = m.showPane(w, t, msg.info.ID)
		return m, ps.saveLayout()
	}
	ws := ps.space()
	t := ps.cur()
	if msg.dir == "tab" || t == nil {
		ws.Tabs = append(ws.Tabs, &tab{Name: tabName(msg.info.Title), Root: &layoutNode{Pane: msg.info.ID}, Focus: msg.info.ID})
		ws.Tab = len(ws.Tabs) - 1
	} else {
		target := msg.target
		if !t.Root.has(target) {
			target = t.Focus
		}
		t.Root = splitPane(t.Root, target, msg.dir, msg.info.ID)
		t.Focus, t.Zoom = msg.info.ID, false
	}
	return m, ps.saveLayout()
}

// showPane brings a pane's workspace and tab on screen and focuses it.
func (m Model) showPane(w *workspace, t *tab, id string) Model {
	ps := m.panes
	for wi, ws := range ps.spaces {
		if ws == w {
			ps.ws = wi
			for ti, tt := range ws.Tabs {
				if tt == t {
					ws.Tab = ti
				}
			}
		}
	}
	t.Focus = id
	return m
}

// ── geometry ────────────────────────────────────────────────────────────

func (m Model) bodyHeight() int {
	return max(0, m.height-4-len(m.viewBottom(m.width-2)))
}

// areaIn is where the panes go inside the body (cw × h): right of the
// sidebar and under the tab bar.
func (m Model) areaIn(cw, h int) rect {
	r := rect{0, 0, cw, h}
	if m.sidebarShown() {
		r.x, r.w = sidebarW+1, cw-sidebarW-1
	}
	if m.tabBarShown() {
		r.y, r.h = 1, h-1
	}
	return r
}

// layoutRects places the visible panes, from the top left of the area.
func (m Model) layoutRects() map[string]rect {
	out := map[string]rect{}
	t := m.panes.cur()
	if t == nil {
		return out
	}
	a := m.areaIn(m.width-2, m.bodyHeight())
	if t.Zoom {
		out[t.Focus] = rect{0, 0, a.w, a.h}
		return out
	}
	t.Root.place(rect{0, 0, a.w, a.h}, out)
	return out
}

// visiblePanes are the panes on screen now.
func (m Model) visiblePanes() []string {
	if !m.split() || m.section != SecChat || m.panes.cur() == nil {
		return nil
	}
	t := m.panes.cur()
	if t.Zoom {
		return []string{t.Focus}
	}
	return t.Root.leaves()
}

// panesAfter runs after every message: each visible terminal gets its
// size and a watcher, and a finished agent you look at is marked seen.
func (m Model) panesAfter() tea.Cmd {
	ps := m.panes
	if ps == nil || m.width == 0 || len(ps.spaces) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for id, r := range m.layoutRects() {
		if id == chatPane {
			continue
		}
		if !ps.watching[id] {
			ps.watching[id] = true
			cmds = append(cmds, watchPane(ps.host, id, -1))
		}
		size := [2]int{r.w, max(1, r.h-1)} // one row is the pane's header
		if ps.sizes[id] == size || m.section != SecChat {
			continue
		}
		ps.sizes[id] = size
		host, id := ps.host, id
		cmds = append(cmds, func() tea.Msg {
			host.Resize(id, size[0], size[1])
			return nil
		})
	}
	if f := ps.focus(); f != chatPane && f != "" && m.section == SecChat && !ps.seen[f] {
		if i := ps.infos[f]; i.State == pane.Done && i.Exit == "" {
			ps.seen[f] = true
			host := ps.host
			cmds = append(cmds, func() tea.Msg { host.Seen(f); return nil })
		}
	}
	return tea.Batch(cmds...)
}

// ── keys ────────────────────────────────────────────────────────────────

// paneInput handles a key while a terminal (or an empty workspace) has
// the focus.
func (m Model) paneInput(msg tea.KeyMsg) (Model, tea.Cmd) {
	ps := m.panes
	f := ps.focus()
	if f == "" {
		m.notice = dimStyle.Render("nothing open here · ctrl+b c opens a tab")
		return m, nil
	}
	if ps.copy != nil && ps.copy.id == f {
		return m.copyKey(msg)
	}
	return m.sendToPane(msg)
}

// sendToPane types a key into the focused terminal.
func (m Model) sendToPane(msg tea.KeyMsg) (Model, tea.Cmd) {
	var k pane.Key
	switch {
	case msg.Paste:
		k.Paste = string(msg.Runes)
	case msg.Type == tea.KeyRunes && !msg.Alt:
		k.Text = string(msg.Runes)
	case msg.Type == tea.KeySpace && !msg.Alt:
		k.Text = " "
	default:
		k.Name = msg.String()
	}
	select {
	case m.panes.keys <- keyJob{m.panes.focus(), []pane.Key{k}}:
	default: // the sender is far behind: drop rather than freeze the screen
	}
	return m, nil
}

// paneKey handles the key after ctrl+b for panes, tabs and workspaces.
// It returns false when the key is not one of those.
func (m Model) paneKey(key string) (Model, tea.Cmd, bool) {
	ps := m.panes
	if ps == nil {
		switch key {
		case "v", "%", "-", "\"", "a", "c", "N", "w", "b", "[":
			m.notice = dimStyle.Render("panes need a team folder")
			return m, nil, true
		}
		return m, nil, false
	}
	if len(ps.spaces) == 0 {
		m.loadLayout("")
	}
	ws, t := ps.space(), ps.cur()
	switch key {
	case "v", "%", "-", "\"", "a", "c":
		dir := "right"
		switch {
		case key == "-" || key == "\"":
			dir = "down"
		case key == "c" || t == nil:
			dir = "tab"
		}
		m.section = SecChat
		return m.newPaneForm(dir, key == "a"), nil, true
	case "N":
		m.section = SecChat
		return m.newWorkspaceForm(), nil, true
	case "w":
		ps.ws = (ps.ws + 1) % len(ps.spaces)
		m.section = SecChat
		return m, ps.saveLayout(), true
	case "b":
		shown := !m.sidebarShown()
		ps.sidebar = &shown
		return m, ps.saveLayout(), true
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if i := int(key[0] - '1'); i < len(ws.Tabs) {
			ws.Tab = i
			m.section = SecChat
			return m, ps.saveLayout(), true
		}
		return m, nil, len(ws.Tabs) > 0
	case "n", "p":
		if len(ws.Tabs) < 2 {
			return m, nil, false // ctrl+b n: a new session, as before
		}
		step := 1
		if key == "p" {
			step = len(ws.Tabs) - 1
		}
		ws.Tab = (ws.Tab + step) % len(ws.Tabs)
		return m, ps.saveLayout(), true
	}
	if t == nil || !m.split() {
		return m, nil, false
	}
	switch key {
	case "h", "left", "j", "down", "k", "up", "l", "right":
		dir := map[string]string{"h": "left", "j": "down", "k": "up", "l": "right"}[key]
		if dir == "" {
			dir = key
		}
		t.Zoom = false
		if next := neighbor(m.layoutRects(), t.Focus, dir); next != "" {
			t.Focus = next
		}
		return m, nil, true
	case "o": // the next pane, in order
		leaves := t.Root.leaves()
		i := slices.Index(leaves, t.Focus)
		t.Focus, t.Zoom = leaves[(i+1)%len(leaves)], false
		return m, nil, true
	case "z":
		t.Zoom = !t.Zoom
		return m, ps.saveLayout(), true
	case "[":
		if t.Focus == chatPane {
			return m, nil, true
		}
		mm, cmd := m.enterCopy(t.Focus, 0)
		return mm, cmd, true
	case "x":
		if t.Focus == chatPane {
			m.notice = dimStyle.Render("the chat stays · ctrl+b x closes a terminal pane")
			return m, nil, true
		}
		if ps.infos[t.Focus].Exit != "" {
			return m.closePane(t.Focus), m.closePaneCmd(t.Focus), true
		}
		ps.closing = t.Focus
		m.notice = warnStyle.Render("stop "+ps.infos[t.Focus].Title+"?") + dimStyle.Render(" y closes it · any other key keeps it")
		return m, nil, true
	case "r":
		if wt := ps.infos[t.Focus].Worktree; wt != nil {
			ch, patch, err := repo.Diff(*wt)
			c := wtChanges{wt: *wt, changes: ch, patch: patch}
			if err != nil {
				c.err = err.Error()
			}
			m.changes, m.review, m.reviewWT, m.fileIdx = []wtChanges{c}, true, 0, 0
			return m, nil, true
		}
		m.notice = dimStyle.Render("this pane has no worktree to review")
		return m, nil, true
	}
	return m, nil, false
}

func (m Model) closePane(id string) Model {
	ps := m.panes
	ps.gone[id] = true
	ps.closing = ""
	if ps.copy != nil && ps.copy.id == id {
		ps.copy = nil
	}
	ps.removePane(id)
	delete(ps.watching, id)
	delete(ps.screens, id)
	delete(ps.infos, id)
	m.notice = ""
	return m
}

func (m Model) closePaneCmd(id string) tea.Cmd {
	host := m.panes.host
	return tea.Batch(func() tea.Msg { host.Close(id); return nil }, m.panes.saveLayout())
}

// ── the new pane menu ───────────────────────────────────────────────────

// newPaneForm asks what to run and where. dir is right, down or tab.
func (m Model) newPaneForm(dir string, agentFirst bool) Model {
	runs := []string{"shell"}
	runs = append(runs, pane.Agents(server.Resolve)...)
	runs = append(runs, "command")
	where, show := m.paneWhere()
	side := map[string]string{"right": "to the right", "down": "below", "tab": "in a new tab"}[dir]
	f := &form{kind: "pane", title: "new pane " + side + " · " + m.panes.space().Name, extra: dir}
	f.fields = []field{
		{key: "run", label: "run", kind: fChoice, options: runs, value: "shell", hint: "←→ choose · your shell, an agent CLI or any command"},
		{key: "command", label: "command", kind: fText, hint: "empty runs your shell · flags welcome: claude --model sonnet"},
		{key: "where", label: "where", kind: fChoice, options: where, show: show,
			hint: "a new worktree gets its own branch; your checkout stays untouched"},
		{key: "title", label: "title", kind: fText, hint: "optional · shown on the pane and its tab"},
	}
	m.form = f
	if agentFirst && len(runs) > 2 {
		f.set("run", runs[1])
	}
	return m.paneFormRunChanged()
}

// paneWhere lists the places a pane can start.
func (m Model) paneWhere() ([]string, []string) {
	var where, show []string
	if ws := m.panes.space(); ws.Own {
		where, show = append(where, "ws"), append(show, ws.Name)
	}
	if m.cfg.WorkDir != "" && !samePath(m.cfg.WorkDir, m.cfg.TeamDir) {
		where, show = append(where, "here"), append(show, "here · "+filepath.Base(m.cfg.WorkDir))
	}
	where, show = append(where, "team"), append(show, "team folder")
	var names []string
	for name := range m.cfg.Repos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, n := range names {
		where = append(where, "repo:"+n, "wt:"+n)
		show = append(show, n, n+" · new worktree")
	}
	return where, show
}

// paneFormRunChanged fills the command and the natural place for what
// you picked: in a repository's workspace, an agent gets a new worktree.
func (m Model) paneFormRunChanged() Model {
	f := *m.form
	f.fields = slices.Clone(f.fields)
	run := f.get("run")
	switch run {
	case "shell":
		f.set("command", "")
	case "command":
	default:
		f.set("command", run)
	}
	where, _ := m.paneWhere()
	choice := where[0]
	if r := m.panes.space().Repo; r != "" {
		choice = "repo:" + r
		if run != "shell" {
			choice = "wt:" + r
		}
	}
	f.set("where", choice)
	m.form = &f
	return m
}

func (m Model) savePane() (Model, tea.Cmd) {
	f := m.form
	dir, _ := f.extra.(string)
	var spec pane.Spec
	if words := pane.SplitCommand(f.get("command")); len(words) > 0 {
		spec.Command, spec.Args = words[0], words[1:]
	} else if run := f.get("run"); run != "shell" && run != "command" {
		spec.Command = run
	}
	spec.Title = f.get("title")
	where := f.get("where")
	spec.Dir = m.cfg.TeamDir
	switch {
	case where == "ws":
		spec.Dir = m.panes.space().Dir
	case where == "here":
		spec.Dir = m.cfg.WorkDir
	case strings.HasPrefix(where, "repo:"):
		name := strings.TrimPrefix(where, "repo:")
		spec.Repo, spec.Dir = name, m.cfg.Repos[name].Path
	case strings.HasPrefix(where, "wt:"):
		name := strings.TrimPrefix(where, "wt:")
		r := m.cfg.Repos[name]
		spec.Repo, spec.Dir, spec.Base, spec.Setup, spec.Worktree = name, r.Path, r.Base, r.Setup, true
	}
	ps := m.panes
	target := ps.focus()
	a := m.areaIn(m.width-2, m.bodyHeight())
	r, ok := m.layoutRects()[target]
	if !ok || dir == "tab" {
		r = rect{0, 0, a.w, a.h}
	}
	w, h := r.w, r.h-1
	switch dir {
	case "right":
		_, w = splitSizes(r.w)
	case "down":
		_, h = splitSizes(r.h)
		h--
	}
	m.form = nil
	m.notice = dimStyle.Render("opening " + orDefault(spec.Command, "your shell") + "…")
	host := ps.host
	return m, func() tea.Msg {
		info, err := host.Open(spec, max(10, w), max(3, h))
		return paneOpenedMsg{info, target, dir, err}
	}
}

func (m Model) newWorkspaceForm() Model {
	m.form = &form{kind: "workspace", title: "new workspace", fields: []field{
		{key: "name", label: "name", kind: fText, hint: "shown in the sidebar"},
		{key: "folder", label: "folder", kind: fText, value: orDefault(m.cfg.WorkDir, m.cfg.TeamDir), hint: "where its panes start"},
	}}
	return m
}

func (m Model) saveWorkspace() (Model, tea.Cmd) {
	f := *m.form
	name, dir := f.get("name"), f.get("folder")
	if name == "" {
		name = filepath.Base(dir)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		f.err = dir + " is not a folder"
		m.form = &f
		return m, nil
	}
	ps := m.panes
	ps.spaces = append(ps.spaces, &workspace{Name: name, Dir: dir, Own: true})
	ps.ws = len(ps.spaces) - 1
	m.form = nil
	m.notice = okStyle.Render("✓ workspace "+name) + dimStyle.Render(" · ctrl+b c opens a tab in it")
	return m, ps.saveLayout()
}

func samePath(a, b string) bool {
	a, _ = filepath.Abs(a)
	b, _ = filepath.Abs(b)
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ── drawing ─────────────────────────────────────────────────────────────

// viewPanes draws the chat area: the sidebar, the tab bar and the panes
// of the tab on screen.
func (m Model) viewPanes(cw, h int) []string {
	ps := m.panes
	a := m.areaIn(cw, h)
	var content []string
	if m.tabBarShown() {
		bar, _ := m.tabBar(a.w)
		content = append(content, bar)
	}
	t := ps.cur()
	switch {
	case t == nil:
		content = append(content, m.placeholder(a.w, a.h)...)
	case t.Zoom:
		content = append(content, m.drawPane(t.Focus, a.w, a.h)...)
	default:
		content = append(content, m.drawNode(t.Root, a.w, a.h)...)
	}
	if !m.sidebarShown() {
		return content
	}
	side, _ := m.sidebarLines(sidebarW, h)
	sep := faintStyle.Render("│")
	out := make([]string, h)
	for i := range out {
		row := ""
		if i < len(content) {
			row = content[i]
		}
		out[i] = side[i] + sep + row
	}
	return out
}

func (m Model) drawNode(n *layoutNode, w, h int) []string {
	if n.Pane != "" {
		return m.drawPane(n.Pane, w, h)
	}
	out := make([]string, 0, h)
	if n.Split == "right" {
		wa, wb := splitAt(w, n.Ratio)
		a, b := m.drawNode(n.A, wa, h), m.drawNode(n.B, wb, h)
		sep := faintStyle.Render("│")
		for i := range h {
			out = append(out, a[i]+sep+b[i])
		}
		return out
	}
	ha, hb := splitAt(h, n.Ratio)
	out = append(out, m.drawNode(n.A, w, ha)...)
	out = append(out, faintStyle.Render(strings.Repeat("─", w)))
	return append(out, m.drawNode(n.B, w, hb)...)
}

// drawPane draws one pane in exactly w × h cells: a header, then the
// chat, the terminal or its scrollback.
func (m Model) drawPane(id string, w, h int) []string {
	if h <= 0 {
		return nil
	}
	out := []string{cell(m.paneHeader(id, w), w)}
	var body []string
	switch {
	case id == chatPane:
		body = m.chatBody(w, h-1)
	case m.panes.copy != nil && m.panes.copy.id == id:
		body = m.copyView(w, h-1)
	default:
		body = m.paneScreenLines(id)
		if s := m.panes.sel; s != nil && s.id == id {
			body = s.draw(body)
		}
	}
	body = fitHeight(body, h-1, false)
	for _, l := range body {
		out = append(out, cell(l, w))
	}
	return out
}

func (m Model) paneScreenLines(id string) []string {
	scr, ok := m.panes.screens[id]
	if !ok {
		return []string{dimStyle.Render(" starting…")}
	}
	return scr.Lines
}

func (m Model) paneHeader(id string, w int) string {
	ps := m.panes
	t := ps.cur()
	focused := t != nil && id == t.Focus
	name := func(s string) string {
		if focused {
			return hiStyle.Bold(true).Render(s)
		}
		return dimStyle.Render(s)
	}
	right := ""
	if t != nil && t.Zoom {
		right = dimStyle.Render("zoom · ctrl+b z")
	}
	if id == chatPane {
		left := name("tubas")
		if m.session.started() {
			left += dimStyle.Render(" · " + fit(m.session.Question, max(10, w/2)))
		}
		if right == "" && m.running {
			right = hiStyle.Render(spinner[m.frame%len(spinner)]) + dimStyle.Render(" agents working")
		}
		return spread(left, right, w)
	}
	info := ps.infos[id]
	state := m.paneStateOf(id)
	where := ""
	switch {
	case info.Worktree != nil:
		where = info.Worktree.Branch
	case info.Repo != "":
		where = info.Repo
	case info.Dir != "":
		where = filepath.Base(info.Dir)
	}
	left := stateIcon(state, m.frame) + " " + name(info.Title)
	if info.Agent != "" && !strings.Contains(strings.ToLower(info.Title), info.Agent) {
		left += dimStyle.Render(" · " + info.Agent)
	}
	if where != "" {
		left += dimStyle.Render(" · " + where)
	}
	if c := ps.copy; c != nil && c.id == id {
		right = hiStyle.Render("copy") + dimStyle.Render(fmt.Sprintf(" %d/%d", c.cy+1, len(c.plain)))
	}
	if right == "" {
		switch state {
		case pane.Blocked:
			right = warnStyle.Render("blocked")
		case pane.Working:
			right = hiStyle.Render("working")
		case pane.Done:
			if info.Exit != "" {
				right = dimStyle.Render(info.Exit)
			} else {
				right = okStyle.Render("done")
			}
		default:
			right = dimStyle.Render("idle")
		}
	}
	if focused || info.Exit != "" {
		right += dimStyle.Render(" ✕") // a click closes it
	}
	return spread(left, right, w)
}

// paneHint replaces the input line while a terminal has the keys.
func (m Model) paneHint(cw int) string {
	ps := m.panes
	f := ps.focus()
	right := m.notice
	var left string
	switch {
	case f == "":
		left = dimStyle.Render("nothing open in " + ps.space().Name)
		if right == "" {
			right = dimStyle.Render("ctrl+b c new tab · w next workspace · N new workspace")
		}
	case ps.copy != nil && ps.copy.id == f:
		left = hiStyle.Render("copy ") + dimStyle.Render(ps.infos[f].Title)
		if ps.copy.searching {
			left = hiStyle.Render("/") + textStyle.Render(ps.copy.query) + barStyle.Render(" ")
		}
		if right == "" {
			right = dimStyle.Render("hjkl move · v select · y copy · / search · n next · q back")
		}
	default:
		left = hiStyle.Render("⌨ ") + dimStyle.Render("keys go to ") + boldStyle.Render(ps.infos[f].Title)
		switch {
		case right != "":
		case m.session.Pending != nil:
			right = warnStyle.Render("an agent waits for you") + dimStyle.Render(" · ctrl+b h, then y / n")
		default:
			right = dimStyle.Render("ctrl+b  hjkl move · z zoom · [ scroll · c tab · b sidebar · x close · q detach")
		}
	}
	return spread(left, right, cw)
}

// panesRollup counts the terminals by state, for the nav line.
func panesRollup(infos []pane.Info) string {
	c := pane.Counts(infos)
	var parts []string
	if n := c[pane.Blocked]; n > 0 {
		parts = append(parts, warnStyle.Render(fmt.Sprintf("%d blocked", n)))
	}
	if n := c[pane.Working]; n > 0 {
		parts = append(parts, dimStyle.Render(fmt.Sprintf("%d working", n)))
	}
	if len(parts) == 0 {
		return ""
	}
	return dimStyle.Render("panes ") + strings.Join(parts, dimStyle.Render(" · "))
}
