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
	"fmt"
	"sort"
	"strings"
	"time"

	"tubas/internal/agent"
)

// Engine runs one turn of one agent: it gets the agent and a prompt,
// reports what the agent does as events on out, and returns the reply.
//
// FakeEngine plays a script; ClaudeEngine runs `claude -p`. The rest of
// tubas only sees this interface, so swapping one for the other changes
// nothing else.
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

// ByHarness picks the engine from the agent's harness field, so one team
// can mix agents run by different harnesses.
type ByHarness map[string]Engine

func (b ByHarness) RunTurn(ctx context.Context, req TurnRequest, out chan<- Event) (Reply, error) {
	eng, ok := b[req.Agent.Harness]
	if !ok {
		return Reply{}, fmt.Errorf("%s runs on %s, which is not installed on this computer (available: %s)",
			req.Agent.Name, req.Agent.Harness, strings.Join(b.Names(), ", "))
	}
	return eng.RunTurn(ctx, req, out)
}

// Names are the harnesses available, sorted, claude first.
func (b ByHarness) Names() []string {
	var names []string
	for name := range b {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == "claude" || names[j] == "claude" {
			return names[i] == "claude"
		}
		return names[i] < names[j]
	})
	return names
}

// Name describes the engines, for the header: claude's own label (with
// its provider), plus the other harnesses available.
func (b ByHarness) Name() string {
	var parts []string
	for _, name := range b.Names() {
		if name == "api" && len(b) > 1 {
			continue // always there: the header names what is installed
		}
		label := name
		if named, ok := b[name].(interface{ Name() string }); ok && name == "claude" {
			label = named.Name()
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, " · ")
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
