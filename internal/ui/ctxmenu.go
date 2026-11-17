package ui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ── the right-click menu ────────────────────────────────────────────────
//
// A right click on a workspace, a tab or a conversation (in the sidebar
// or the conversation list) opens a small menu where it was clicked:
// rename it, or delete it. ↑↓ and enter, or a click, pick; esc or a
// click outside closes it. Rename turns the menu into a text field;
// delete asks once more, in the menu.

type ctxItem struct{ label, action string }

type ctxMenu struct {
	x, y     int // the screen cell it opens at
	title    string
	items    []ctxItem
	idx      int
	row      sideRow // what it is about: a workspace, a tab or a conversation
	renaming bool
	input    []rune
}

// menuFor builds the menu for a sidebar row, or nil when there is none.
func (m Model) menuFor(row sideRow, x, y int) *ctxMenu {
	ps := m.panes
	menu := &ctxMenu{x: x, y: y, row: row}
	switch {
	case row.session != "":
		for _, c := range m.conversations() {
			if c.ID == row.session {
				menu.title = c.Title
			}
		}
		if menu.title == "" {
			return nil
		}
		menu.items = []ctxItem{{"Rename", "rename"}, {"Delete", "delete"}}
	case ps != nil && row.ws >= 0 && row.ws < len(ps.spaces) && row.tab >= 0 && row.tab < len(ps.spaces[row.ws].Tabs):
		menu.title = ps.spaces[row.ws].Tabs[row.tab].Name
		menu.items = []ctxItem{{"Rename", "rename"}, {"Close", "delete"}}
	case ps != nil && row.ws >= 0 && row.ws < len(ps.spaces) && row.action == "":
		menu.title = ps.spaces[row.ws].Name
		menu.items = []ctxItem{{"Rename", "rename"}, {"Delete", "delete"}}
	default:
		return nil
	}
	return menu
}

// currentName is what the menu's thing is called now.
func (m Model) currentName(menu *ctxMenu) string {
	row := menu.row
	switch {
	case row.session != "":
		return menu.title
	case row.tab >= 0:
		return m.panes.spaces[row.ws].Tabs[row.tab].Name
	default:
		return m.panes.spaces[row.ws].Name
	}
}

func (m Model) menuKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	menu := *m.menu
	key := msg.String()
	if menu.renaming {
		switch msg.Type {
		case tea.KeyEsc:
			m.menu = nil
			return m, nil
		case tea.KeyEnter:
			return m.renameTo(&menu, strings.TrimSpace(string(menu.input)))
		case tea.KeyBackspace:
			if len(menu.input) > 0 {
				menu.input = menu.input[:len(menu.input)-1]
			}
		case tea.KeyCtrlU:
			menu.input = nil
		case tea.KeySpace:
			menu.input = append(menu.input, ' ')
		case tea.KeyRunes:
			menu.input = append(menu.input, []rune(typedText(msg))...)
		}
		m.menu = &menu
		return m, nil
	}
	switch key {
	case "esc", "q":
		m.menu = nil
		return m, nil
	case "up", "k":
		menu.idx = max(0, menu.idx-1)
	case "down", "j":
		menu.idx = min(len(menu.items)-1, menu.idx+1)
	case "enter", " ":
		return m.menuAction(&menu, menu.items[menu.idx].action)
	case "r":
		return m.menuAction(&menu, "rename")
	case "d":
		return m.menuAction(&menu, "delete")
	}
	m.menu = &menu
	return m, nil
}

func (m Model) menuAction(menu *ctxMenu, action string) (Model, tea.Cmd) {
	switch action {
	case "rename":
		menu.renaming, menu.input = true, []rune(m.currentName(menu))
		m.menu = menu
		return m, nil
	case "delete":
		label := "Yes, delete"
		if menu.row.tab >= 0 && menu.row.session == "" {
			label = "Yes, close"
		}
		menu.items, menu.idx = []ctxItem{{label, "confirm"}, {"No", "close"}}, 1
		m.menu = menu
		return m, nil
	case "confirm":
		m.menu = nil
		return m.deleteRow(menu.row)
	}
	m.menu = nil
	return m, nil
}

// renameTo saves a new name for what the menu is about.
func (m Model) renameTo(menu *ctxMenu, name string) (Model, tea.Cmd) {
	m.menu = nil
	if name == "" {
		m.notice = dimStyle.Render("an empty name: nothing changed")
		return m, nil
	}
	row := menu.row
	switch {
	case row.session != "":
		if m.cfg.Remote == nil {
			return m, nil
		}
		c, id := m.cfg.Remote, row.session
		return m, func() tea.Msg { return conversationRenamedMsg{name, c.RenameConversation(id, name)} }
	case row.tab >= 0:
		m.panes.spaces[row.ws].Tabs[row.tab].Name = name
	default:
		m.panes.spaces[row.ws].Name = name
	}
	m.notice = okStyle.Render("✓ renamed to " + name)
	return m, m.panes.saveLayout()
}

