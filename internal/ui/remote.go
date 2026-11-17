package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tubas/internal/engine"
	"tubas/internal/server"
)

// With a background server (Config.Remote), sessions run there and this
// screen is a client: it shows one session at a time (ctrl+g picks
// another, ctrl+n starts a new one), gathers every session's approvals
// and routine notes in the inbox, and leaves everything running when it
// closes (ctrl+b q, or ctrl+c).

const statusEvery = 2 * time.Second

type (
	statusMsg  server.Status
	statusTick struct{}
)

func pollStatus(c *server.Client) tea.Cmd {
	return func() tea.Msg {
		st, err := c.Status()
		if err != nil {
			return noticeMsg(badStyle.Render("server: " + err.Error()))
		}
		return statusMsg(st)
	}
}

func nextStatusTick() tea.Cmd {
	return tea.Tick(statusEvery, func(time.Time) tea.Msg { return statusTick{} })
}

// pumpRemote feeds a server session's events into out, the same channel
// a local engine.Run would fill, so the rest of the screen doesn't know
// the difference. It replays from the start, so attaching later works.
func pumpRemote(ctx context.Context, c *server.Client, id string, out chan<- engine.Event) {
	defer close(out)
	from := 0
	for ctx.Err() == nil {
		events, info, err := c.Events(id, from, 20*time.Second)
		if err != nil {
			select {
			case out <- engine.Failed{Err: fmt.Errorf("server: %w", err)}:
			case <-ctx.Done():
			}
			return
		}
		for _, w := range events {
			from = w.Seq + 1
			if w.Kind == "answered" {
				continue // answered here, or by another client: the server moves on
			}
			reply := make(chan bool, 1)
			e := w.Decode(reply)
			if e == nil {
				continue
			}
			if ap, ok := e.(engine.ApprovalRequested); ok {
				ap.ID = w.Approval
				e = ap
				go func(approval string) {
					select {
					case ok := <-reply:
						c.Answer(approval, ok)
					case <-ctx.Done():
					}
				}(w.Approval)
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
			if w.Ends() {
				return
			}
		}
		if info.State == server.Done || info.State == server.Idle {
			if len(events) == 0 {
				return // finished, and everything was delivered
			}
		}
	}
}

// startRemote starts a session on the server and listens to it.
func (m Model) startRemote(r request, ctx context.Context, events chan engine.Event) (Model, error) {
	info, err := m.cfg.Remote.Start(server.StartRequest{Target: r.target, Question: r.question, Everyone: r.everyone,
		Context: conversationContext(m.past), Conversation: m.conversation})
	if err != nil {
		return m, err
	}
	m.remoteID, m.conversation = info.ID, convOf(info)
	go pumpRemote(ctx, m.cfg.Remote, info.ID, events)
	return m, nil
}

// attach shows a server session in the chat, replaying what happened.
func (m Model) attach(info server.Info) (Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel() // stop listening to the one on screen; it keeps running on the server
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan engine.Event, 64)
	go pumpRemote(ctx, m.cfg.Remote, info.ID, events)
	m.run++
	m.events, m.cancel, m.running = events, cancel, true
	// Its approvals come again through its own stream, as the chat's.
	m.queue = slices.DeleteFunc(slices.Clone(m.queue), func(p pendingApproval) bool {
		return p.routine == "" || p.session == info.ID
	})
	m.remoteID, m.conversation = info.ID, convOf(info)
	m.past = nil
	m.selected, m.showDetails, m.notice, m.picker = -1, false, "", false
	m.last = request{target: info.Target, question: info.Question}
	m.session = Session{Team: m.cfg.Name, AskedAt: info.Created.Format("15:04"), Target: info.Target,
		Question: info.Question, MaxHops: m.cfg.MaxHops, StartedAt: info.Created}
	m.section = SecChat
	go m.cfg.Remote.Seen(info.ID)
	return m, tea.Batch(waitForEvent(m.run, events), tick(m.run))
}

// newSession leaves the session on screen running and clears the chat
// for a new question.
func (m Model) newSession() Model {
	if m.cancel != nil {
		m.cancel()
	}
	m.run++
	m.running, m.events, m.cancel = false, nil, nil
	m.queue = slices.DeleteFunc(slices.Clone(m.queue), func(p pendingApproval) bool { return p.routine == "" })
	m.session, m.past, m.remoteID, m.conversation, m.scroll = Session{}, nil, "", "", 0
	m.selected, m.picker, m.section = -1, false, SecChat
	m.notice = dimStyle.Render("new conversation · the others stay in ctrl+g")
	return m
}

