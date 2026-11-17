package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"tubas/internal/agent"
	"tubas/internal/engine"
)

const demo = "@iris por que os pedidos caíram em março?"

// newModel builds the model with an instant fake engine. Nothing runs yet.
func newModel(t *testing.T, scripts map[string]engine.Script) Model {
	t.Helper()
	team, err := agent.LoadTeam(filepath.Join("..", "..", "examples", "dados", "agents"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Name: "dados", Example: demo, MaxHops: 6, Connectors: []string{"data", "reports"}}
	return New(team, engine.FakeEngine{Scripts: scripts}, cfg) // Delay 0: instant
}

// asked sends the demo question and runs until nico asks for approval.
func asked(t *testing.T) Model {
	t.Helper()
	m := newModel(t, engine.DemoScripts()).Ask(demo).Settle(0)
	t.Cleanup(m.Stop) // runs when the test ends, even if it fails
	return m
}

func contains(t *testing.T, frame string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(frame, w) {
			t.Errorf("frame should contain %q", w)
		}
	}
}

// Every screen must fill the terminal exactly: not one line or column more.
func TestEveryScreenFitsTheTerminal(t *testing.T) {
	screens := map[string]Model{
		"start":    newModel(t, engine.DemoScripts()),
		"mention":  newModel(t, engine.DemoScripts()).PressKeys("@"),
		"approval": asked(t),
		"details":  asked(t).PressKeys("v"),
		"expanded": asked(t).PressKeys("up", "up", "up", "enter"),
		"grid":     asked(t).PressKeys("tab"),
		"help":     asked(t).PressKeys("?"),
		"done":     asked(t).PressKeys("y").Settle(0),
	}
	for s := SecInbox; s < numSections; s++ {
		screens["section "+s.String()] = asked(t).PressKeys(fmt.Sprintf("alt+%d", s+1))
	}
	screens["empty inbox"] = newModel(t, engine.DemoScripts()).PressKeys("alt+2")
	for name, m := range screens {
		for _, size := range [][2]int{{60, 18}, {80, 24}, {128, 32}, {200, 60}} {
			w, h := size[0], size[1]
			lines := strings.Split(m.Snapshot(w, h), "\n")
			if len(lines) != h {
				t.Errorf("%s %dx%d: got %d lines", name, w, h, len(lines))
			}
			for i, line := range lines {
				if lw := lipgloss.Width(line); lw > w {
					t.Errorf("%s %dx%d: line %d is %d wide", name, w, h, i, lw)
				}
			}
		}
	}
}

func TestTinyTerminalShowsMessage(t *testing.T) {
	m := newModel(t, engine.DemoScripts())
	contains(t, m.Snapshot(52, 14), "at least", "52×14")
}

func TestStartScreenShowsTheTeam(t *testing.T) {
	m := newModel(t, engine.DemoScripts())
	contains(t, m.Snapshot(128, 32), "no session yet", "investigator", "reports: write", "calls")
}

func TestWhoGetsTheQuestion(t *testing.T) {
	m := newModel(t, engine.DemoScripts())
	cases := []struct {
		text string
		want request
	}{
		{"por que caiu?", request{question: "por que caiu?"}},                                 // no mention: routing decides
		{"@leo a pipeline está ok?", request{target: "leo", question: "a pipeline está ok?"}}, // mention first
		{"a pipeline está ok, @leo?", request{target: "leo", question: "a pipeline está ok, @leo?"}},
		{"@todos o que vocês acham?", request{question: "o que vocês acham?", everyone: true}},
		{"o que acham, @todos?", request{question: "o que acham, @todos?", everyone: true}},
		{"quanto caiu no sul?", request{question: "quanto caiu no sul?"}}, // starts with q: still text
	}
	for _, c := range cases {
		got, err := m.parseAsk(c.text)
		if err != nil || got != c.want {
			t.Errorf("parseAsk(%q) = %+v, %v", c.text, got, err)
		}
	}
	for text, want := range map[string]string{
		"@zeca oi": "no agent named @zeca",
		"@iris":    "write a question",
		"   ":      "type a question",
	} {
		if got := m.Ask(text); got.running || !strings.Contains(got.notice, want) {
			t.Errorf("Ask(%q) notice = %q, want %q", text, got.notice, want)
		}
	}
}

// With no @ required, letters must never act as commands on their own:
// a question can start with q, r or any other word.
func TestLettersStartAQuestion(t *testing.T) {
	m := newModel(t, engine.DemoScripts()).PressKeys("q", "u", "a", "n", "t", "o")
	if got := string(m.input); got != "quanto" {
		t.Errorf("typed %q", got)
	}
}

