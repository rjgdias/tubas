package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tubas/internal/agent"
)

// team builds agents from name -> who they can call.
func team(calls map[string][]string) map[string]agent.Agent {
	t := map[string]agent.Agent{}
	for name, canCall := range calls {
		t[name] = agent.Agent{Name: name, Role: "role of " + name, CanCall: canCall}
	}
	return t
}

var demoTeam = team(map[string][]string{
	"iris": {"leo", "nico"},
	"leo":  {"iris", "nico"},
	"nico": {"iris"},
})

// collect reads every event until Run closes the channel, answering any
// approval with approve.
func collect(t *testing.T, events <-chan Event, approve bool) []Event {
	t.Helper()
	var got []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-events:
			if !ok { // closed: Run is done
				return got
			}
			if a, isApproval := e.(ApprovalRequested); isApproval {
				a.Reply <- approve
			}
			got = append(got, e)
		case <-timeout:
			t.Fatal("Run did not finish in time")
		}
	}
}

func turnsOf(events []Event) []string {
	var names []string
	for _, e := range events {
		if s, ok := e.(TurnStarted); ok {
			names = append(names, s.Agent)
		}
	}
	return names
}

func last(events []Event) Event { return events[len(events)-1] }

func TestRunFollowsHandoffs(t *testing.T) {
	events := make(chan Event)
	req := Request{Team: demoTeam, Target: "iris", Question: "?", MaxHops: 6}
	go Run(context.Background(), FakeEngine{Scripts: DemoScripts()}, req, events)

	got := collect(t, events, true)
	if names := turnsOf(got); !reflect.DeepEqual(names, []string{"iris", "leo", "nico"}) {
		t.Errorf("turns = %v", names)
	}
	if _, ok := last(got).(SessionDone); !ok {
		t.Errorf("last event should be SessionDone, got %T", last(got))
	}
	// The handoff line is protocol, not part of what the UI shows.
	for _, e := range got {
		if f, ok := e.(TurnFinished); ok && strings.Contains(f.Summary, "HANDOFF") {
			t.Errorf("summary still has the handoff line: %q", f.Summary)
		}
	}
}

func TestDeniedApprovalIsAFailedResult(t *testing.T) {
	events := make(chan Event)
	req := Request{Team: demoTeam, Target: "nico", Question: "?", MaxHops: 6}
	go Run(context.Background(), FakeEngine{Scripts: DemoScripts()}, req, events)

	for _, e := range collect(t, events, false) {
		if r, ok := e.(ToolResult); ok && r.Failed {
			return
		}
	}
	t.Error("denying should produce a failed ToolResult")
}

func TestRunStopsAtHopLimit(t *testing.T) {
	// Two agents that keep passing the work back and forth.
	scripts := map[string]Script{
		"ana": {Summary: "sua vez", Handoff: "bia", HandoffNote: "olha"},
		"bia": {Summary: "não, sua", Handoff: "ana", HandoffNote: "olha você"},
	}
	events := make(chan Event)
	req := Request{Team: team(map[string][]string{"ana": {"bia"}, "bia": {"ana"}}),
		Target: "ana", Question: "?", MaxHops: 3}
	go Run(context.Background(), FakeEngine{Scripts: scripts}, req, events)

	got := collect(t, events, true)
	if n := len(turnsOf(got)); n != 3 {
		t.Errorf("expected 3 turns, got %d", n)
	}
	if _, ok := last(got).(HopLimitReached); !ok {
		t.Errorf("last event should be HopLimitReached, got %T", last(got))
	}
}

func TestHandoffOutsideCanCallIsRefused(t *testing.T) {
	scripts := map[string]Script{"ana": {Summary: "bia resolve", Handoff: "bia", HandoffNote: "resolve"}}
	events := make(chan Event)
	req := Request{Team: team(map[string][]string{"ana": nil, "bia": nil}),
		Target: "ana", Question: "?", MaxHops: 6}
	go Run(context.Background(), FakeEngine{Scripts: scripts}, req, events)

	got := collect(t, events, true)
	if n := len(turnsOf(got)); n != 1 {
		t.Errorf("bia should never run; got %d turns", n)
	}
	refused := false
	for _, e := range got {
		if r, ok := e.(ToolResult); ok && r.Failed && strings.Contains(r.Text, "refused") {
			refused = true
		}
	}
	if !refused {
		t.Error("expected a refused handoff step")
	}
}

func TestHandoffToMissingAgentIsRefused(t *testing.T) {
	scripts := map[string]Script{"ana": {Summary: "zeca?", Handoff: "zeca", HandoffNote: "oi"}}
	events := make(chan Event)
	req := Request{Team: team(map[string][]string{"ana": nil}), Target: "ana", Question: "?", MaxHops: 6}
	go Run(context.Background(), FakeEngine{Scripts: scripts}, req, events)

	if got := collect(t, events, true); len(turnsOf(got)) != 1 {
		t.Errorf("zeca does not exist and must not run")
	}
}

