package ui

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tubas/internal/agent"
	"tubas/internal/engine"
	"tubas/internal/server"
	"tubas/internal/team"
)

// ── a user, with a mouse ────────────────────────────────────────────────
//
// These tests use tubas the way a person does: they look at the screen,
// find the words to click ("Rename", "/model changes the model", "esc
// cancel") and click them, then look again. What is clicked is what is
// drawn, so a hint that moves or changes is clicked where it now is.

// personalModel is tubas as a new user gets it: one assistant, the
// background server, panes on, a wide window.
func personalModel(t *testing.T) (Model, *server.Client) {
	t.Helper()
	dir := t.TempDir()
	if err := team.Init(dir); err != nil {
		t.Fatal(err)
	}
	tm, err := team.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	scripts := map[string]engine.Script{"tubas": {Summary: "Oi! Como posso ajudar?"}}
	s := server.New(server.Options{TeamDir: dir, Engine: engine.FakeEngine{Scripts: scripts}, Router: engine.RuleRouter{},
		Load: func() (team.Team, error) { return team.Load(dir) }})
	s.SetToken("t")
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.Close(); hs.Close() })
	c := &server.Client{URL: hs.URL, Token: "t"}
	cfg := Config{Name: tm.Name, Lead: tm.Lead, MaxHops: tm.MaxHops, TeamDir: dir, Remote: c,
		Connectors: tm.ConnectorNames(), WorkDir: t.TempDir()}
	cfg.Panes = &fakeHost{}
	m := New(tm.Agents, engine.FakeEngine{}, cfg)
	m = feed(m, tea.WindowSizeMsg{Width: 130, Height: 34})
	m, _ = m.panesLoaded(panesLoadedMsg{})
	t.Cleanup(m.Stop)
	return m, c
}

// where finds text on the screen: the column of its first letter, its row.
func where(m Model, text string) (x, y int, ok bool) {
	for y, line := range strings.Split(m.View(), "\n") {
		plain := ansi.Strip(line)
		if i := strings.Index(plain, text); i >= 0 {
			return utf8.RuneCountInString(plain[:i]), y, true
		}
	}
	return 0, 0, false
}

// click clicks the middle of text on the screen, and lets whatever it
// started finish (a call to the server, say).
func click(t *testing.T, m Model, text string) Model {
	t.Helper()
	x, y, ok := where(m, text)
	if !ok {
		t.Fatalf("%q is not on the screen:\n%s", text, ansi.Strip(m.View()))
	}
	x += utf8.RuneCountInString(text) / 2
	next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = settle(next.(Model), cmd)
	next, cmd = m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	return settle(next.(Model), cmd)
}

// rightClick right-clicks text on the screen.
func rightClick(t *testing.T, m Model, text string) Model {
	t.Helper()
	x, y, ok := where(m, text)
	if !ok {
		t.Fatalf("%q is not on the screen:\n%s", text, ansi.Strip(m.View()))
	}
	next, cmd := m.Update(tea.MouseMsg{X: x + 1, Y: y, Button: tea.MouseButtonRight, Action: tea.MouseActionPress})
	return settle(next.(Model), cmd)
}

// settle runs a command the way Bubble Tea would, feeding back what it
// returns; one that waits (a tick, the next event) is let go after a
// moment, as the screen would just carry on.
func settle(m Model, cmd tea.Cmd) Model { return settleN(m, cmd, 0) }

// settleN follows what commands return, a few steps deep: a terminal's
// screen asks for the next one at once with the fake host, forever.
func settleN(m Model, cmd tea.Cmd, depth int) Model {
	if cmd == nil || depth > 12 {
		return m
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				m = settleN(m, c, depth+1)
			}
			return m
		}
		if msg == nil {
			return m
		}
		next, more := m.Update(msg)
		return settleN(next.(Model), more, depth+1)
	case <-time.After(300 * time.Millisecond):
		return m
	}
}

func onScreen(t *testing.T, m Model, texts ...string) {
	t.Helper()
	screen := ansi.Strip(m.View())
	for _, text := range texts {
		if !strings.Contains(screen, text) {
			t.Fatalf("%q should be on the screen:\n%s", text, screen)
		}
	}
}

