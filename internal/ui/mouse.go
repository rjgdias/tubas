package ui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── the mouse ───────────────────────────────────────────────────────────
//
// Almost everything you can reach with a key, you can click:
//
//   - the sections in the top bar, and the sessions rollup (ctrl+g);
//   - in the sidebar: a workspace, a tab, a session (shown in the chat),
//     + new tab, + new session, and the line at the bottom to hide it;
//   - the tab bar, when the sidebar is hidden;
//   - a pane, to focus it; its ✕ to close it; a double click on its
//     header to zoom it;
//   - the border between two panes, dragged, to resize them;
//   - y, n and v on an approval bar;
//   - a row in the session picker, or in a section's list.
//
// The wheel scrolls a terminal's history (copy mode) or the chat;
// dragging over a terminal selects text and copies it when you let go.

// Where the body starts on screen: one column of margin, and the header,
// the top bar, a rule and a blank line above it.
const bodyX, bodyY = 1, 4

const doubleClick = 400 * time.Millisecond

func (m Model) mouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	if m.width == 0 {
		return m, nil
	}
	right := msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonRight
	wheel := msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown
	up := msg.Button == tea.MouseButtonWheelUp
	press := msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft

	if m.showHelp { // a click anywhere closes the keys panel
		if press || right {
			m.showHelp = false
		}
		return m, nil
	}
	if m.menu != nil {
		return m.menuClick(msg)
	}
	// What is being dragged follows the mouse until the button comes up.
	if ps := m.panes; ps != nil && (ps.drag != nil || ps.sel != nil) {
		return m.dragging(msg)
	}
	cw, bodyH := m.width-2, m.bodyHeight()
	bx, by := msg.X-bodyX, msg.Y-bodyY
	if m.form != nil {
		if press {
			return m.clickForm(msg, bx, by)
		}
		return m, nil
	}
	if press && msg.Y == 1 { // the top bar
		return m.clickNav(msg.X - bodyX)
	}
	if press && msg.Y == 0 { // the header: the model, the agents
		return m.clickHeader(msg.X)
	}
	if press && m.section == SecChat && m.session.Pending != nil {
		if bar := bodyY + bodyH + len(m.viewPopup()); msg.Y == bar {
			return m.clickBar(bx, cw)
		}
	}
	// Below the body: the / and @ menu, then the line of hints and
	// notices, where every "key action" is a button.
	if press && by >= bodyH {
		if mm, cmd, ok := m.clickPopup(by - bodyH); ok {
			return mm, cmd
		}
		return m.clickHint(msg)
	}
	if by < 0 || by >= bodyH || bx < 0 || bx >= cw {
		return m, nil
	}
	switch {
	case m.connect != nil:
		if press {
			return m.clickConnect(msg, by)
		}
		return m, nil
	case m.modelPick != nil:
		if press {
			return m.clickModelPicker(msg, by, bodyH)
		}
		return m, nil
	case m.mcp != nil:
		if press {
			return m.clickMCP(msg, by)
		}
		return m, nil
	case m.section != SecChat:
		if press {
			return m.clickSection(msg, bx, by, cw)
		}
		return m, nil
	case m.picker:
		convs := m.conversations()
		if by >= 2 && by-2 < len(convs) {
			if right {
				m.pickIdx = by - 2
				m.menu = m.menuFor(sideRow{ws: -1, tab: -1, session: convs[by-2].ID}, msg.X, msg.Y)
				return m, nil
			}
			if press {
				return m.attachConversation(convs[by-2])
			}
		}
		if press {
			return m.clickHint(msg) // "r renames · d deletes" in its header
		}
		return m, nil
	case m.review:
		return m, nil
	case !m.split():
		if wheel && msg.Action == tea.MouseActionPress {
			m = m.scrollBy(map[bool]int{true: 3, false: -3}[up])
		}
		if press {
			return m.clickChat(msg)
		}
		return m, nil
	}
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	if m.sidebarShown() && bx < sidebarW {
		if press {
			return m.clickSidebar(by, bodyH)
		}
		if right {
			if _, rows := m.sidebarLines(sidebarW, bodyH); by < len(rows) {
				m.menu = m.menuFor(rows[by], msg.X, msg.Y)
			}
		}
		return m, nil
	}
	return m.clickPanes(msg, bx, by, cw, bodyH)
}