func TestMentionMenuCompletes(t *testing.T) {
	m := newModel(t, engine.DemoScripts()).PressKeys("@")
	contains(t, m.Snapshot(128, 32), "mention an agent", "engineer")

	m = m.PressKeys("down", "tab")
	if got := string(m.input); got != "@leo " {
		t.Errorf("down + tab should complete @leo, got %q", got)
	}
}

func TestLettersAreTextWhileTyping(t *testing.T) {
	m := newModel(t, engine.DemoScripts()).PressKeys("@", "esc", "q") // esc closes the menu
	if got := string(m.input); got != "@q" {
		t.Errorf("q should be typed, not quit; input is %q", got)
	}
}

func TestRunsUntilNicoAsksForApproval(t *testing.T) {
	m := asked(t)
	if n := len(m.session.Turns); n != 3 {
		t.Fatalf("expected 3 turns, got %d", n)
	}
	if m.session.Pending == nil || m.session.Pending.Agent != "nico" {
		t.Fatalf("expected nico waiting for approval, got %+v", m.session.Pending)
	}
	contains(t, m.Snapshot(128, 32), "nico wants to run", "waiting for you")
}

func TestSelectAndExpandATurn(t *testing.T) {
	m := asked(t)
	irisStep := "cupons sem mudança"
	if strings.Contains(m.Snapshot(128, 40), irisStep) {
		t.Fatal("iris's steps should start collapsed")
	}
	m = m.PressKeys("up", "up", "up", "enter") // latest turn, then up to iris
	if m.selected != 0 {
		t.Fatalf("expected iris (turn 0) selected, got %d", m.selected)
	}
	contains(t, m.Snapshot(128, 40), irisStep, "enter collapse")
}

func TestDetailsShowWhyItAsked(t *testing.T) {
	contains(t, asked(t).PressKeys("v").Snapshot(128, 32), "why asked", "reports: write", "agents/nico.md")
}

func TestApproveFinishesTheSession(t *testing.T) {
	m := asked(t).PressKeys("y").Settle(0)
	frame := m.Snapshot(128, 32)
	if strings.Contains(frame, "wants to run") {
		t.Error("approval bar should be gone after y")
	}
	contains(t, frame, "relatório publicado", "approved by you", "done")
	if m.running {
		t.Error("the session should have ended")
	}
}

func TestDenyIsShownAsFailure(t *testing.T) {
	contains(t, asked(t).PressKeys("n").Settle(0).Snapshot(128, 32), "recusada", "denied by you")
}

func TestGridShowsEveryAgent(t *testing.T) {
	contains(t, asked(t).PressKeys("tab").Snapshot(128, 32), "· grid", "1 iris", "3 nico", "from you")
}

func TestHelpOpensAndCloses(t *testing.T) {
	m := asked(t).PressKeys("?")
	contains(t, m.Snapshot(128, 32), "esc close", "grid of all agents")
	if m.PressKeys("esc").showHelp {
		t.Error("esc should close the keys panel")
	}
}

func TestEscStopsTheSession(t *testing.T) {
	m := asked(t).PressKeys("esc").Settle(0)
	contains(t, m.Snapshot(128, 32), "stopped", "by you")
	if m.running {
		t.Error("esc should stop the run")
	}
}

func TestHopLimitIsShown(t *testing.T) {
	pingPong := map[string]engine.Script{
		"iris": {Summary: "pode olhar de novo?", Handoff: "leo", HandoffNote: "confere?"},
		"leo":  {Summary: "tudo certo, volte.", Handoff: "iris", HandoffNote: "refaça"},
	}
	m := newModel(t, pingPong).Ask("@iris o ticket médio mudou?").Settle(0)
	t.Cleanup(m.Stop)
	contains(t, m.Snapshot(128, 32), "hop limit of 6 reached", "6/6 hops", "what you can do")
}

// Update must not change the old model: that is the Elm architecture.
func TestUpdateDoesNotChangeOldModel(t *testing.T) {
	before := asked(t)
	_ = before.PressKeys("y")
	if before.session.Pending == nil {
		t.Error("the old model lost its pending approval")
	}
	if st := before.session.Turns[2].Status; st != Waiting {
		t.Errorf("the old model's turn changed to %v", st)
	}
}

// After a restart, events from the previous run must be ignored.
func TestEventsFromOldRunAreIgnored(t *testing.T) {
	m := asked(t).PressKeys("esc").Settle(0)
	oldRun := m.run
	m = m.PressKeys("ctrl+r") // run again: a fresh, empty session
	t.Cleanup(m.Stop)

	next, _ := m.Update(eventMsg{oldRun, engine.TurnStarted{Agent: "leo"}})
	for _, turn := range next.(Model).session.Turns {
		if turn.Agent == "leo" && turn.Status == Live && len(next.(Model).session.Turns) == 1 {
			t.Errorf("an old event changed the new session")
		}
	}
}
