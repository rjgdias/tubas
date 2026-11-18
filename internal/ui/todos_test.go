package ui

import (
	"strings"
	"testing"

	"tubas/internal/engine"
	"tubas/internal/tools"
)

func TestTheChecklistShowsWhileTheAgentWorks(t *testing.T) {
	s := Session{Question: "q"}.apply(engine.TurnStarted{Agent: "dev", From: "you"})
	s = s.apply(engine.Todos{Agent: "dev", Items: []tools.Todo{{Content: "read the code", Status: "completed"}, {Content: "fix it", Status: "in_progress"}, {Content: "run tests", Status: "pending"}}})
	lines := strings.Join(todoLines(s.Turns[0].Todos, 60), "\n")
	for _, want := range []string{"☑ read the code", "◐ fix it", "☐ run tests"} {
		if !strings.Contains(lines, want) {
			t.Errorf("missing %q in\n%s", want, lines)
		}
	}
	var m Model
	body := strings.Join(m.turnBody(s.Turns[0], 60, false), "\n")
	if !strings.Contains(body, "◐ fix it") {
		t.Errorf("a live turn shows its checklist:\n%s", body)
	}
	s = s.apply(engine.TurnFinished{Agent: "dev", Summary: "fixed"})
	if body := strings.Join(m.turnBody(s.Turns[0], 60, false), "\n"); strings.Contains(body, "fix it") {
		t.Errorf("a finished, closed turn hides it:\n%s", body)
	}
}
