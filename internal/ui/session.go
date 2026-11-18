package ui

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"tubas/internal/engine"
	"tubas/internal/tools"
)

// These types describe what the screen shows about a session: who asked
// what, which agent took each turn, and what each one did. They are built
// up one engine event at a time by apply.

// Status is what an agent is doing right now.
type Status int

const (
	Idle    Status = iota // not called in this session yet
	Live                  // running right now
	Waiting               // paused, waiting for a human decision
	Done                  // finished its turn
	Stopped               // interrupted before finishing
	Passed                // had nothing to add
)

// StepKind says how to draw a step.
type StepKind int

const (
	ToolCall StepKind = iota // the agent called a tool
	Result                   // something that worked or was confirmed
	Failure                  // something that failed or was refused
	Senior                   // advice from a stronger model (escalation)
)

// Step is one thing an agent did during its turn.
type Step struct {
	Kind StepKind
	Text string
	Note string // extra detail, like "approved by you at 10:46"
}

// Turn is one agent's participation, from being called to handing off.
type Turn struct {
	Agent     string
	Status    Status
	Steps     []Step
	Text      string    // answer being streamed while the turn is live
	Summary   string    // the conclusion, shown even when collapsed
	From      string    // who handed it the work: "you", an agent, or "" for answers coming back
	Ask       string    // what it was asked
	Handoffs  []Handoff // who it called next: one passes the baton, several run in parallel
	Tokens    int
	Cached    int     // of Tokens, read from the prompt cache
	Cost      float64 // US$, when the engine knows
	Model     string  // the model that answered
	Plan      bool    // billed to a subscription, not per token
	Seconds   int
	StartedAt time.Time
	Retry     string       // the provider is failing and tubas tries again: why
	Todos     []tools.Todo // the agent's checklist, when it keeps one
	Open      bool         // expanded by the user
}

// Handoff is one agent this turn called, and what it asked.
type Handoff struct{ To, Note string }

// queries counts the tool calls in the turn.
func (t Turn) queries() int {
	n := 0
	for _, s := range t.Steps {
		if s.Kind == ToolCall {
			n++
		}
	}
	return n
}

// Approval is a tool call that is waiting for a human yes or no.
type Approval struct {
	Agent string
	Call  string
}

// Outcome is how a session ended.
type Outcome int

const (
	Ongoing   Outcome = iota // still running (or never started)
	Finished                 // the last agent answered without handing off
	HopLimit                 // stopped by the hop limit
	Failed                   // something went wrong
	Cancelled                // you pressed esc
)

// Session is one conversation: the question and every turn after it.
type Session struct {
	Team      string
	AskedAt   string
	Target    string // the agent the question was addressed to ("" when routing decided)
	Question  string
	Route     *Route // who routing picked, when the human named no single agent
	Turns     []Turn
	MaxHops   int
	Pending   *Approval `json:"-"` // the approval on screen; nil when nothing waits
	StartedAt time.Time
	Outcome   Outcome
	Detail    string        // why it stopped, for HopLimit and Failed
	Elapsed   time.Duration // total time, once it ended

	// Your decisions, by agent, waiting to be attached to that agent's
	// next tool result. One per agent: parallel agents can each have one.
	decisions map[string]string
}

// Route is how a question that named no single agent was routed.
type Route struct {
	Agents []string
	By     string // lead, router or everyone
	Reason string
	Scores map[string]float64
}

// started reports whether there is a session at all.
func (s Session) started() bool { return s.Question != "" }

// addressee is who the question went to, for prompts and screens.
func (s Session) addressee() string {
	if s.Target != "" {
		return "@" + s.Target
	}
	return "the team"
}

// apply returns the session after one event.
//
// It clones Turns first. The old session shares the same underlying
// array, and append or s.Turns[i] = ... would otherwise change it too.
func (s Session) apply(e engine.Event) Session {
	s.Turns = slices.Clone(s.Turns)

	// A type switch: e is an interface, and each case gets the concrete
	// type, with its own fields.
	switch e := e.(type) {
	case engine.Routed:
		s.Route = &Route{Agents: e.Agents, By: e.By, Reason: e.Reason, Scores: e.Scores}
	case engine.TurnStarted:
		s.Turns = append(s.Turns, Turn{Agent: e.Agent, Status: Live, StartedAt: time.Now(), From: e.From, Ask: e.Note})
	case engine.ToolCalled:
		s.updateTurn(e.Agent, func(t *Turn) {
			t.Steps = append(t.Steps, Step{Kind: ToolCall, Text: e.Call})
		})
	case engine.ToolResult:
		step := Step{Kind: Result, Text: e.Text}
		if e.Failed {
			step.Kind = Failure
		}
		if note, ok := s.decisions[e.Agent]; ok {
			step.Note = note
			s.decisions = maps.Clone(s.decisions) // maps are shared too: copy before changing
			delete(s.decisions, e.Agent)
		}
		var advice *Step
		if text, rest, ok := strings.Cut(step.Text, "\n↑ "); ok {
			step.Text = text
			advice = &Step{Kind: Senior, Text: strings.TrimSpace(rest)}
		}
		s.updateTurn(e.Agent, func(t *Turn) {
			t.Steps = append(t.Steps, step)
			if advice != nil {
				t.Steps = append(t.Steps, *advice)
			}
		})
	case engine.TextDelta:
		s.updateTurn(e.Agent, func(t *Turn) { t.Text += e.Text; t.Retry = "" })
	case engine.Todos:
		s.updateTurn(e.Agent, func(t *Turn) { t.Todos = e.Items })
	case engine.Retrying:
		s.updateTurn(e.Agent, func(t *Turn) {
			t.Retry = e.String()
			if e.Partial {
				t.Text = "" // the attempt that broke: its text goes, the next one writes again
			}
		})
	case engine.ApprovalRequested:
		s.updateTurn(e.Agent, func(t *Turn) { t.Status = Waiting })
		if s.Pending == nil { // otherwise it waits in the model's queue
			s.Pending = &Approval{Agent: e.Agent, Call: e.Call}
		}
	case engine.TurnFinished:
		s.updateTurn(e.Agent, func(t *Turn) {
			t.Status = Done
			if e.Passed {
				t.Status = Passed
			}
			t.Summary = e.Summary
			t.Text = ""
			t.Tokens, t.Cached, t.Cost, t.Model, t.Plan = e.Tokens, e.Cached, e.Cost, e.Model, e.Plan
			t.Seconds = int(e.Elapsed.Seconds())
		})
	case engine.HandedOff:
		s.updateTurn(e.From, func(t *Turn) {
			t.Handoffs = append(slices.Clone(t.Handoffs), Handoff{e.To, e.Note})
		})
	case engine.SessionDone:
		s = s.end(Finished, "")
	case engine.HopLimitReached:
		s = s.end(HopLimit, fmt.Sprintf("hop limit of %d reached", e.Limit))
	case engine.Failed:
		s = s.end(Failed, fmt.Sprintf("%s: %v", e.Agent, e.Err))
	}
	return s
}