// recorder is an engine that remembers the prompts it got. Its scripts
// are keyed by agent name, or "name:kind" for a specific kind of turn.
type recorder struct {
	mu      sync.Mutex
	prompts map[string]string
	scripts map[string]string
}

func (r *recorder) RunTurn(_ context.Context, req TurnRequest, _ chan<- Event) (Reply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := req.Agent.Name + ":" + string(req.Kind)
	r.prompts[key] = req.Prompt
	r.prompts[req.Agent.Name] = req.Prompt
	if text, ok := r.scripts[key]; ok {
		return Reply{Text: text}, nil
	}
	return Reply{Text: r.scripts[req.Agent.Name]}, nil
}

func TestNextAgentGetsTheContext(t *testing.T) {
	rec := &recorder{prompts: map[string]string{}, scripts: map[string]string{
		"iris": "queda na zona sul.\nHANDOFF @leo: pode ser pipeline?",
		"leo":  "pipeline ok.",
	}}
	events := make(chan Event)
	req := Request{Team: demoTeam, Target: "iris", Question: "por que caiu?", MaxHops: 6}
	go Run(context.Background(), rec, req, events)
	collect(t, events, true)

	p := rec.prompts["leo"]
	for _, want := range []string{"por que caiu?", "queda na zona sul.", "pode ser pipeline?", "@iris"} {
		if !strings.Contains(p, want) {
			t.Errorf("leo's prompt should contain %q:\n%s", want, p)
		}
	}
}

func TestParseHandoffs(t *testing.T) {
	cases := []struct {
		text, body string
		want       []Handoff
	}{
		{"tudo certo.", "tudo certo.", nil},
		{"achei.\nHANDOFF @leo: confere?", "achei.", []Handoff{{"leo", "confere?"}}},
		{"achei.\n\nhandoff @Nico - revisa\n\n", "achei.", []Handoff{{"nico", "revisa"}}},
		{"achei.\n→ HANDOFF @leo: ok", "achei.", []Handoff{{"leo", "ok"}}},
		{"HANDOFF @leo: no meio\nmas continuei.", "HANDOFF @leo: no meio\nmas continuei.", nil},
		// Several lines, or several names on one line: parallel.
		{"dividi.\nHANDOFF @leo: região\nHANDOFF @nico: horário", "dividi.",
			[]Handoff{{"leo", "região"}, {"nico", "horário"}}},
		{"dividi.\nHANDOFF @leo, @nico: olhem março\nHANDOFF @leo: repetido", "dividi.",
			[]Handoff{{"leo", "olhem março"}, {"nico", "olhem março"}}},
	}
	for _, c := range cases {
		body, got := ParseHandoffs(c.text)
		if body != c.body || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseHandoffs(%q) = %q, %v", c.text, body, got)
		}
	}
}

func TestParallelHandoffsComeBackToTheCaller(t *testing.T) {
	scripts := map[string]Script{
		"iris": {Summary: "vou dividir", Handoff: "leo", HandoffNote: "região",
			Also: []FakeHandoff{{"nico", "horário"}},
			Then: &Script{Summary: "conclusão: sul, no jantar"}},
		"leo":  {Summary: "o sul caiu", Handoff: "iris", HandoffNote: "ignorado"}, // workers cannot hand off
		"nico": {Summary: "o jantar caiu"},
	}
	events := make(chan Event)
	req := Request{Team: demoTeam, Target: "iris", Question: "?", MaxHops: 6}
	go Run(context.Background(), FakeEngine{Scripts: scripts}, req, events)
	got := collect(t, events, true)

	names := turnsOf(got)
	if len(names) != 4 || names[0] != "iris" || names[3] != "iris" {
		t.Fatalf("turns = %v, want iris, (leo and nico), iris", names)
	}
	var join TurnStarted
	for _, e := range got {
		if s, ok := e.(TurnStarted); ok && s.Agent == "iris" && s.From == "" {
			join = s
		}
	}
	if !strings.Contains(join.Note, "@leo and @nico") {
		t.Errorf("the join turn should say whose answers came back, got %+v", join)
	}
	if _, ok := last(got).(SessionDone); !ok {
		t.Errorf("last event should be SessionDone, got %T", last(got))
	}
}

// barrier is an engine whose workers only finish once both are running:
// if Run ran them one after the other, this would time out.
type barrier struct{ started sync.WaitGroup }

func (b *barrier) RunTurn(ctx context.Context, req TurnRequest, _ chan<- Event) (Reply, error) {
	switch req.Kind {
	case KindAsk:
		return Reply{Text: "divido\nHANDOFF @leo: a\nHANDOFF @nico: b"}, nil
	case KindWorker:
		b.started.Done()
		done := make(chan struct{})
		go func() { b.started.Wait(); close(done) }()
		select {
		case <-done:
			return Reply{Text: "feito"}, nil
		case <-time.After(2 * time.Second):
			return Reply{}, errors.New("workers did not run at the same time")
		}
	}
	return Reply{Text: "fim"}, nil
}