func TestAUserClicksThroughTubas(t *testing.T) {
	m, c := personalModel(t)

	// The start screen's suggestions are buttons: /model opens the list,
	// a click on a model picks it.
	onScreen(t, m, "/model changes the model", "? keys")
	m = click(t, m, "/model changes the model")
	if m.modelPick == nil {
		t.Fatal("clicking /model opens the model list")
	}
	m = click(t, m, "haiku")
	if a := m.agentByName("tubas"); a == nil || a.Model != "haiku" || m.modelPick != nil {
		t.Fatalf("clicking haiku picks it: %+v", a)
	}
	// The model in the header opens the list too; esc closes it.
	m = click(t, m, "haiku")
	if m.modelPick == nil {
		t.Fatal("clicking the model in the header opens the list")
	}
	m = click(t, m, "esc closes")
	if m.modelPick != nil {
		t.Fatal("clicking esc closes closes it")
	}

	// The notice goes after a moment, and the hints are back.
	m = feed(m, noticeExpiredMsg(m.notice))
	// "? keys" opens the keys panel; a click anywhere closes it.
	m = click(t, m, "? keys")
	if !m.showHelp {
		t.Fatal("clicking ? keys opens the keys panel")
	}
	m = click(t, m, "talk")
	if m.showHelp {
		t.Fatal("a click closes the keys panel")
	}

	// "/ commands" opens the command menu; a click on one runs it.
	m = click(t, m, "/ commands")
	onScreen(t, m, "commands · ")
	m = m.PressKeys("u") // a letter narrows the list
	m = click(t, m, "/usage")
	onScreen(t, m, "nothing used yet")
	onScreen(t, m, "/ commands", "? keys") // a notice never hides the buttons

	// Two conversations: the sidebar lists them.
	for _, q := range []string{"primeira pergunta", "segunda pergunta"} {
		info, err := c.Start(server.StartRequest{Target: "tubas", Question: q})
		if err != nil {
			t.Fatal(err)
		}
		c.Wait(info.ID, []string{server.Done}, 5*time.Second)
	}
	m = feed(m, status(t, c))
	onScreen(t, m, "conversations", "primeira pergunta", "segunda pergunta")

	// A click on one shows it in the chat.
	m = click(t, m, "primeira pergunta")
	onScreen(t, m, "Oi! Como posso ajudar?")

	// Right click: rename it.
	m = rightClick(t, m, "segunda pergunta")
	m = click(t, m, "Rename")
	if m.menu == nil || !m.menu.renaming {
		t.Fatal("clicking Rename turns the menu into a text field")
	}
	m = m.PressKeys("ctrl+u", "minha", "space", "conversa")
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = settle(next.(Model), cmd)
	m = feed(m, status(t, c))
	onScreen(t, m, "minha conversa")

	// The conversation list: from the top bar, "d deletes" asks, the
	// notice's "y deletes it for good" deletes.
	m = click(t, m, "ctrl+g")
	if !m.picker {
		t.Fatal("clicking ctrl+g in the top bar opens the conversation list")
	}
	m = click(t, m, "minha conversa")
	m = click(t, m, "ctrl+g")
	m = m.PressKeys("down")
	before := len(m.conversations())
	m = click(t, m, "d deletes")
	onScreen(t, m, "y deletes it for good")
	m = click(t, m, "y deletes it for good")
	m = feed(m, status(t, c))
	if len(m.conversations()) != before-1 {
		t.Fatalf("clicking y deletes: %d of %d left", len(m.conversations()), before)
	}

	// Sections: a click opens one, a click on a row selects it, a second
	// click opens it; in the form, a click picks an option; esc cancel.
	m = click(t, m, "3 agents")
	if m.section != SecAgents {
		t.Fatal("clicking 3 agents opens Agents")
	}
	m = click(t, m, "tubas  assistant") // already selected: a click opens it
	if m.form == nil {
		t.Fatal("a click on the selected agent opens its form")
	}
	m = click(t, m, "green")
	if m.form.get("color") != "green" {
		t.Fatalf("clicking an option picks it: %q", m.form.get("color"))
	}
	m = click(t, m, "esc cancel")
	if m.form != nil {
		t.Fatal("clicking esc cancel closes the form")
	}
	m = click(t, m, "1 chat")
	if m.section != SecChat {
		t.Fatal("clicking 1 chat goes back")
	}

	// "ctrl+n new" in the hints starts a new conversation.
	m = click(t, m, "ctrl+n new")
	onScreen(t, m, "ask anything")
}