type conversationRenamedMsg struct {
	name string
	err  error
}

func (m Model) conversationRenamed(msg conversationRenamedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.notice = badStyle.Render("could not rename: " + msg.err.Error())
		return m, nil
	}
	m.notice = okStyle.Render("✓ renamed to " + msg.name)
	return m, pollStatus(m.cfg.Remote)
}

// deleteRow deletes (or closes) what a sidebar row points at.
func (m Model) deleteRow(row sideRow) (Model, tea.Cmd) {
	ps := m.panes
	switch {
	case row.session != "":
		return m.deleteConversation(row.session)
	case row.tab >= 0:
		return m.closeTab(ps.spaces[row.ws], row.tab)
	default:
		return m.closeWorkspace(ps.spaces[row.ws])
	}
}

// closeTab stops a tab's terminals and removes it; the chat's tab keeps
// the chat alone.
func (m Model) closeTab(ws *workspace, ti int) (Model, tea.Cmd) {
	ps := m.panes
	t := ws.Tabs[ti]
	var cmds []tea.Cmd
	chat := false
	for _, id := range t.Root.leaves() {
		if id == chatPane {
			chat = true
			continue
		}
		m = m.closePane(id)
		host, pid := ps.host, id
		cmds = append(cmds, func() tea.Msg { host.Close(pid); return nil })
	}
	// Closing its last pane may already have taken an emptied tab away.
	idx := slices.Index(ws.Tabs, t)
	switch {
	case chat && idx >= 0:
		ws.Tabs[idx] = &tab{Name: t.Name, Root: &layoutNode{Pane: chatPane}, Focus: chatPane}
	case idx >= 0 && len(ws.Tabs) > 1:
		ws.Tabs = append(ws.Tabs[:idx:idx], ws.Tabs[idx+1:]...)
	case idx >= 0: // its last tab: the workspace goes with it
		if i := slices.Index(ps.spaces, ws); i >= 0 && !hasChat(ws) {
			ps.spaces = append(ps.spaces[:i:i], ps.spaces[i+1:]...)
			ps.ws = 0
		}
	}
	ws.Tab = max(0, min(ws.Tab, len(ws.Tabs)-1))
	m.notice = okStyle.Render("✓ closed the tab " + t.Name)
	return m, tea.Batch(append(cmds, ps.saveLayout())...)
}

// menuClick handles the mouse while the menu is open: a click on an item
// picks it, a click anywhere else closes the menu.
func (m Model) menuClick(msg tea.MouseMsg) (Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	menu := m.menu
	lines := m.menuLines()
	w := lipgloss.Width(lines[0])
	// rows: border, title, then the items (or the text field)
	row := msg.Y - menu.y - 2
	inside := msg.X >= menu.x && msg.X < menu.x+w && msg.Y >= menu.y && msg.Y < menu.y+len(lines)
	if !inside {
		m.menu = nil
		if msg.Button == tea.MouseButtonRight {
			return m.mouse(msg) // a right click elsewhere opens a menu there
		}
		return m, nil
	}
	if msg.Button != tea.MouseButtonLeft || menu.renaming || row < 0 || row >= len(menu.items) {
		return m, nil
	}
	copied := *menu
	copied.idx = row
	return m.menuAction(&copied, copied.items[row].action)
}

// menuLines draws the menu: a bordered box with the name on top.
func (m Model) menuLines() []string {
	menu := m.menu
	width := max(24, min(36, lipgloss.Width(menu.title)+2))
	lines := []string{boldStyle.Render(fit(menu.title, width))}
	if menu.renaming {
		lines = append(lines, dimStyle.Render("new name:"),
			textStyle.Render(fit(string(menu.input), width-1))+barStyle.Render(" "),
			dimStyle.Render("enter saves · esc cancels"))
	} else {
		for i, it := range menu.items {
			label := padRight(it.label, width)
			if i == menu.idx {
				lines = append(lines, barBold.Render(label))
			} else {
				lines = append(lines, textStyle.Render(label))
			}
		}
	}
	return strings.Split(lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colHi).
		Padding(0, 1).Render(strings.Join(lines, "\n")), "\n")
}

// withMenu lays the menu over the screen at its place, keeping the
// screen's colors around it.
func (m Model) withMenu(screen []string) []string {
	box := m.menuLines()
	w := lipgloss.Width(box[0])
	x, y := m.menu.x, m.menu.y
	if x+w > m.width { // keep it on screen
		x = max(0, m.width-w)
	}
	if y+len(box) > len(screen) {
		y = max(0, len(screen)-len(box))
	}
	m.menu.x, m.menu.y = x, y
	out := append([]string(nil), screen...)
	for i, b := range box {
		r := y + i
		if r >= len(out) {
			break
		}
		line := out[r]
		left := ansi.Cut(line, 0, x)
		left += strings.Repeat(" ", max(0, x-ansi.StringWidth(left)))
		out[r] = left + b + ansi.Cut(line, x+w, m.width)
	}
	return out
}