// clickNav opens the section under the mouse in the top bar; the
// sessions rollup at the right opens the picker.
func (m Model) clickNav(x int) (Model, tea.Cmd) {
	at := 0
	for s := range numSections {
		w := lipgloss.Width(fmt.Sprintf(" %d %s ", s+1, s))
		if s == SecInbox && m.inboxCount() > 0 {
			w += lipgloss.Width(fmt.Sprintf(" %d ", m.inboxCount()))
		}
		if s == SecRoutines && len(m.runs) > 0 {
			w += 2
		}
		if x >= at && x < at+w {
			m.form, m.picker, m.review = nil, false, false
			return m.goTo(s), nil
		}
		at += w
	}
	if x > at && m.cfg.Remote != nil && m.status != nil && len(m.status.Sessions) > 0 {
		m.section, m.picker, m.pickIdx = SecChat, true, 0
	}
	return m, nil
}

// clickBar answers the approval on screen: y, n, or v for the details.
func (m Model) clickBar(x, cw int) (Model, tea.Cmd) {
	v := " details "
	if m.showDetails {
		v = " hide "
	}
	yw, nw, vw := len("y approve   "), len("n deny   "), len("v"+v)
	start := cw - (yw + nw + vw)
	switch {
	case x >= start && x < start+yw:
		if len(m.queue) > 0 {
			return m.answerApproval(true), nil
		}
	case x >= start+yw && x < start+yw+nw:
		if len(m.queue) > 0 {
			return m.answerApproval(false), nil
		}
	default:
		m.showDetails = !m.showDetails
	}
	return m, nil
}

// clickList selects the row under the mouse in a section's list.
func (m Model) clickList(bx, by, cw int) (Model, tea.Cmd) {
	if m.section == SecAgents && (m.permMode || m.skillsMode) {
		return m, nil
	}
	if bx < listWidth(cw) && by >= 2 && by-2 < m.sectionLen(m.section) {
		m.secIdx[m.section] = by - 2
	}
	return m, nil
}

// clickSidebar shows what the row points at.
func (m Model) clickSidebar(by, bodyH int) (Model, tea.Cmd) {
	ps := m.panes
	_, rows := m.sidebarLines(sidebarW, bodyH)
	if by >= len(rows) {
		return m, nil
	}
	row := rows[by]
	switch {
	case row.action == "hide":
		hidden := false
		ps.sidebar = &hidden
		return m, ps.saveLayout()
	case row.action == "newtab":
		ps.ws = row.ws
		return m.newPaneForm("tab", false), nil
	case row.action == "newsession":
		m = m.showChat()
		return m.newSession(), nil
	case row.session != "":
		for _, c := range m.conversations() {
			if c.ID == row.session {
				m = m.showChat()
				if c.ID == m.conversation {
					return m, nil
				}
				return m.attachConversation(c)
			}
		}
	case row.ws >= 0:
		ps.ws = row.ws
		if row.tab >= 0 {
			ps.spaces[row.ws].Tab = row.tab
		}
		return m, ps.saveLayout()
	}
	return m, nil
}

// showChat brings the chat's tab on screen and focuses it.
func (m Model) showChat() Model {
	m.section, m.picker, m.review = SecChat, false, false
	if ps := m.panes; ps != nil {
		if w, t := ps.where(chatPane); w != nil {
			m = m.showPane(w, t, chatPane)
			t.Zoom = false
		}
	}
	return m
}

