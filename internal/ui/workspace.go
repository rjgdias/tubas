package ui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"tubas/internal/pane"
	"tubas/internal/server"
)

// ── workspaces and tabs ─────────────────────────────────────────────────
//
// Like herdr, panes live in tabs and tabs in workspaces. A workspace is
// a place you work on: the team, each repository connector, or any folder
// you add (ctrl+b N). Each tab has its own split layout. The sidebar
// (ctrl+b b) lists them all with the state of what runs inside, so a
// blocked agent in another tab is never out of sight.
//
// The whole arrangement is saved in the server (and in .tubas/panes.json),
// so a window that attaches later, or a server that restarts, finds it
// the same way.

const sidebarW = 26

type workspace struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	Repo string `json:"repo,omitempty"` // a repository connector: agents get worktrees of it
	Tabs []*tab `json:"tabs"`
	Tab  int    `json:"tab"` // the tab on screen
	Own  bool   `json:"own,omitempty"`
}

type tab struct {
	Name  string      `json:"name"`
	Root  *layoutNode `json:"root"`
	Focus string      `json:"focus"`
	Zoom  bool        `json:"zoom,omitempty"`
}

type savedLayout struct {
	V       int          `json:"v"`
	Spaces  []*workspace `json:"spaces"`
	WS      int          `json:"ws"`
	Sidebar *bool        `json:"sidebar,omitempty"`
}