func TestAUserAddsAnMCPServerByClicking(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	m, _ := personalModel(t)
	exe, _ := os.Executable()
	state := `{"mcpServers": {"notes": {"command": ` + jsonString(exe) + `, "args": ["-test.run=^$"]}}}`
	os.WriteFile(filepath.Join(cfgDir, ".claude.json"), []byte(state), 0o600)

	m = m.PressKeys("/", "m", "c") // a few letters filter the commands
	m = click(t, m, "/mcp")
	onScreen(t, m, "MCP servers", "from your Claude Code", "notes")
	m = click(t, m, "notes") // select it
	m = click(t, m, "notes") // and a second click adds it
	if a := m.agentByName("tubas"); a == nil || a.Tools["notes"] != "write" {
		t.Fatalf("clicking a server twice adds it and gives the assistant access: %+v", a)
	}
	onScreen(t, m, "in tubas", "from Claude Code: notes")
	m = click(t, m, "esc closes")
	if m.mcp != nil {
		t.Fatal("clicking esc closes closes /mcp")
	}
	_ = agent.Agent{}
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}

func TestAUserClicksEverySection(t *testing.T) {
	m, _ := personalModel(t)

	m = click(t, m, "2 inbox")
	m = click(t, m, "1 back to the chat")
	if m.section != SecChat {
		t.Fatal("clicking 1 back to the chat goes back")
	}

	// Agents: n new opens the form, esc cancel closes it; p permissions
	// and s skills open their screens; in skills, n new and esc back.
	m = click(t, m, "3 agents")
	m = click(t, m, "n new")
	if m.form == nil {
		t.Fatal("clicking n new opens the new agent form")
	}
	m = click(t, m, "reviewer") // a template, clicked
	if m.form.get("role") != "reviewer" {
		t.Fatalf("clicking a template fills the form: role %q", m.form.get("role"))
	}
	m = click(t, m, "esc cancel")
	if m.form != nil {
		t.Fatal("esc cancel closes it")
	}
	m = click(t, m, "p permissions")
	if !m.permMode {
		t.Fatal("clicking p permissions opens the grid")
	}
	m = m.PressKeys("esc")
	m = click(t, m, "s skills")
	if !m.skillsMode {
		t.Fatal("clicking s skills opens the skills")
	}
	m = click(t, m, "n new")
	if m.form == nil {
		t.Fatal("clicking n new in skills opens the skill form")
	}
	m = click(t, m, "esc cancel")
	m = click(t, m, "esc back")
	if m.skillsMode {
		t.Fatal("clicking esc back leaves the skills")
	}

	// Routines: n new, then esc cancel.
	m = click(t, m, "4 routines")
	m = click(t, m, "n new")
	if m.form == nil {
		t.Fatal("clicking n new opens the routine form")
	}
	m = click(t, m, "esc cancel")

	// Files: r read again. Settings: e edit, a choice clicked, esc cancel.
	m = click(t, m, "6 files")
	m = click(t, m, "r read again")
	m = click(t, m, "7 settings")
	m = click(t, m, "e edit")
	if m.form == nil {
		t.Fatal("clicking e edit opens the settings form")
	}
	m = click(t, m, "only in the window")
	if m.form.get("notify") != "window" {
		t.Fatalf("clicking window picks it: %q", m.form.get("notify"))
	}
	// Selected, its hint never hides an option: you see what is chosen.
	onScreen(t, m, "in the window, and the desktop when it is closed", "only in the window", "off")
	m = click(t, m, "esc cancel")
	m = click(t, m, "esc chat")
	if m.section != SecChat {
		t.Fatal("clicking esc chat goes back to the chat")
	}
}

func TestAUserClicksTerminals(t *testing.T) {
	m, _ := personalModel(t)
	host := m.cfg.Panes.(*fakeHost)

	// /terminal opens one next to the chat; the sidebar's tree is back.
	m = m.PressKeys("/", "t", "e", "r")
	m = click(t, m, "/terminal")
	if len(m.panes.cur().Root.leaves()) != 2 {
		t.Fatalf("clicking /terminal opens a terminal next to the chat: %v", m.panes.cur().Root.leaves())
	}
	onScreen(t, m, "+ new tab", "right click: menu")

	// + new tab opens the new pane form; ctrl+s save opens it.
	m = click(t, m, "+ new tab")
	if m.form == nil {
		t.Fatal("clicking + new tab opens the new pane form")
	}
	m = click(t, m, "ctrl+s save")
	if m.form != nil || len(m.panes.space().Tabs) != 2 {
		t.Fatalf("clicking ctrl+s save opens it in a new tab: %d tabs", len(m.panes.space().Tabs))
	}

	// Right click on the new tab: Close, then Yes, close.
	tabName := m.panes.space().Tabs[1].Name
	m = rightClick(t, m, "2 "+tabName)
	m = click(t, m, "Close")
	m = click(t, m, "Yes, close")
	if len(m.panes.space().Tabs) != 1 {
		t.Fatalf("closed from the menu: %d tabs", len(m.panes.space().Tabs))
	}
	if len(host.closed) == 0 {
		t.Error("and its terminal was stopped")
	}
}