func (s Session) end(o Outcome, detail string) Session {
	s.Outcome, s.Detail, s.Elapsed = o, detail, time.Since(s.StartedAt)
	// A turn still running when the session ends did not finish: it no
	// longer says "working", nor that it is trying again.
	s.Turns = slices.Clone(s.Turns)
	for i := range s.Turns {
		if t := &s.Turns[i]; t.Status == Live || t.Status == Waiting {
			t.Status, t.Retry = Stopped, ""
			t.Seconds = int(time.Since(t.StartedAt).Seconds())
		}
	}
	return s
}

// approvalAnswered clears the pending approval, puts the agent back to
// work and remembers the decision for the result that comes next.
func (s Session) approvalAnswered(approved bool, at time.Time) Session {
	if s.Pending == nil {
		return s
	}
	s.Turns = slices.Clone(s.Turns)
	s.updateTurn(s.Pending.Agent, func(t *Turn) { t.Status = Live })
	note := "denied by you at " + at.Format("15:04")
	if approved {
		note = "approved by you at " + at.Format("15:04")
	}
	s.decisions = maps.Clone(s.decisions)
	if s.decisions == nil {
		s.decisions = map[string]string{}
	}
	s.decisions[s.Pending.Agent] = note
	s.Pending = nil
	return s
}

// cancelled marks the session as stopped by the user.
func (s Session) cancelled() Session {
	s.Turns = slices.Clone(s.Turns)
	for i := range s.Turns {
		if st := s.Turns[i].Status; st == Live || st == Waiting {
			s.Turns[i].Status = Stopped
		}
	}
	s.Pending = nil
	return s.end(Cancelled, "by you")
}

// toggleOpen expands or collapses turn i.
func (s Session) toggleOpen(i int) Session {
	if i < 0 || i >= len(s.Turns) {
		return s
	}
	s.Turns = slices.Clone(s.Turns)
	s.Turns[i].Open = !s.Turns[i].Open
	return s
}

// updateTurn changes the agent's latest turn. The pointer receiver
// (s *Session) lets it modify the session it is called on.
func (s *Session) updateTurn(name string, change func(t *Turn)) {
	i := s.lastTurnIndex(name)
	if i < 0 {
		return
	}
	t := s.Turns[i]
	t.Steps = slices.Clone(t.Steps) // same reason as in apply
	change(&t)
	s.Turns[i] = t
}

// agentStatus is the status of the agent's latest turn, or Idle.
func (s Session) agentStatus(name string) Status {
	st := Idle
	for _, t := range s.Turns {
		if t.Agent == name {
			st = t.Status
		}
	}
	return st
}

func (s Session) agentTokens(name string) int {
	total := 0
	for _, t := range s.Turns {
		if t.Agent == name {
			total += t.Tokens
		}
	}
	return total
}

func (s Session) agentSeconds(name string) int {
	total := 0
	for _, t := range s.Turns {
		if t.Agent == name {
			total += t.Seconds
		}
	}
	return total
}

func (s Session) totalTokens() int {
	total := 0
	for _, t := range s.Turns {
		total += t.Tokens
	}
	return total
}

// usage sums the session: tokens, the share read from the cache, the
// cost of the turns billed per token, and whether any went to a plan.
func (s Session) usage() (tokens, cached int, cost float64, plan bool) {
	for _, t := range s.Turns {
		tokens += t.Tokens
		cached += t.Cached
		if t.Plan {
			plan = true
		} else {
			cost += t.Cost
		}
	}
	return tokens, cached, cost, plan
}

// lastTurnIndex returns the index of the agent's latest turn, or -1.
func (s Session) lastTurnIndex(name string) int {
	for i := len(s.Turns) - 1; i >= 0; i-- {
		if s.Turns[i].Agent == name {
			return i
		}
	}
	return -1
}

// finalAnswer is the session's conclusion: the last turn's summary.
func (s Session) finalAnswer() (agentName, summary string) {
	for i := len(s.Turns) - 1; i >= 0; i-- {
		if s.Turns[i].Summary != "" {
			return s.Turns[i].Agent, s.Turns[i].Summary
		}
	}
	return "", ""
}
