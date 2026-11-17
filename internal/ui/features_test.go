package ui

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tubas/internal/agent"
	"tubas/internal/engine"
)

// parallelScripts: Iris splits the work between Leo and Nico, then
// concludes with their answers. withApprovals makes both workers ask.
func parallelScripts(withApprovals bool) map[string]engine.Script {
	ask := func(name string) *engine.FakeApproval {
		if !withApprovals {
			return nil
		}
		return &engine.FakeApproval{Call: "publish_report(" + name + ")", OnApprove: "publicado por " + name, OnDeny: "recusado"}
	}
	return map[string]engine.Script{
		"iris": {Summary: "vou dividir", Handoff: "leo", HandoffNote: "olha a pipeline",
			Also: []engine.FakeHandoff{{To: "nico", Note: "confere a soma"}},
			Then: &engine.Script{Summary: "conclusão: o sul caiu no jantar"}},
		"leo":  {Summary: "pipeline ok", Approval: ask("leo")},
		"nico": {Summary: "a soma bate", Approval: ask("nico")},
	}
}

func TestParallelTurnsOnScreen(t *testing.T) {
	m := newModel(t, parallelScripts(false)).Ask("@iris por quê?").Settle(0)
	t.Cleanup(m.Stop)
	frame := m.Snapshot(128, 40)
	contains(t, frame, "in parallel", "↩ answers from @leo and @nico", "conclusão: o sul caiu", "done")

	grid := m.PressKeys("tab").Snapshot(128, 40)
	contains(t, grid, "from iris", "↩ answers from")
}

// Two workers asking at the same time: the second waits in a queue and
// shows up after the first is answered. Nobody is left hanging.
func TestParallelApprovalsQueueUp(t *testing.T) {
	m := newModel(t, parallelScripts(true)).Ask("@iris por quê?").Settle(0)
	t.Cleanup(m.Stop)
	for i := 0; i < 4 && m.running; i++ {
		if m.session.Pending == nil {
			t.Fatalf("round %d: the session paused without a visible approval", i)
		}
		m = m.PressKeys("y").Settle(0)
	}
	if m.running {
		t.Fatal("the session should have finished after both approvals")
	}
	approved := 0
	for _, turn := range m.session.Turns {
		for _, s := range turn.Steps {
			if strings.Contains(s.Note, "approved by you") {
				approved++
			}
		}
	}
	if approved != 2 {
		t.Errorf("expected 2 approved steps, got %d", approved)
	}
}

// promptRecorder is an engine that answers "ok" and remembers prompts.
type promptRecorder struct {
	mu      sync.Mutex
	prompts []string
}

func (p *promptRecorder) RunTurn(_ context.Context, req engine.TurnRequest, _ chan<- engine.Event) (engine.Reply, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompts = append(p.prompts, req.Prompt)
	return engine.Reply{Text: "resposta " + req.Agent.Name}, nil
}

func TestFollowUpRemembersTheConversation(t *testing.T) {
	team, err := agent.LoadTeam(filepath.Join("..", "..", "examples", "dados", "agents"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &promptRecorder{}
	m := New(team, rec, Config{Name: "dados"}).Ask("@iris por que caiu em março?").Settle(0)
	m = m.Ask("@leo e em abril?").Settle(0)
	t.Cleanup(m.Stop)

	last := rec.prompts[len(rec.prompts)-1]
	for _, want := range []string{"Earlier in this conversation", "por que caiu em março?", "resposta iris", "e em abril?"} {
		if !strings.Contains(last, want) {
			t.Errorf("the follow-up prompt should contain %q:\n%s", want, last)
		}
	}
	if len(m.past) != 1 {
		t.Errorf("the first session should be in the past, got %d", len(m.past))
	}
	contains(t, m.Snapshot(128, 32), "por que caiu em março?", "e em abril?")
}

func TestHistorySavesAndResumes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "history", "conv.json")
	team, _ := agent.LoadTeam(filepath.Join("..", "..", "examples", "dados", "agents"))
	m := New(team, &promptRecorder{}, Config{Name: "dados", HistoryFile: file}).Ask("@iris oi?").Settle(0)
	t.Cleanup(m.Stop)
	if msg := m.save()(); msg != nil { // run the command right here
		t.Fatalf("save failed: %v", msg)
	}

	latest, err := LatestHistory(filepath.Dir(file))
	if err != nil || latest != file {
		t.Fatalf("latest = %q, %v", latest, err)
	}
	past, err := LoadHistory(latest)
	if err != nil || len(past) != 1 || past[0].Question != "oi?" {
		t.Fatalf("loaded %+v, %v", past, err)
	}

	resumed := New(team, &promptRecorder{}, Config{Name: "dados", Past: past})
	frame := resumed.Snapshot(128, 32)
	if strings.Contains(frame, "no session yet") && strings.Contains(frame, "start by mentioning") {
		t.Error("a resumed conversation should show its past, not the start screen")
	}
	contains(t, frame, "oi?")
}

// A question with no @ goes through the team's routing, and the screen
// says who was picked and why.
func TestRoutedQuestionShowsWhoWasPicked(t *testing.T) {
	team, _ := agent.LoadTeam(filepath.Join("..", "..", "examples", "dados", "agents"))
	cfg := Config{Name: "dados", Lead: "iris", Routing: "router", Router: engine.RuleRouter{}}
	m := New(team, &promptRecorder{}, cfg).Ask("leo, a carga de ontem rodou?").Settle(0)
	t.Cleanup(m.Stop)
	if m.session.Route == nil || m.session.Route.Agents[0] != "leo" {
		t.Fatalf("route = %+v", m.session.Route)
	}
	contains(t, m.Snapshot(128, 32), "→ router: @leo", "resposta leo")
}

// @todos: everyone answers, a pass shows as one quiet line, and the lead
// closes the round.
func TestEveryoneRoundOnScreen(t *testing.T) {
	scripts := map[string]engine.Script{
		"iris": {Summary: "queda no sul", Then: &engine.Script{Summary: "conclusão do time"}},
		"leo":  {Summary: "pipeline ok"},
		"nico": {Summary: "PASS"},
	}
	m := newModel(t, scripts).Ask("@todos o que vocês acham?").Settle(0)
	t.Cleanup(m.Stop)
	frame := m.Snapshot(128, 40)
	contains(t, frame, "→ everyone:", "passed · nothing to add", "conclusão do time", "↩ answers from @iris and @leo")
	if st := m.session.agentStatus("nico"); st != Passed {
		t.Errorf("nico's status = %v, want Passed", st)
	}
}

func TestMentionMenuOffersTodos(t *testing.T) {
	m := newModel(t, engine.DemoScripts()).PressKeys("@", "t")
	contains(t, m.Snapshot(128, 32), "todos", "everyone answers")
	if got := string(m.PressKeys("tab").input); got != "@todos " {
		t.Errorf("tab should complete @todos, got %q", got)
	}
}