// applyStatus merges what the server says: approvals waiting in other
// sessions join the inbox, routine notes too, and what was answered
// elsewhere leaves.
func (m Model) applyStatus(st server.Status) Model {
	m.status = &st
	waiting := map[string]bool{}
	for _, p := range st.Pending {
		waiting[p.ID] = true
		if p.Session == m.remoteID {
			continue // the chat's own stream brings this one
		}
		if slices.ContainsFunc(m.queue, func(q pendingApproval) bool { return q.id == p.ID }) {
			continue
		}
		reply := make(chan bool, 1)
		go func(id string, c *server.Client) {
			if ok, open := <-reply; open {
				c.Answer(id, ok)
			}
		}(p.ID, m.cfg.Remote)
		from := p.Title
		if p.Routine != "" {
			from = p.Routine
		}
		m.queue = append(slices.Clone(m.queue), pendingApproval{agent: p.Agent, call: p.Call, reply: reply, at: p.At,
			detail: p.Detail, routine: "session " + from, id: p.ID, session: p.Session})
	}
	m.queue = slices.DeleteFunc(slices.Clone(m.queue), func(q pendingApproval) bool {
		return q.id != "" && q.routine != "" && !waiting[q.id]
	})
	// Routine notes come from the server.
	var keep []inboxNote
	for _, n := range m.notes {
		if n.serverID == "" {
			keep = append(keep, n)
		}
	}
	for _, n := range st.Notes {
		keep = append(keep, inboxNote{kind: "routine", title: n.Routine, text: n.Text, at: n.At, failed: n.Failed,
			routine: n.Routine, serverID: n.ID})
	}
	m.notes = keep
	m.secIdx[SecInbox] = max(0, min(m.secIdx[SecInbox], m.inboxCount()-1))
	return m
}

// firstAttach picks what to show when the screen opens: the session that
// most needs you.
func (m Model) firstAttach(st server.Status) (Model, tea.Cmd) {
	rank := map[string]int{server.Blocked: 0, server.Working: 1, server.Done: 2}
	best := -1
	for i, s := range st.Sessions {
		r, ok := rank[s.State]
		if !ok || s.Kind != "chat" {
			continue
		}
		if best < 0 || r < rank[st.Sessions[best].State] {
			best = i
		}
	}
	if best < 0 {
		return m, nil
	}
	for _, c := range m.conversations() { // the whole conversation, not just its last message
		if c.ID == convOf(st.Sessions[best]) {
			return m.attachConversation(c)
		}
	}
	return m.attach(st.Sessions[best])
}

// sessionAlerts is the notice for sessions that started waiting for you
// since the last status, other than the one on screen.
func sessionAlerts(old *server.Status, now server.Status, onScreen string) string {
	if old == nil {
		return ""
	}
	was := map[string]string{}
	for _, s := range old.Sessions {
		was[s.ID] = s.State
	}
	var parts []string
	for _, s := range now.Sessions {
		if s.ID == onScreen || s.State == was[s.ID] {
			continue
		}
		switch s.State {
		case server.Blocked:
			parts = append(parts, warnStyle.Render("◐ "+s.Title)+dimStyle.Render(" waits for you · ctrl+g"))
		case server.Done:
			parts = append(parts, okStyle.Render("● "+s.Title)+dimStyle.Render(" is done"))
		}
	}
	return strings.Join(parts, dimStyle.Render(" · "))
}

// rollupText is "1 blocked · 2 working · 1 done".
func rollupText(sessions []server.Info) string {
	counts := map[string]int{}
	for _, s := range sessions {
		counts[s.State]++
	}
	var parts []string
	if n := counts[server.Blocked]; n > 0 {
		parts = append(parts, warnStyle.Render(fmt.Sprintf("%d blocked", n)))
	}
	if n := counts[server.Working]; n > 0 {
		parts = append(parts, dimStyle.Render(fmt.Sprintf("%d working", n)))
	}
	if n := counts[server.Done]; n > 0 {
		parts = append(parts, okStyle.Render(fmt.Sprintf("%d done", n)))
	}
	return strings.Join(parts, dimStyle.Render(" · "))
}

