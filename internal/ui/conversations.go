package ui

import (
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tubas/internal/engine"
	"tubas/internal/server"
)

// ── conversations ───────────────────────────────────────────────────────
//
// On the server every question is a session; a follow-up is a new
// session of the same conversation. The screen shows conversations: one
// row each in the sidebar and in ctrl+g, titled by their first question,
// with the state that matters most among their messages.

type conversation struct {
	ID       string
	Title    string
	State    string        // the most urgent of its sessions'
	Sessions []server.Info // oldest first
	Updated  time.Time
}

// latest is the conversation's newest message, the one to show.
func (c conversation) latest() server.Info { return c.Sessions[len(c.Sessions)-1] }

func convOf(s server.Info) string {
	if s.Conversation != "" {
		return s.Conversation
	}
	return s.ID
}

// conversations groups the server's chat sessions, newest first.
func (m Model) conversations() []conversation {
	if m.status == nil {
		return nil
	}
	rank := map[string]int{server.Blocked: 4, server.Working: 3, server.Done: 2, server.Idle: 1}
	byID := map[string]*conversation{}
	var order []*conversation
	for _, s := range m.status.Sessions {
		if s.Kind == "routine" {
			continue
		}
		id := convOf(s)
		c, ok := byID[id]
		if !ok {
			c = &conversation{ID: id}
			byID[id] = c
			order = append(order, c)
		}
		c.Sessions = append(c.Sessions, s)
		if rank[s.State] > rank[c.State] {
			c.State = s.State
		}
		if s.Updated.After(c.Updated) {
			c.Updated = s.Updated
		}
	}
	out := make([]conversation, 0, len(order))
	for _, c := range order {
		sort.Slice(c.Sessions, func(i, j int) bool { return c.Sessions[i].Created.Before(c.Sessions[j].Created) })
		c.Title = orDefault(c.Sessions[0].Title, "new conversation")
		out = append(out, *c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// conversationStates are the conversations' states, for the top bar.
func (m Model) conversationStates() []server.Info {
	var out []server.Info
	for _, c := range m.conversations() {
		out = append(out, server.Info{State: c.State})
	}
	return out
}

// attachConversation shows a conversation: its earlier messages as the
// chat's past, its newest one live.
func (m Model) attachConversation(c conversation) (Model, tea.Cmd) {
	var past []Session
	for _, s := range c.Sessions[:len(c.Sessions)-1] {
		past = append(past, m.replay(s))
	}
	m, cmd := m.attach(c.latest())
	m.past = past
	m.conversation = c.ID
	return m, cmd
}

// replay rebuilds a finished session from the server's events.
func (m Model) replay(info server.Info) Session {
	s := Session{Team: m.cfg.Name, AskedAt: info.Created.Format("15:04"), Target: info.Target,
		Question: info.Question, MaxHops: m.cfg.MaxHops, StartedAt: info.Created}
	events, _, err := m.cfg.Remote.Events(info.ID, 0, 0)
	if err != nil {
		return s
	}
	for _, w := range events {
		if w.Kind == "answered" {
			continue
		}
		if e := w.Decode(make(chan bool, 1)); e != nil {
			if _, ok := e.(engine.ApprovalRequested); ok {
				continue // long answered
			}
			s = s.apply(e)
		}
	}
	return s
}