// defaultSpaces are the team and its repositories, the chat in the
// team's first tab.
func (m Model) defaultSpaces() []*workspace {
	name := m.cfg.Name
	if name == "" {
		name = "team"
	}
	spaces := []*workspace{{Name: name, Dir: m.cfg.TeamDir,
		Tabs: []*tab{{Name: "chat", Root: &layoutNode{Pane: chatPane}, Focus: chatPane}}}}
	var repos []string
	for r := range m.cfg.Repos {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	for _, r := range repos {
		spaces = append(spaces, &workspace{Name: r, Dir: m.cfg.Repos[r].Path, Repo: r})
	}
	return spaces
}

// loadLayout reads the saved arrangement (or an old single layout) and
// makes sure the team, its repositories and the chat are all there.
func (m Model) loadLayout(s string) {
	ps := m.panes
	defaults := m.defaultSpaces()
	var saved savedLayout
	switch {
	case strings.Contains(s, `"v":2`) && json.Unmarshal([]byte(s), &saved) == nil && len(saved.Spaces) > 0:
		ps.spaces, ps.ws, ps.sidebar = saved.Spaces, saved.WS, saved.Sidebar
	default:
		ps.spaces = defaults
		if root := decodeLayout(s); root != nil { // the layout of one screen, from before workspaces
			ps.spaces[0].Tabs[0].Root = root
		}
	}
	// Repositories added to team.yaml since get their workspace.
	for _, d := range defaults[1:] {
		found := false
		for _, w := range ps.spaces {
			found = found || w.Repo == d.Repo
		}
		if !found {
			ps.spaces = append(ps.spaces, d)
		}
	}
	// Tabs whose layout is missing are dropped; the chat is always somewhere.
	hasChat := false
	for _, w := range ps.spaces {
		var keep []*tab
		for _, t := range w.Tabs {
			if t.Root != nil {
				keep = append(keep, t)
				hasChat = hasChat || t.Root.has(chatPane)
			}
		}
		w.Tabs = keep
		w.Tab = max(0, min(w.Tab, len(w.Tabs)-1))
	}
	if !hasChat {
		ps.spaces[0].Tabs = append([]*tab{{Name: "chat", Root: &layoutNode{Pane: chatPane}, Focus: chatPane}}, ps.spaces[0].Tabs...)
	}
	ps.ws = max(0, min(ps.ws, len(ps.spaces)-1))
}

func (ps *paneState) encode() string {
	data, _ := json.Marshal(savedLayout{V: 2, Spaces: ps.spaces, WS: ps.ws, Sidebar: ps.sidebar})
	return string(data)
}

// space is the workspace on screen.
func (ps *paneState) space() *workspace { return ps.spaces[ps.ws] }

// cur is the tab on screen, nil in a workspace with no tabs yet.
func (ps *paneState) cur() *tab {
	w := ps.space()
	if len(w.Tabs) == 0 {
		return nil
	}
	return w.Tabs[w.Tab]
}

// focus is the focused pane of the tab on screen.
func (ps *paneState) focus() string {
	if t := ps.cur(); t != nil {
		return t.Focus
	}
	return ""
}

// where finds the workspace and tab that hold a pane.
func (ps *paneState) where(id string) (*workspace, *tab) {
	for _, w := range ps.spaces {
		for _, t := range w.Tabs {
			if t.Root.has(id) {
				return w, t
			}
		}
	}
	return nil, nil
}

// terminals counts the terminal panes in every tab.
func (ps *paneState) terminals() int {
	n := 0
	for _, w := range ps.spaces {
		for _, t := range w.Tabs {
			for _, l := range t.Root.leaves() {
				if l != chatPane {
					n++
				}
			}
		}
	}
	return n
}

// removePane takes a pane out of its tab; an empty tab goes too.
func (ps *paneState) removePane(id string) {
	for _, w := range ps.spaces {
		for i, t := range w.Tabs {
			if !t.Root.has(id) {
				continue
			}
			t.Root = withoutPane(t.Root, id)
			if t.Root == nil {
				w.Tabs = append(w.Tabs[:i:i], w.Tabs[i+1:]...)
				w.Tab = max(0, min(w.Tab, len(w.Tabs)-1))
			} else if t.Focus == id {
				t.Focus = t.Root.leaves()[0]
				t.Zoom = false
			}
			return
		}
	}
}

// home picks the workspace a pane opened elsewhere belongs to: its
// repository's, or the one whose folder holds it, or the one on screen.
func (ps *paneState) home(i pane.Info) *workspace {
	if i.Repo != "" {
		for _, w := range ps.spaces {
			if w.Repo == i.Repo {
				return w
			}
		}
	}
	var best *workspace
	for _, w := range ps.spaces {
		if w.Dir != "" && inside(i.Dir, w.Dir) && (best == nil || len(w.Dir) > len(best.Dir)) {
			best = w
		}
	}
	if best != nil {
		return best
	}
	return ps.space()
}

func inside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// sidebarShown says whether the sidebar is on screen: as you set it with
// ctrl+b b, or by itself once there are terminals and room.
func (m Model) sidebarShown() bool {
	ps := m.panes
	if ps == nil || m.width-2 < 80 {
		return false
	}
	if ps.sidebar != nil {
		return *ps.sidebar
	}
	sessions := m.cfg.Remote != nil && len(m.conversations()) >= 2
	return (ps.terminals() > 0 || sessions) && m.width-2 >= 110
}

// tabBarShown: without the sidebar, a line of tabs when there are several.
func (m Model) tabBarShown() bool {
	return !m.sidebarShown() && len(m.panes.space().Tabs) > 1
}

// worst is the state that matters most among some panes.
func (m Model) worst(ids []string) string {
	rank := map[string]int{pane.Blocked: 4, pane.Working: 3, pane.Done: 2, pane.Idle: 1}
	best := ""
	for _, id := range ids {
		s := m.paneStateOf(id)
		if rank[s] > rank[best] {
			best = s
		}
	}
	return best
}

func (m Model) paneStateOf(id string) string {
	if id == chatPane {
		switch {
		case m.session.Pending != nil:
			return pane.Blocked
		case m.running:
			return pane.Working
		}
		return pane.Idle
	}
	if scr, ok := m.panes.screens[id]; ok && scr.State != "" && m.panes.watching[id] {
		return scr.State
	}
	return m.panes.infos[id].State
}

// sideRow is what a sidebar line points at, for the mouse: a workspace
// (tab -1), a tab, a session, or an action (newtab, newsession, hide).
type sideRow struct {
	ws, tab         int
	session, action string
}

var nothing = sideRow{ws: -1, tab: -1}

// sidebarLines draws the sidebar in w × h, with what each row points at.
func (m Model) sidebarLines(w, h int) ([]string, []sideRow) {
	ps := m.panes
	var lines []string
	var rows []sideRow
	for wi, ws := range ps.spaces {
		var ids []string
		for _, t := range ws.Tabs {
			ids = append(ids, t.Root.leaves()...)
		}
		name := dimStyle.Render(ws.Name)
		if wi == ps.ws {
			name = boldStyle.Render(ws.Name)
		}
		icon := ""
		if s := m.worst(ids); s != "" && s != pane.Idle {
			icon = stateIcon(s, m.frame)
		}
		lines = append(lines, spread(" "+name, icon+" ", w))
		rows = append(rows, sideRow{ws: wi, tab: -1})
		for ti, t := range ws.Tabs {
			label := fmt.Sprintf("%d %s", ti+1, t.Name)
			line := "   " + dimStyle.Render(fit(label, w-7))
			if wi == ps.ws && ti == ws.Tab {
				line = " " + hiStyle.Render("› ") + textStyle.Render(fit(label, w-7))
			}
			icon := ""
			if s := m.worst(t.Root.leaves()); s != "" && s != pane.Idle {
				icon = stateIcon(s, m.frame)
			}
			lines = append(lines, spread(line, icon+" ", w))
			rows = append(rows, sideRow{ws: wi, tab: ti})
		}
		if wi == ps.ws {
			lines = append(lines, "   "+hiStyle.Render("+")+faintStyle.Render(" new tab"))
			rows = append(rows, sideRow{ws: wi, tab: -1, action: "newtab"})
		}
	}
	// Your conversations, on the server: click one to show it in the chat.
	if convs := m.conversations(); m.cfg.Remote != nil && len(convs) > 0 {
		lines = append(lines, "", " "+faintStyle.Render("conversations"))
		rows = append(rows, nothing, nothing)
		for _, c := range convs {
			label := fit(c.Title, w-6)
			line := "   " + dimStyle.Render(label)
			if c.ID == m.conversation {
				line = " " + hiStyle.Render("› ") + textStyle.Render(label)
			}
			icon := ""
			if c.State != server.Idle {
				icon = stateIcon(c.State, m.frame)
			}
			lines = append(lines, spread(line, icon+" ", w))
			rows = append(rows, sideRow{ws: -1, tab: -1, session: c.ID})
		}
		lines = append(lines, "   "+hiStyle.Render("+")+faintStyle.Render(" new conversation"))
		rows = append(rows, sideRow{ws: -1, tab: -1, action: "newsession"})
	}
	for len(lines) < h-1 {
		lines = append(lines, "")
		rows = append(rows, nothing)
	}
	if len(lines) >= h {
		lines, rows = lines[:h-1], rows[:h-1]
	}
	lines = append(lines, faintStyle.Render(" ctrl+b b hide · w next"))
	rows = append(rows, sideRow{ws: -1, tab: -1, action: "hide"})
	for i := range lines {
		lines[i] = cell(lines[i], w)
	}
	return lines, rows
}

// tabBar draws the tabs of the workspace on screen in one line, with the
// columns each one takes, for the mouse.
func (m Model) tabBar(w int) (string, [][2]int) {
	ws := m.panes.space()
	var b strings.Builder
	var spans [][2]int
	x := 0
	for i, t := range ws.Tabs {
		label := fmt.Sprintf(" %d %s ", i+1, t.Name)
		if s := m.worst(t.Root.leaves()); s != "" && s != pane.Idle {
			label = fmt.Sprintf(" %d %s %s ", i+1, t.Name, stateIcon(s, m.frame))
		}
		styled := dimStyle.Render(label)
		if i == ws.Tab {
			styled = barBold.Render(label)
		}
		width := len([]rune(fmt.Sprintf(" %d %s ", i+1, t.Name)))
		if s := m.worst(t.Root.leaves()); s != "" && s != pane.Idle {
			width += 2
		}
		spans = append(spans, [2]int{x, x + width})
		b.WriteString(styled + " ")
		x += width + 1
	}
	return cell(b.String(), w), spans
}

// placeholder is the body of a workspace with no tabs.
func (m Model) placeholder(w, h int) []string {
	ws := m.panes.space()
	lines := []string{"", "  " + boldStyle.Render(ws.Name) + dimStyle.Render("  "+ws.Dir), "",
		"  " + dimStyle.Render("nothing open here yet"),
		"  " + hiStyle.Render("ctrl+b c") + dimStyle.Render("  a tab with a shell, claude, codex or any command"),
		"  " + hiStyle.Render("ctrl+b w") + dimStyle.Render("  the next workspace"),
	}
	out := fitHeight(lines, h, false)
	for i := range out {
		out[i] = cell(out[i], w)
	}
	return out
}
