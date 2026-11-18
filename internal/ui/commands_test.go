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
	got := m.modelsFor("anthropic", "")
	want := []string{"sonnet", "acme-claude-sonnet", "acme-llama-70b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("on a gateway, only the short names your settings map: got %v, want %v", got, want)
	}
	m.onGateway = false
	if got := m.modelsFor("anthropic", ""); !slices.Contains(got, "opus") {
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
	// ctrl+b X on the team's workspace: it asks, closes the terminal, and
	// with only the chat left it leaves the sidebar.
	m = m.PressKeys("ctrl+b", "X")
	if !strings.Contains(ansi.Strip(m.notice), "from the sidebar") || !strings.Contains(ansi.Strip(m.notice), "the chat stays") {
		t.Fatalf("it asks first, saying the chat stays: %q", m.notice)
	}
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(Model)
	if !m.onlyChat() {
		t.Error("its terminal is closed: only the chat is left")
	}
	lines, _ = m.sidebarLines(sidebarW, 20)
	if text := ansi.Strip(strings.Join(lines, "\n")); strings.Contains(text, "1 chat") || strings.Contains(text, "new tab") {
		t.Errorf("and it is gone from the sidebar:\n%s", text)
	}
}

// rightClickAt presses the right button at a screen cell.
func rightClickAt(m Model, x, y int) Model {
	next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonRight, Action: tea.MouseActionPress})
	return next.(Model)
}

func TestRightClickRenamesAndDeletes(t *testing.T) {
	scripts := map[string]engine.Script{"leo": {Summary: "feito"}}
	m, _, c := remoteModel(t, scripts)
	host := &fakeHost{}
	m.cfg.Panes = host
	m.panes = newPaneState(host)
	m = feed(m, tea.WindowSizeMsg{Width: 140, Height: 32})
	m, _ = m.panesLoaded(panesLoadedMsg{})
	for _, q := range []string{"@leo um", "@leo dois"} {
		info, _ := c.Start(server.StartRequest{Target: "leo", Question: q})
		c.Wait(info.ID, []string{server.Done}, 5*time.Second)
	}
	m = feed(m, status(t, c))
	t.Cleanup(m.Stop)
	_, rows := m.sidebarLines(sidebarW, m.bodyHeight())
	row := -1
	for i, r := range rows {
		if r.session != "" && row < 0 {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("a conversation row in the sidebar")
	}
	// Right click: a menu with Rename and Delete, drawn where clicked.
	m = rightClickAt(m, bodyX+4, bodyY+row)
	if m.menu == nil || len(m.menu.items) != 2 || m.menu.items[0].action != "rename" {
		t.Fatalf("a right click opens the menu: %+v", m.menu)
	}
	frame := ansi.Strip(m.View())
	if !strings.Contains(frame, "Rename") || !strings.Contains(frame, "Delete") {
		t.Fatalf("the menu is on screen:\n%s", frame)
	}
	// Rename: the field starts with the name; type a new one, enter.
	id := m.menu.row.session
	m = m.PressKeys("enter", "ctrl+u")
	if !m.menu.renaming {
		t.Fatal("Rename turns the menu into a text field")
	}
	m = m.PressKeys("minha", "space", "conversa")
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = feed(next.(Model), cmd())
	m = feed(m, status(t, c))
	found := false
	for _, cv := range m.conversations() {
		if cv.ID == id && cv.Title == "minha conversa" {
			found = true
		}
	}
	if !found {
		t.Errorf("renamed on the server: %+v", m.conversations())
	}
	// Delete asks in the menu; "Yes, delete" deletes.
	m = rightClickAt(m, bodyX+4, bodyY+row)
	m = m.PressKeys("down", "enter")
	if m.menu == nil || m.menu.items[0].action != "confirm" {
		t.Fatalf("Delete asks first: %+v", m.menu)
	}
	m = m.PressKeys("up")
	next, cmd = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = feed(next.(Model), cmd())
	m = feed(m, status(t, c))
	if len(m.conversations()) != 1 {
		t.Errorf("deleted: %d conversations left", len(m.conversations()))
	}
	// A click outside closes a menu.
	m = rightClickAt(m, bodyX+4, bodyY+row)
	m, _ = clickAt(m, bodyX+80, bodyY+10)
	if m.menu != nil {
		t.Error("a click outside closes the menu")
	}
}

func TestMenuRenamesWorkspacesAndClosesTabs(t *testing.T) {
	m, host := paneModelSized(t, 140, 30, nil)
	m, _ = m.panesLoaded(panesLoadedMsg{})
	m = openPane(t, m, "c") // a new tab with a terminal
	ps := m.panes
	if len(ps.space().Tabs) != 2 {
		t.Fatalf("two tabs: %d", len(ps.space().Tabs))
	}
	// Rename the workspace.
	menu := m.menuFor(sideRow{ws: 0, tab: -1}, 2, 4)
	m, _ = m.menuAction(menu, "rename")
	m = m.PressKeys("ctrl+u", "trabalho", "enter")
	if ps.spaces[0].Name != "trabalho" || m.menu != nil {
		t.Errorf("renamed: %q", ps.spaces[0].Name)
	}
	// Close the terminal's tab: it goes, the chat's stays.
	menu = m.menuFor(sideRow{ws: 0, tab: 1}, 2, 6)
	m, _ = m.menuAction(menu, "delete")
	m, cmd := m.menuAction(m.menu, "confirm")
	runAll(cmd)
	if len(ps.spaces[0].Tabs) != 1 || !hasChat(ps.spaces[0]) || len(host.closed) != 1 {
		t.Errorf("the tab and its terminal are closed: %d tabs, closed %v", len(ps.spaces[0].Tabs), host.closed)
	}
}

// runAll runs a command and every command in a batch it returns.
func runAll(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runAll(c)
		}
	}
}
