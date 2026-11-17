package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tubas/internal/engine"
)

func TestAltNumberOpensASection(t *testing.T) {
	m := newModel(t, engine.DemoScripts())
	for s := SecChat; s < numSections; s++ {
		m = m.PressKeys("alt+" + string(rune('1'+s)))
		if m.section != s {
			t.Fatalf("alt+%d opened %s", s+1, m.section)
		}
	}
	contains(t, m.Snapshot(128, 32), " 7 settings ", "alt+1…7")
}

// In the chat a digit is text; outside it, there is nothing to type, so
// digits, tab and esc move between sections.
func TestDigitsAreTextOnlyInTheChat(t *testing.T) {
	m := newModel(t, engine.DemoScripts()).PressKeys("3")
	if m.section != SecChat || string(m.input) != "3" {
		t.Fatalf("in the chat, 3 should be typed: section %s, input %q", m.section, string(m.input))
	}
	m = newModel(t, engine.DemoScripts()).PressKeys("alt+3", "5")
	if m.section != SecConnectors {
		t.Fatalf("outside the chat, 5 should open connectors, got %s", m.section)
	}
	if m = m.PressKeys("tab"); m.section != SecFiles {
		t.Fatalf("tab should go to the next section, got %s", m.section)
	}
	if m = m.PressKeys("esc"); m.section != SecChat {
		t.Fatalf("esc should go back to the chat, got %s", m.section)
	}
	if len(m.input) != 0 {
		t.Fatalf("keys pressed in sections should not be typed: %q", string(m.input))
	}
}

func TestInboxShowsWhatWaits(t *testing.T) {
	m := asked(t)
	t.Cleanup(m.Stop)
	frame := m.PressKeys("alt+2").Snapshot(128, 32)
	contains(t, frame, " 2 inbox ", "1 waiting", "nico", "publish_report", "y  approve")

	empty := newModel(t, engine.DemoScripts()).PressKeys("alt+2").Snapshot(128, 32)
	contains(t, empty, "Nothing waits for you.")
}

// Two agents in parallel both ask: the inbox can answer the second one
// first, and the first stays waiting, in the chat too.
func TestInboxAnswersInAnyOrder(t *testing.T) {
	m := newModel(t, parallelScripts(true)).Ask("@iris por quê?").Settle(0)
	t.Cleanup(m.Stop)
	for len(m.queue) < 2 { // Settle stops at the first; wait for the second worker too
		select {
		case e, ok := <-m.events:
			if !ok {
				t.Fatal("the session ended before both workers asked")
			}
			m = m.handle(e)
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d approval(s) arrived", len(m.queue))
		}
	}
	if len(m.queue) != 2 {
		t.Fatalf("both workers should be waiting, got %d", len(m.queue))
	}
	first, second := m.queue[0].agent, m.queue[1].agent

	m = m.PressKeys("alt+2", "down", "n")
	if len(m.queue) != 1 || m.queue[0].agent != first {
		t.Fatalf("answering the second should leave the first: %+v", m.queue)
	}
	if m.session.Pending == nil || m.session.Pending.Agent != first {
		t.Fatalf("the chat should now show %s's approval, got %+v", first, m.session.Pending)
	}
	m = m.PressKeys("y").Settle(0)
	for i := 0; m.running && i < 20; i++ {
		m = m.Settle(0)
	}
	if m.running {
		t.Fatal("the session should finish once both are answered")
	}
	notes := map[string]string{}
	for _, turn := range m.session.Turns {
		for _, st := range turn.Steps {
			if st.Note != "" {
				notes[turn.Agent] = st.Note
			}
		}
	}
	if !strings.HasPrefix(notes[second], "denied") || !strings.HasPrefix(notes[first], "approved") {
		t.Errorf("each answer should reach its own agent: %v", notes)
	}
}

func TestAgentsSectionShowsTheSelectedAgent(t *testing.T) {
	m := newModel(t, engine.DemoScripts()).PressKeys("alt+3", "down")
	frame := m.Snapshot(128, 32)
	contains(t, frame, "3 agents", "model", "can call", "instructions", "agents/leo.md")
}

func TestFilesSectionListsWhatAgentsWrote(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, ".tubas", "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "notes.md"), []byte("# Notas\nqueda no sul\x1b[31m"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newModel(t, engine.DemoScripts())
	m.cfg.TeamDir = dir
	frame := m.PressKeys("alt+6").Snapshot(128, 32)
	contains(t, frame, "1 files", "shared/notes.md", "# Notas", "queda no sul")
	if strings.Contains(frame, "\x1b[31m") {
		t.Error("escape codes from a file must not reach the screen")
	}
}
