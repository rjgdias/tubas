package engine

import "time"

// Event is something that happened while agents were working. The engine
// produces events; the TUI reads them and redraws.
//
// Event is an interface with one unexported method. Only types in this
// package can implement it, so the list below is the complete set. The
// TUI tells them apart with a type switch.
type Event interface{ isEvent() }

// Routed: who will answer a question that named no single agent, and why.
// By is "lead", "router" or "everyone".
type Routed struct {
	Agents []string
	By     string
	Reason string
	Scores map[string]float64
}

// TurnStarted: an agent began its turn. From is who handed it the work
// ("you" for the first turn, "" when answers come back to a caller) and
// Note is what it was asked.
type TurnStarted struct{ Agent, From, Note string }

// ToolCalled: an agent called a tool, like run_sql(...).
type ToolCalled struct{ Agent, Call string }

// ToolResult: what came back from a tool call.
type ToolResult struct {
	Agent  string
	Text   string
	Failed bool
}

// TextDelta: a piece of the agent's answer, streamed as it is written.
type TextDelta struct{ Agent, Text string }

// ApprovalRequested: an agent wants to run a tool that changes something
// and is paused until a human answers on Reply.
type ApprovalRequested struct {
	Agent string
	Call  string
	Reply chan<- bool // send-only: whoever gets this event can only answer

	Detail string // the whole call (every argument), and a senior review if escalation ran
	ID     string // set when the approval comes from the background server
}

// TurnFinished: an agent ended its turn.
type TurnFinished struct {
	Agent   string
	Summary string
	Tokens  int
	Elapsed time.Duration
	Passed  bool    // the agent had nothing to add
	Cached  int     // tokens read from the prompt cache
	Cost    float64 // US$, when the engine knows
	Model   string  // the model that answered
	Plan    bool    // billed to a subscription: no price per turn
}

// HandedOff: an agent passed the work to another one.
type HandedOff struct{ From, To, Note string }

// SessionDone: the last agent answered without handing off.
type SessionDone struct{}

// HopLimitReached: the session stopped because it hit the hop limit.
type HopLimitReached struct{ Limit int }

// Failed: something went wrong and the session stopped.
type Failed struct {
	Agent string
	Err   error
}

func (Routed) isEvent()            {}
func (TurnStarted) isEvent()       {}
func (ToolCalled) isEvent()        {}
func (ToolResult) isEvent()        {}
func (TextDelta) isEvent()         {}
func (ApprovalRequested) isEvent() {}
func (TurnFinished) isEvent()      {}
func (HandedOff) isEvent()         {}
func (SessionDone) isEvent()       {}
func (HopLimitReached) isEvent()   {}
func (Failed) isEvent()            {}