func TestWorkersRunAtTheSameTime(t *testing.T) {
	b := &barrier{}
	b.started.Add(2)
	events := make(chan Event)
	go Run(context.Background(), b, Request{Team: demoTeam, Target: "iris", Question: "?", MaxHops: 6}, events)
	for _, e := range collect(t, events, true) {
		if r, ok := e.(ToolResult); ok && r.Failed {
			t.Fatal(r.Text)
		}
	}
}

func TestParallelPromptsAndHopLimit(t *testing.T) {
	rec := &recorder{prompts: map[string]string{}, scripts: map[string]string{
		"iris":      "divido\nHANDOFF @leo: olha a região\nHANDOFF @nico: olha o horário",
		"leo":       "sul caiu",
		"nico":      "jantar caiu",
		"iris:join": "conclusão",
	}}
	events := make(chan Event)
	go Run(context.Background(), rec, Request{Team: demoTeam, Target: "iris", Question: "por quê?", MaxHops: 6}, events)
	collect(t, events, true)

	if p := rec.prompts["leo:worker"]; !strings.Contains(p, "olha a região") || !strings.Contains(p, "do not hand off") {
		t.Errorf("worker prompt:\n%s", p)
	}
	if p := rec.prompts["iris:join"]; !strings.Contains(p, "sul caiu") || !strings.Contains(p, "jantar caiu") {
		t.Errorf("join prompt should carry both answers:\n%s", p)
	}

	// With room for only one more turn, the second worker is not called.
	events = make(chan Event)
	go Run(context.Background(), rec, Request{Team: demoTeam, Target: "iris", Question: "?", MaxHops: 2}, events)
	got := collect(t, events, true)
	if n := len(turnsOf(got)); n != 2 {
		t.Errorf("expected 2 turns, got %d", n)
	}
	if _, ok := last(got).(HopLimitReached); !ok {
		t.Errorf("last event should be HopLimitReached, got %T", last(got))
	}
}

func TestFollowUpCarriesContext(t *testing.T) {
	rec := &recorder{prompts: map[string]string{}, scripts: map[string]string{"iris": "ok"}}
	events := make(chan Event)
	req := Request{Team: demoTeam, Target: "iris", Question: "e em abril?", MaxHops: 6,
		Context: "Earlier: the drop in March was in the south."}
	go Run(context.Background(), rec, req, events)
	collect(t, events, true)
	if p := rec.prompts["iris"]; !strings.Contains(p, "drop in March") || !strings.Contains(p, "e em abril?") {
		t.Errorf("first prompt should carry the earlier context:\n%s", p)
	}
}

func TestSystemPromptExplainsHandoff(t *testing.T) {
	p := SystemPrompt(demoTeam["iris"], demoTeam)
	for _, want := range []string{"@leo", "@nico", "HANDOFF @name:"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt should contain %q", want)
		}
	}
	if p := SystemPrompt(demoTeam["leo"], map[string]agent.Agent{"leo": demoTeam["leo"], "x": {}}); len(demoTeam["leo"].CanCall) == 0 && !strings.Contains(p, "cannot hand work") {
		t.Error("an agent with no can_call should be told it cannot hand off")
	}
	// Alone, it is a plain chat: nothing about a team or handing off.
	alone := agent.Agent{Name: "tubas", Role: "assistant"}
	p = SystemPrompt(alone, map[string]agent.Agent{"tubas": alone})
	for _, unwanted := range []string{"team", "hand", "numbers"} {
		if strings.Contains(strings.ToLower(p), unwanted) {
			t.Errorf("a lone assistant's prompt mentions %q:\n%s", unwanted, p)
		}
	}
	if !strings.Contains(p, "chatting with a person") {
		t.Errorf("a lone assistant is told it is a chat:\n%s", p)
	}
	if p := SystemPrompt(agent.Agent{Name: "x"}, map[string]agent.Agent{"x": {}, "y": {}}); !strings.Contains(p, "cannot hand work") {
		t.Error("in a team, an agent with no can_call is told it cannot hand off")
	}
}

// Cancelling the context must stop Run even while it waits for approval.
func TestCancelStopsRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // vet asks for it: always release a context
	events := make(chan Event)
	req := Request{Team: demoTeam, Target: "nico", Question: "?", MaxHops: 6}
	go Run(ctx, FakeEngine{Scripts: DemoScripts()}, req, events)

	for e := range events { // range on a channel stops when it is closed
		if _, ok := e.(ApprovalRequested); ok {
			cancel() // never answer; cancel instead
		}
	}
	// Reaching this line means the channel was closed: Run stopped.
}
