package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"tubas/internal/agent"
)

// stubRouter returns a fixed decision (or error).
type stubRouter struct {
	d   RouteDecision
	err error
}

func (s stubRouter) Route(context.Context, string, string, []agent.Agent) (RouteDecision, error) {
	return s.d, s.err
}

func routedOf(t *testing.T, events []Event) Routed {
	t.Helper()
	for _, e := range events {
		if r, ok := e.(Routed); ok {
			return r
		}
	}
	t.Fatal("no Routed event")
	return Routed{}
}

func TestSelectAgentsPolicy(t *testing.T) {
	cases := []struct {
		name string
		d    RouteDecision
		want []string
	}{
		{"best alone", RouteDecision{Scores: map[string]float64{"leo": 0.9, "iris": 0.6}}, []string{"leo"}},
		{"for everyone", RouteDecision{Scores: map[string]float64{"leo": 0.9, "iris": 0.6, "nico": 0.2}, ForEveryone: 0.8}, []string{"leo", "iris"}},
		{"nobody clears the bar", RouteDecision{Scores: map[string]float64{"leo": 0.3}}, []string{"iris"}},
		{"for everyone but only one fits", RouteDecision{Scores: map[string]float64{"leo": 0.9}, ForEveryone: 0.9}, []string{"leo"}},
	}
	for _, c := range cases {
		if got := SelectAgents(c.d, "iris"); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNoMentionGoesToTheLead(t *testing.T) {
	events := make(chan Event)
	req := Request{Team: demoTeam, Question: "?", MaxHops: 6, Lead: "nico"}
	go Run(context.Background(), FakeEngine{Scripts: DemoScripts()}, req, events)
	got := collect(t, events, true)
	if r := routedOf(t, got); r.By != "lead" || !reflect.DeepEqual(r.Agents, []string{"nico"}) {
		t.Errorf("routed = %+v", r)
	}
	if turnsOf(got)[0] != "nico" {
		t.Errorf("the lead should answer first, got %v", turnsOf(got))
	}
}

func TestRouterPicksOneAgent(t *testing.T) {
	events := make(chan Event)
	router := stubRouter{d: RouteDecision{Scores: map[string]float64{"leo": 0.95, "iris": 0.4}, Reason: "pipeline"}}
	req := Request{Team: demoTeam, Question: "a carga falhou?", MaxHops: 6, Lead: "iris", Routing: "router", Router: router}
	go Run(context.Background(), FakeEngine{Scripts: map[string]Script{"leo": {Summary: "não falhou"}}}, req, events)
	got := collect(t, events, true)
	if r := routedOf(t, got); r.By != "router" || !reflect.DeepEqual(r.Agents, []string{"leo"}) || r.Reason != "pipeline" {
		t.Errorf("routed = %+v", r)
	}
	if names := turnsOf(got); !reflect.DeepEqual(names, []string{"leo"}) {
		t.Errorf("turns = %v", names)
	}
}

func TestRouterFailureFallsBackToTheLead(t *testing.T) {
	events := make(chan Event)
	req := Request{Team: demoTeam, Question: "?", MaxHops: 6, Lead: "iris", Routing: "router",
		Router: stubRouter{err: errors.New("no network")}}
	go Run(context.Background(), FakeEngine{Scripts: map[string]Script{"iris": {Summary: "ok"}}}, req, events)
	got := collect(t, events, true)
	if r := routedOf(t, got); r.Agents[0] != "iris" || r.By != "lead" {
		t.Errorf("routed = %+v", r)
	}
}

// @todos: everyone answers at once, whoever has nothing passes, and the
// lead closes the round with the answers.
func TestEveryoneAnswersAndTheLeadCloses(t *testing.T) {
	rec := &recorder{prompts: map[string]string{}, scripts: map[string]string{
		"iris:team": "queda no sul",
		"leo:team":  "pipeline ok",
		"nico:team": "PASS",
		"iris:join": "conclusão do time",
	}}
	events := make(chan Event)
	req := Request{Team: demoTeam, Question: "o que vocês acham?", MaxHops: 6, Lead: "iris", Everyone: true}
	go Run(context.Background(), rec, req, events)
	got := collect(t, events, true)

	if r := routedOf(t, got); r.By != "everyone" || len(r.Agents) != 3 {
		t.Errorf("routed = %+v", r)
	}
	names := turnsOf(got)
	if len(names) != 4 || names[3] != "iris" {
		t.Fatalf("turns = %v, want the three agents, then iris closing", names)
	}
	var nicoPassed bool
	for _, e := range got {
		if f, ok := e.(TurnFinished); ok && f.Agent == "nico" {
			nicoPassed = f.Passed && f.Summary == ""
		}
	}
	if !nicoPassed {
		t.Error("nico answered PASS and should show as passed")
	}
	join := rec.prompts["iris:join"]
	for _, want := range []string{"queda no sul", "pipeline ok", "@nico (role of nico): passed"} {
		if !strings.Contains(join, want) {
			t.Errorf("the lead's closing prompt should contain %q:\n%s", want, join)
		}
	}
	if p := rec.prompts["leo:team"]; !strings.Contains(p, "exactly: PASS") {
		t.Errorf("team prompt should allow passing:\n%s", p)
	}
}

func TestOnlyTeamTurnsMayPass(t *testing.T) {
	events := make(chan Event)
	req := Request{Team: demoTeam, Target: "leo", Question: "?", MaxHops: 6}
	go Run(context.Background(), &recorder{prompts: map[string]string{}, scripts: map[string]string{"leo": "PASS"}}, req, events)
	for _, e := range collect(t, events, true) {
		if f, ok := e.(TurnFinished); ok && f.Passed {
			t.Error("an agent named by the human must answer: PASS is just text there")
		}
	}
}

func TestIsPass(t *testing.T) {
	for text, want := range map[string]bool{"PASS": true, "pass.": true, "**PASS**\n": true, "passo a passo": false, "Não passo": false} {
		if isPass(text) != want {
			t.Errorf("isPass(%q) = %v", text, !want)
		}
	}
}

func TestRuleRouter(t *testing.T) {
	cands := []agent.Agent{{Name: "iris", Role: "investigator"}, {Name: "leo", Role: "engineer"}}
	d, _ := RuleRouter{}.Route(context.Background(), "leo, a carga rodou?", "", cands)
	if got := SelectAgents(d, "iris"); !reflect.DeepEqual(got, []string{"leo"}) {
		t.Errorf("named agent: got %v", got)
	}
	d, _ = RuleRouter{}.Route(context.Background(), "o que o time acha?", "", cands)
	if got := SelectAgents(d, "iris"); len(got) != 2 {
		t.Errorf("for the team: got %v", got)
	}
}