// clickPanes handles the tab bar, the borders and the panes.
func (m Model) clickPanes(msg tea.MouseMsg, bx, by, cw, bodyH int) (Model, tea.Cmd) {
	ps := m.panes
	wheel := msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown
	up := msg.Button == tea.MouseButtonWheelUp
	a := m.areaIn(cw, bodyH)
	if m.tabBarShown() && by == 0 {
		if wheel {
			return m, nil
		}
		_, spans := m.tabBar(a.w)
		for i, sp := range spans {
			if bx-a.x >= sp[0] && bx-a.x < sp[1] {
				ps.space().Tab = i
				return m, ps.saveLayout()
			}
		}
		return m, nil
	}
	t := ps.cur()
	if t == nil {
		return m, nil
	}
	ax, ay := bx-a.x, by-a.y
	if !wheel && msg.Button == tea.MouseButtonLeft && !t.Zoom {
		var seps []separator
		t.Root.placeAll(rect{0, 0, a.w, a.h}, map[string]rect{}, &seps)
		for i := range seps {
			if seps[i].line.contains(ax, ay) {
				ps.drag = &seps[i]
				return m, nil
			}
		}
	}
	for id, r := range m.layoutRects() {
		if !r.contains(ax, ay) {
			continue
		}
		switch {
		case wheel && id == chatPane:
			m = m.scrollBy(map[bool]int{true: 3, false: -3}[up])
		case wheel:
			t.Focus = id
			step := 3
			if up {
				step = -3
			}
			if ps.copy == nil || ps.copy.id != id {
				if !up {
					return m, nil
				}
				return m.enterCopy(id, step)
			}
			m = m.copyScroll(step)
		case msg.Button == tea.MouseButtonLeft && ay == r.y: // the header
			if id != chatPane && ax >= r.x+r.w-2 && (t.Focus == id || ps.infos[id].Exit != "") {
				t.Focus = id
				mm, cmd, _ := m.paneKey("x") // ✕: close, asking first while it runs
				return mm, cmd
			}
			now := time.Now()
			if ps.lastClick.id == id && now.Sub(ps.lastClick.at) < doubleClick {
				t.Focus, t.Zoom = id, !t.Zoom
				ps.lastClick.id = ""
				return m, ps.saveLayout()
			}
			ps.lastClick.id, ps.lastClick.at = id, now
			t.Focus = id
		case msg.Button == tea.MouseButtonLeft && id == chatPane:
			t.Focus = id
			return m.clickChat(msg)
		case msg.Button == tea.MouseButtonLeft:
			t.Focus = id
			if id != chatPane && (ps.copy == nil || ps.copy.id != id) {
				x, y := ax-r.x, ay-r.y-1
				ps.sel = &selection{id: id, x0: x, y0: y, x1: x, y1: y}
			}
		}
		return m, nil
	}
	return m, nil
}

// dragging moves a border, or the end of a selection, with the mouse.
func (m Model) dragging(msg tea.MouseMsg) (Model, tea.Cmd) {
	ps := m.panes
	cw, bodyH := m.width-2, m.bodyHeight()
	a := m.areaIn(cw, bodyH)
	ax, ay := msg.X-bodyX-a.x, msg.Y-bodyY-a.y
	if d := ps.drag; d != nil {
		switch msg.Action {
		case tea.MouseActionMotion:
			if d.node.Split == "right" {
				d.node.Ratio = clampRatio(float64(ax-d.parent.x) / float64(max(1, d.parent.w-1)))
			} else {
				d.node.Ratio = clampRatio(float64(ay-d.parent.y) / float64(max(1, d.parent.h-1)))
			}
		case tea.MouseActionRelease:
			ps.drag = nil
			return m, ps.saveLayout()
		}
		return m, nil
	}
	s := ps.sel
	switch msg.Action {
	case tea.MouseActionMotion:
		if r, ok := m.layoutRects()[s.id]; ok {
			s.x1 = max(0, min(r.w-1, ax-r.x))
			s.y1 = max(0, min(r.h-2, ay-r.y-1))
			s.moved = true
		}
	case tea.MouseActionRelease:
		ps.sel = nil
		if s.moved {
			return m, copyText(s.text(m.paneScreenLines(s.id)))
		}
	}
	return m, nil
}

func clampRatio(r float64) float64 { return max(0.1, min(0.9, r)) }
