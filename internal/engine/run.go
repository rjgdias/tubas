package engine

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"tubas/internal/agent"
)

// Request describes one session: the team, who gets the question first,
// and how many turns it may take.
type Request struct {
	Team     map[string]agent.Agent
	Target   string // the agent the human named; "" lets routing decide
	Everyone bool   // the human asked @todos
	Question string
	MaxHops  int
	Context  string // earlier questions and answers, for follow-ups

	Lead    string // who answers when nobody else is picked, and who closes a team round
	Routing string // for questions that name no agent: "lead" (default), "router" or "all"
	Router  Router // used when Routing is "router"

	// Code: repository connectors (name -> your checkout) and the session's
	// worktree for each, made the first time an agent writes there.
	Repos     map[string]string
	Workspace func(repo string) (string, error)
}

// Run plays a whole session. It decides who answers first (the named
// agent, the router's pick, the lead or everyone), runs that turn, reads
// the handoffs at the end of each answer, checks them against can_call,
// and goes on until an agent answers without handing off or the hop limit
// is reached.
//
// One handoff passes the baton. Several handoffs fan out: those agents run
// at the same time, each in its own goroutine, and when all of them are
// done the caller gets a turn with their answers. A question for several
// agents works the same way, with the lead closing the round.
//
// Run is meant to be started with `go`, in its own goroutine. It closes
// out when it returns: that is how the reader learns nothing else is
// coming. The rule in Go is that the sender closes, never the reader.
func Run(ctx context.Context, eng Engine, req Request, out chan<- Event) {
	defer close(out) // runs when Run returns, whatever the path
	if req.Lead == "" {
		req.Lead = sortedNames(req.Team)[0]
	}

	targets, ok := firstTargets(ctx, req, out)
	if !ok {
		return
	}

	hops := 0
	var history []string
	current, prompt := targets[0], firstPrompt(req)
	from, note, kind := "you", req.Question, KindAsk

	if len(targets) > 1 { // a round for the team: everyone at once, then the lead closes
		targets = targets[:min(len(targets), req.MaxHops-1)]
		hops += len(targets)
		answers, passed := runParallel(ctx, eng, req, "you", req.Question, KindTeam, handoffsTo(targets, req.Question),
			func(h Handoff) string { return teamPrompt(req, req.Team[h.To]) }, out)
		if ctx.Err() != nil {
			return
		}
		var names []string
		for i, name := range targets {
			w := req.Team[name]
			if passed[i] {
				history = append(history, fmt.Sprintf("- @%s (%s): passed, nothing to add", w.Name, w.Role))
				continue
			}
			history = append(history, fmt.Sprintf("- @%s (%s): %s", w.Name, w.Role, brief(answers[i])))
			names = append(names, "@"+name)
		}
		list := strings.Join(names, " and ")
		if list == "" {
			list = "nobody (everyone passed)"
		}
		current, from, note, kind = req.Lead, "", "answers from "+list, KindJoin
		prompt = teamJoinPrompt(req, history)
	}

	for {
		if hops >= req.MaxHops {
			send(ctx, out, HopLimitReached{req.MaxHops})
			return
		}
		a, ok := req.Team[current]
		if !ok {
			send(ctx, out, Failed{current, fmt.Errorf("no agent named %s", current)})
			return
		}
		hops++
		body, handoffs, _, err := runTurn(ctx, eng, req, a, prompt, from, note, kind, out)
		if err != nil {
			if ctx.Err() == nil { // a cancel is a normal stop, not a failure
				send(ctx, out, Failed{current, err})
			}
			return
		}
		history = append(history, fmt.Sprintf("- @%s (%s): %s", a.Name, a.Role, brief(body)))
		handoffs = allowed(ctx, a, handoffs, out)

		switch len(handoffs) {
		case 0:
			send(ctx, out, SessionDone{})
			return

		case 1: // pass the baton
			h := handoffs[0]
			if send(ctx, out, HandedOff{a.Name, h.To, h.Note}) != nil {
				return
			}
			current, from, note, kind = h.To, a.Name, h.Note, KindHandoff
			prompt = handoffPrompt(req, history, a.Name, h.Note)

		default: // fan out, then come back to the caller
			if room := req.MaxHops - hops; len(handoffs) > room {
				for _, h := range handoffs[max(room, 0):] {
					send(ctx, out, ToolResult{Agent: a.Name, Failed: true,
						Text: fmt.Sprintf("@%s not called: hop limit", h.To)})
				}
				handoffs = handoffs[:max(room, 0)]
			}
			if len(handoffs) == 0 {
				send(ctx, out, HopLimitReached{req.MaxHops})
				return
			}
			for _, h := range handoffs {
				if send(ctx, out, HandedOff{a.Name, h.To, h.Note}) != nil {
					return
				}
			}
			caller, past := a, slices.Clone(history)
			answers, _ := runParallel(ctx, eng, req, a.Name, "", KindWorker, handoffs,
				func(h Handoff) string { return workerPrompt(req, past, caller.Name, h.Note) }, out)
			if ctx.Err() != nil {
				return
			}
			hops += len(handoffs)
			var names []string
			for i, h := range handoffs {
				w := req.Team[h.To]
				history = append(history, fmt.Sprintf("- @%s (%s): %s", w.Name, w.Role, brief(answers[i])))
				names = append(names, "@"+h.To)
			}
			list := strings.Join(names, " and ")
			current, from, note, kind = a.Name, "", "answers from "+list, KindJoin
			prompt = joinPrompt(req, history, list)
		}
	}
}

