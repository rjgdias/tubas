package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tubas/internal/engine"
	"tubas/internal/server"
)

func TestSlashAgentsOpensTheForm(t *testing.T) {
	m, _, _ := remoteModel(t, nil)
	m = feed(m, tea.WindowSizeMsg{Width: 110, Height: 34})
	t.Cleanup(m.Stop)
	// Some terminals also send ctrl+g as the text "\a"; an invisible
	// character must not turn the command into a question.
	m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\a'}})
	m = m.PressKeys("/agents", "enter")
	if m.section != SecAgents || m.form == nil || m.running {
		t.Fatalf("/agents opens the new agent form: section %v, form %v, running %v", m.section, m.form != nil, m.running)
	}
}

func TestGatewayListsOnlyMappedShortNames(t *testing.T) {
	m, _, _ := remoteModel(t, nil)
	t.Cleanup(m.Stop)
	m.cfg.Providers = map[string][]string{"anthropic": {"acme-claude-sonnet", "acme-llama-70b"}}
	m.onGateway, m.aliases = true, map[string]string{"sonnet": "acme-claude-sonnet"}
	got := m.modelsFor("claude", "anthropic", "")
	want := []string{"sonnet", "acme-claude-sonnet", "acme-llama-70b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("on a gateway, only the short names your settings map: got %v, want %v", got, want)
	}
	m.onGateway = false
	if got := m.modelsFor("claude", "anthropic", ""); !slices.Contains(got, "opus") {
		t.Errorf("on Anthropic itself, every short name: %v", got)
	}
}

func TestAFailingProviderShowsOnScreen(t *testing.T) {
	s := Session{Question: "oi", StartedAt: time.Now()}
	s = s.apply(engine.TurnStarted{Agent: "leo", From: "you"})
	s = s.apply(engine.Retrying{Agent: "leo", Attempt: 6, Max: 10, Status: 500})
	if got := s.Turns[0].Retry; got != "the provider failed (500) · trying again, 6 of 10" {
		t.Errorf("a retry shows on the turn, got %q", got)
	}
	long := "leo: claude: API Error: 500 upstream failed. This is a server-side issue, usually temporary, try again in a moment. If it persists, check your inference gateway."
	s = s.apply(engine.Failed{Agent: "leo", Err: errors.New(long[5:])})
	if tr := s.Turns[0]; tr.Status != Stopped || tr.Retry != "" {
		t.Errorf("a failed session's turn is no longer working: %+v", tr)
	}
	text := ansi.Strip(strings.Join(outcomeLines(s, 60), " "))
	for _, want := range []string{"failed", "check your inference gateway"} {
		if !strings.Contains(text, want) {
			t.Errorf("the whole reason shows, wrapped: %q missing in %q", want, text)
		}
	}
}

func TestTheCommandMenu(t *testing.T) {
	m, _, _ := remoteModel(t, nil)
	m = feed(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	t.Cleanup(m.Stop)
	m = m.PressKeys("/")
	names := map[string]bool{}
	for _, c := range m.commandCandidates() {
		names[c.Name] = true
	}
	for _, want := range []string{"new", "conversations", "retry", "copy", "export", "model", "usage", "doctor", "mcp", "skills", "help", "quit"} {
		if !names[want] {
			t.Errorf("/ lists /%s", want)
		}
	}
	frame := ansi.Strip(m.Snapshot(120, 40))
	if !strings.Contains(frame, "↓") || !strings.Contains(frame, "more") {
		t.Errorf("a long list scrolls, with a count of what is below:\n%s", frame)
	}
	// Aliases and words in the description find a command too.
	m = m.PressKeys("backspace", "/", "c", "l", "e")
	if c := m.commandCandidates(); len(c) == 0 || c[0].Name != "new" {
		t.Errorf("/cle finds /new by its alias /clear: %v", c)
	}
	// /model with a name sets it at once.
	m = m.PressKeys("ctrl+u", "/model haiku", "enter")
	if a := m.agentByName(m.pickerAgent()); a == nil || a.Model != "haiku" || m.modelPick != nil {
		t.Errorf("/model haiku sets the model without the picker: %+v", a)
	}
	// /export writes the conversation as Markdown.
	m.cfg.WorkDir = t.TempDir()
	m = m.export("")
	if !strings.Contains(m.notice, "nothing to export") {
		t.Errorf("an empty conversation has nothing to export: %q", m.notice)
	}
}

func TestDeletingConversationsFromTheList(t *testing.T) {
	scripts := map[string]engine.Script{"leo": {Summary: "feito"}}
	m, _, c := remoteModel(t, scripts)
	m = feed(m, tea.WindowSizeMsg{Width: 140, Height: 32})
	t.Cleanup(m.Stop)
	var ids []string
	for _, q := range []string{"@leo primeira", "@leo segunda"} {
		info, err := c.Start(server.StartRequest{Target: "leo", Question: q})
		if err != nil {
			t.Fatal(err)
		}
		c.Wait(info.ID, []string{server.Done}, 5*time.Second)
		ids = append(ids, info.ID)
	}
	m = feed(m, status(t, c))
	m = m.PressKeys("ctrl+g")
	if !m.picker || len(m.conversations()) != 2 {
		t.Fatalf("ctrl+g lists two conversations: %d", len(m.conversations()))
	}
	m = m.PressKeys("d") // the newest, first in the list
	if m.deleting == "" || !strings.Contains(ansi.Strip(m.notice), "delete") {
		t.Fatalf("d asks first: %q", m.notice)
	}
	m = m.PressKeys("n") // no: it stays
	if m.deleting != "" || !m.picker {
		t.Fatal("any other key keeps it, and the list stays open")
	}
	// d then y: the delete runs (here by hand: PressKeys drops commands).
	m = m.PressKeys("d")
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("y deletes")
	}
	m = feed(next.(Model), cmd())
	m = feed(m, status(t, c))
	if strings.Contains(m.notice, "could not") {
		t.Fatal(m.notice)
	}
	next = m
	if got := next.(Model).conversations(); len(got) != 1 || got[0].ID != ids[0] {
		t.Errorf("one conversation is left: %+v", got)
	}
}

func TestPlainChatSidebarHasNoWorkspaceTree(t *testing.T) {
	m, _ := paneModelSized(t, 140, 30, nil)
	m, _ = m.panesLoaded(panesLoadedMsg{})
	if !m.onlyChat() {
		t.Fatal("no terminal open: only the chat")
	}
	lines, _ := m.sidebarLines(sidebarW, 20)
	text := ansi.Strip(strings.Join(lines, "\n"))
	if strings.Contains(text, "1 chat") || strings.Contains(text, "new tab") {
		t.Errorf("just chatting, the sidebar has no workspace tree:\n%s", text)
	}
	m = openPane(t, m, "v")
	if m.onlyChat() {
		t.Error("with a terminal open, the tree is back")
	}
	m = m.PressKeys("ctrl+b", "X")
	if !strings.Contains(ansi.Strip(m.notice), "team's workspace stays") {
		t.Errorf("the team's workspace can't be closed: %q", m.notice)
	}
}