func stateIcon(state string, frame int) string {
	switch state {
	case server.Blocked:
		return warnStyle.Render("◐")
	case server.Working:
		return hiStyle.Render(spinner[frame%len(spinner)])
	case server.Done:
		return okStyle.Render("●")
	}
	return dimStyle.Render("○")
}

// ── the session picker (ctrl+g) ─────────────────────────────────────────

func (m Model) pickerKey(key string) (Model, tea.Cmd) {
	list := m.conversations()
	switch key {
	case "esc", "ctrl+g":
		m.picker = false
	case "up":
		m.pickIdx = max(0, m.pickIdx-1)
	case "down":
		m.pickIdx = max(0, min(len(list)-1, m.pickIdx+1))
	case "ctrl+n", "n":
		return m.newSession(), nil
	case "enter":
		if m.pickIdx < len(list) {
			return m.attachConversation(list[m.pickIdx])
		}
	case "d", "delete", "x":
		if m.pickIdx < len(list) {
			return m.askDelete(list[m.pickIdx]), nil
		}
	case "r":
		if m.pickIdx < len(list) {
			if menu := m.menuFor(sideRow{ws: -1, tab: -1, session: list[m.pickIdx].ID}, bodyX+4, bodyY+2+m.pickIdx); menu != nil {
				return m.menuAction(menu, "rename")
			}
		}
	}
	return m, nil
}

// askDelete asks before deleting a conversation: y (or d again) does it.
func (m Model) askDelete(c conversation) Model {
	m.deleting = c.ID
	m.notice = warnStyle.Render("delete "+fit(c.Title, 30)+" ("+plural(len(c.Sessions), "message")+")?") +
		dimStyle.Render(" y deletes it for good · any other key keeps it")
	return m
}

type conversationDeletedMsg struct {
	id  string
	err error
}

// deleteConversation removes a conversation on the server.
func (m Model) deleteConversation(id string) (Model, tea.Cmd) {
	if m.cfg.Remote == nil {
		return m, nil
	}
	c := m.cfg.Remote
	return m, func() tea.Msg { return conversationDeletedMsg{id, c.DeleteConversation(id)} }
}

func (m Model) conversationDeleted(msg conversationDeletedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.notice = badStyle.Render("could not delete: " + msg.err.Error())
		return m, nil
	}
	if msg.id == m.conversation {
		picker := m.picker
		m = m.newSession() // it was on screen: a clean chat instead
		m.picker = picker
	}
	if n := len(m.conversations()) - 1; m.pickIdx >= n {
		m.pickIdx = max(0, n-1)
	}
	m.notice = okStyle.Render("✓ conversation deleted")
	return m, pollStatus(m.cfg.Remote)
}

func (m Model) viewPicker(cw, h int) []string {
	lines := []string{boldStyle.Render("conversations") + dimStyle.Render(" · enter shows one · r renames · d deletes · ctrl+n new · right click: menu"), ""}
	list := m.conversations()
	if len(list) == 0 {
		return fitHeight(append(lines, dimStyle.Render("none yet · ask something and it becomes one")), h, false)
	}
	for i, c := range list {
		last := c.latest()
		what := c.State
		switch c.State {
		case server.Blocked:
			what = "waits for you: " + last.Waiting
		case server.Working:
			what = "working " + time.Since(last.Created).Round(time.Second).String()
		case server.Done, server.Idle:
			what = plainSummary(orDefault(last.Summary, last.Error))
		}
		count := ""
		if n := len(c.Sessions); n > 1 {
			count = fmt.Sprintf(" · %d messages", n)
		}
		name := fmt.Sprintf("%-36s", fit(c.Title, 34))
		row := stateIcon(c.State, m.frame) + " " + textStyle.Render(name) + dimStyle.Render(what+count)
		if c.ID == m.conversation {
			row += faintStyle.Render("  · on screen")
		}
		if i == m.pickIdx {
			row = hiStyle.Render("› ") + row
		} else {
			row = "  " + row
		}
		lines = append(lines, fit(row, cw))
	}
	lines = append(lines, "", dimStyle.Render("◐ waits for you  ⠹ working  ● done, not seen  ○ seen"))
	return fitHeight(lines, h, false)
}