// firstTargets decides who gets the question, and says so with a Routed
// event when the human named nobody.
func firstTargets(ctx context.Context, req Request, out chan<- Event) ([]string, bool) {
	if req.Target != "" {
		return []string{req.Target}, true
	}
	everyone := sortedNames(req.Team)
	var routed Routed
	switch {
	case req.Everyone || req.Routing == "all":
		routed = Routed{Agents: everyone, By: "everyone", Reason: "everyone was asked"}
	case req.Routing == "router" && req.Router != nil:
		candidates := make([]agent.Agent, len(everyone))
		for i, n := range everyone {
			candidates[i] = req.Team[n]
		}
		d, err := req.Router.Route(ctx, req.Question, req.Context, candidates)
		if ctx.Err() != nil {
			return nil, false
		}
		if err != nil {
			routed = Routed{Agents: []string{req.Lead}, By: "lead", Reason: "router failed, the lead answers: " + err.Error()}
		} else {
			routed = Routed{Agents: SelectAgents(d, req.Lead), By: "router", Reason: d.Reason, Scores: d.Scores}
		}
	default:
		routed = Routed{Agents: []string{req.Lead}, By: "lead", Reason: "the lead answers"}
	}
	if send(ctx, out, routed) != nil {
		return nil, false
	}
	return routed.Agents, true
}

// runParallel runs several agents at the same time and returns their
// answers in order, and which of them passed.
func runParallel(ctx context.Context, eng Engine, req Request, caller, note string, kind TurnKind,
	handoffs []Handoff, promptFor func(Handoff) string, out chan<- Event) ([]string, []bool) {
	answers := make([]string, len(handoffs))
	passed := make([]bool, len(handoffs))
	var wg sync.WaitGroup // waits until every goroutine called Done
	for i, h := range handoffs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := req.Team[h.To]
			ask := h.Note
			if note != "" {
				ask = note
			}
			body, _, didPass, err := runTurn(ctx, eng, req, w, promptFor(h), caller, ask, kind, out)
			if err != nil {
				answers[i] = "(no answer: " + err.Error() + ")"
				if ctx.Err() == nil { // close the worker's turn on screen
					send(ctx, out, ToolResult{Agent: w.Name, Failed: true, Text: err.Error()})
					send(ctx, out, TurnFinished{Agent: w.Name})
				}
				return
			}
			// Each goroutine writes only its own slots: no lock needed.
			answers[i], passed[i] = body, didPass
		}()
	}
	wg.Wait()
	return answers, passed
}

// runTurn runs one agent turn and announces its start and end.
func runTurn(ctx context.Context, eng Engine, req Request, a agent.Agent, prompt, from, note string, kind TurnKind, out chan<- Event) (string, []Handoff, bool, error) {
	if err := send(ctx, out, TurnStarted{a.Name, from, note}); err != nil {
		return "", nil, false, err
	}
	start := time.Now()
	reply, err := eng.RunTurn(ctx, TurnRequest{Agent: a, Prompt: prompt, Team: req.Team, Kind: kind, Repos: req.Repos, Workspace: req.Workspace}, out)
	if err != nil {
		return "", nil, false, err
	}
	body, handoffs := ParseHandoffs(reply.Text)
	if kind == KindWorker || kind == KindTeam {
		handoffs = nil // they answer back; they never hand off
	}
	passed := kind == KindTeam && isPass(body)
	if passed {
		body = ""
	}
	finished := TurnFinished{Agent: a.Name, Summary: body, Tokens: reply.Tokens, Elapsed: time.Since(start),
		Passed: passed, Cached: reply.Cached, Cost: reply.Cost, Model: reply.Model, Plan: reply.Plan}
	if err := send(ctx, out, finished); err != nil {
		return "", nil, false, err
	}
	return body, handoffs, passed, nil
}

// isPass reports whether an answer is a pass: "PASS", alone on its first line.
func isPass(body string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	return strings.EqualFold(strings.Trim(strings.TrimSpace(first), ".*` "), "pass")
}

// allowed drops handoffs the agent may not make, showing each as refused.
func allowed(ctx context.Context, a agent.Agent, handoffs []Handoff, out chan<- Event) []Handoff {
	var ok []Handoff
	for _, h := range handoffs {
		if h.To == a.Name || !slices.Contains(a.CanCall, h.To) {
			send(ctx, out, ToolResult{Agent: a.Name, Failed: true,
				Text: fmt.Sprintf("handoff to @%s refused: not in can_call", h.To)})
			continue
		}
		ok = append(ok, h)
	}
	return ok
}

func handoffsTo(names []string, note string) []Handoff {
	hs := make([]Handoff, len(names))
	for i, n := range names {
		hs[i] = Handoff{To: n, Note: note}
	}
	return hs
}

func sortedNames(team map[string]agent.Agent) []string {
	names := make([]string, 0, len(team))
	for n := range team {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
