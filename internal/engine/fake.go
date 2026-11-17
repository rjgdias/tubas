package engine

import (
	"context"
	"strings"
	"time"
)

// FakeEngine plays a hand-written script for each agent, with pauses, so
// the TUI can be built and tested without calling any model.
type FakeEngine struct {
	Delay   time.Duration // base pause between events; 0 runs instantly
	Scripts map[string]Script
}

// Script is what an agent does in its turn.
type Script struct {
	Steps       []FakeStep
	Approval    *FakeApproval // a tool call that needs a human yes
	Summary     string
	Handoff     string
	HandoffNote string
	Also        []FakeHandoff // more handoffs: together they run in parallel
	Then        *Script       // what it does when the parallel answers come back
	Tokens      int
}

// FakeStep is one tool call and its result.
type FakeStep struct{ Call, Result string }

// FakeHandoff is one extra handoff line.
type FakeHandoff struct{ To, Note string }

// FakeApproval is a tool call that waits for approval, and what happens
// on each answer.
type FakeApproval struct{ Call, OnApprove, OnDeny string }

// RunTurn plays the agent's script. The value receiver (f FakeEngine)
// means FakeEngine itself satisfies the Engine interface.
func (f FakeEngine) RunTurn(ctx context.Context, req TurnRequest, out chan<- Event) (Reply, error) {
	a := req.Agent
	s, ok := f.Scripts[a.Name]
	if !ok {
		s = Script{Summary: "(fake) sem roteiro para este agente.", Tokens: 100}
	}
	if req.Kind == KindJoin && s.Then != nil {
		s = *s.Then
	}

	// emit waits, then sends. A closure: it captures ctx and out.
	emit := func(wait time.Duration, e Event) error {
		if err := sleep(ctx, wait); err != nil {
			return err
		}
		return send(ctx, out, e)
	}

	for _, step := range s.Steps {
		if err := emit(f.Delay, ToolCalled{a.Name, step.Call}); err != nil {
			return Reply{}, err
		}
		if err := emit(f.Delay*2, ToolResult{Agent: a.Name, Text: step.Result}); err != nil {
			return Reply{}, err
		}
	}

	if s.Approval != nil {
		// Buffer of 1: the TUI can answer without waiting for us to read.
		reply := make(chan bool, 1)
		if err := emit(f.Delay, ApprovalRequested{Agent: a.Name, Call: s.Approval.Call, Reply: reply}); err != nil {
			return Reply{}, err
		}
		var approved bool
		select {
		case approved = <-reply:
		case <-ctx.Done():
			return Reply{}, ctx.Err()
		}
		result := ToolResult{Agent: a.Name, Text: s.Approval.OnApprove}
		if !approved {
			result = ToolResult{Agent: a.Name, Text: s.Approval.OnDeny, Failed: true}
		}
		if err := emit(f.Delay, result); err != nil {
			return Reply{}, err
		}
	}

	// Stream the answer word by word, the way a real model does.
	for i, word := range strings.Fields(s.Summary) {
		if i > 0 {
			word = " " + word
		}
		if err := emit(f.Delay/4, TextDelta{a.Name, word}); err != nil {
			return Reply{}, err
		}
	}

	// Answer the way a real agent would: the text, then the handoff line.
	text := s.Summary
	if s.Handoff != "" {
		text += "\nHANDOFF @" + s.Handoff + ": " + s.HandoffNote
	}
	for _, h := range s.Also {
		text += "\nHANDOFF @" + h.To + ": " + h.Note
	}
	return Reply{Text: text, Tokens: s.Tokens}, nil
}

// Name describes the engine, for the header.
func (FakeEngine) Name() string { return "fake" }

// DemoScripts is the "why did orders drop in March?" story.
// The agents' text is in Portuguese because that is how they answer.
func DemoScripts() map[string]Script {
	return map[string]Script{
		"iris": {
			Steps: []FakeStep{
				{"run_sql(SELECT regiao, COUNT(*) FROM pedidos GROUP BY regiao, mes)", "queda de 18% concentrada na zona sul"},
				{"run_sql(SELECT faixa_horaria, COUNT(*) FROM pedidos WHERE regiao = 'sul')", "o jantar responde por 70% da queda"},
				{"run_sql(SELECT mes, COUNT(*) FROM cupons_ativos GROUP BY mes)", "cupons sem mudança no período"},
			},
			Summary:     "queda de 18% concentrada na zona sul, no jantar.",
			Handoff:     "leo",
			HandoffNote: "pode ser pipeline?",
			Tokens:      4200,
		},
		"leo": {
			Steps: []FakeStep{
				{"run_sql(SELECT tabela, status FROM cargas WHERE mes = 3)", "todas as cargas de março concluídas"},
				{"run_sql(SELECT regiao, COUNT(DISTINCT entregador) FROM entregas)", "23% menos entregadores ativos na zona sul"},
			},
			Summary:     "pipeline ok; menos entregadores ativos na zona sul.",
			Handoff:     "nico",
			HandoffNote: "confere os números?",
			Tokens:      2100,
		},
		"nico": {
			Steps: []FakeStep{
				{"run_sql(SELECT SUM(pedidos) FROM resumo GROUP BY regiao)", "soma das regiões bate com o total"},
				{"run_sql(SELECT COUNT(*) antes e depois do join)", "join não duplicou linhas · queda confirmada"},
			},
			Approval: &FakeApproval{
				Call:      "publish_report(#dados)",
				OnApprove: "relatório publicado em #dados",
				OnDeny:    "publicação recusada por você",
			},
			Summary: "números conferidos: a queda é real e vem da falta de entregadores na zona sul.",
			Tokens:  2800,
		},
	}
}
