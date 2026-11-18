// Package engine runs agents and reports what they do as a stream of
// events.
//
// Two ideas from Go do the heavy lifting here:
//
//   - goroutines: functions running at the same time as the rest of the
//     program. The session runs in one, so the TUI never freezes.
//   - channels: typed pipes between goroutines. The engine writes events
//     into one; the TUI reads from the other end.
package engine

import (
	"context"
	"time"

	"tubas/internal/agent"
)

// Engine runs one turn of one agent: it gets the agent and a prompt,
// reports what the agent does as events on out, and returns the reply.
//
// FakeEngine plays a script; NativeEngine runs tubas's own loop against a
// provider. The rest of tubas only sees this interface, so swapping one
// for the other changes nothing else.
type Engine interface {
	RunTurn(ctx context.Context, req TurnRequest, out chan<- Event) (Reply, error)
}

// TurnRequest is everything an engine needs for one turn.
type TurnRequest struct {
	Agent  agent.Agent
	Prompt string
	Team   map[string]agent.Agent // to tell the agent who it can call
	Kind   TurnKind

	// Code: the team's repositories (connector -> your checkout), and
	// where this session writes in each (its worktree, made on first use).
	Repos     map[string]string
	Workspace func(repo string) (string, error)

	// History: the conversation the turn belongs to, where its agents'
	// histories are kept (Threads), and its earlier questions (Past).
	// Followup is Prompt without the earlier questions, for an agent that
	// already has them in its history.
	Conversation string
	Threads      string
	Past         []Exchange
	Followup     string
}

// TurnKind says why an agent is running.
type TurnKind string

const (
	KindAsk     TurnKind = "ask"     // the human asked it
	KindHandoff TurnKind = "handoff" // another agent passed it the work
	KindWorker  TurnKind = "worker"  // one of several working in parallel; it answers back
	KindJoin    TurnKind = "join"    // the parallel answers came back to it
	KindTeam    TurnKind = "team"    // the human asked several agents at once; it may pass
)

// Reply is how an agent's turn ended: its full answer, which may end
// with a handoff line (see ParseHandoff).
type Reply struct {
	Text   string
	Tokens int     // every token the turn read and wrote
	Cached int     // of those, read from the prompt cache (about a tenth of the price)
	Cost   float64 // what the turn cost, in US$, when the engine knows
	Model  string  // the model that answered
	Plan   bool    // billed to a subscription (Claude's or ChatGPT's), not per token
}

// send delivers an event, unless the context is cancelled first.
//
// select waits on several channel operations and runs whichever is ready
// first. Without it, a send to a reader that is gone would block forever.
func send(ctx context.Context, out chan<- Event, e Event) error {
	select {
	case out <- e:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sleep waits for d, but wakes up early if the context is cancelled.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
