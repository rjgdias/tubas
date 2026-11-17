// Package server keeps a team's work running in the background.
//
// tubas starts one server per team folder. The server owns the sessions,
// the routines and the approvals; the screen is only a client. Closing
// the screen (or ctrl+b q) detaches: agents keep working, routines keep
// running, and `tubas` attaches again. Scripts and other agents talk to
// the same server through the command line (tubas status, session new,
// wait, read).
//
// Client and server speak JSON over HTTP on 127.0.0.1, with a random
// token written in .tubas/server.json, which only you can read.
package server

import (
	"errors"
	"time"

	"tubas/internal/engine"
)

// Wire is an engine event as JSON: Kind says which, and only the fields
// that kind uses are set.
type Wire struct {
	Seq  int    `json:"seq"`
	Kind string `json:"kind"`

	Agent   string  `json:"agent,omitempty"`
	From    string  `json:"from,omitempty"`
	To      string  `json:"to,omitempty"`
	Note    string  `json:"note,omitempty"`
	Call    string  `json:"call,omitempty"`
	Text    string  `json:"text,omitempty"`
	Failed  bool    `json:"failed,omitempty"`
	Detail  string  `json:"detail,omitempty"`
	Summary string  `json:"summary,omitempty"`
	Tokens  int     `json:"tokens,omitempty"`
	Cached  int     `json:"cached,omitempty"`
	Cost    float64 `json:"cost,omitempty"`
	Millis  int64   `json:"ms,omitempty"`
	Passed  bool    `json:"passed,omitempty"`
	Model   string  `json:"model,omitempty"`
	Plan    bool    `json:"plan,omitempty"`
	Limit   int     `json:"limit,omitempty"`
	Error   string  `json:"error,omitempty"`

	Agents []string           `json:"agents,omitempty"`
	By     string             `json:"by,omitempty"`
	Reason string             `json:"reason,omitempty"`
	Scores map[string]float64 `json:"scores,omitempty"`

	Approval string `json:"approval,omitempty"` // the approval's id, to answer it
}

// Encode turns an event into its wire form.
func Encode(e engine.Event) Wire {
	switch e := e.(type) {
	case engine.Routed:
		return Wire{Kind: "routed", Agents: e.Agents, By: e.By, Reason: e.Reason, Scores: e.Scores}
	case engine.TurnStarted:
		return Wire{Kind: "turn_started", Agent: e.Agent, From: e.From, Note: e.Note}
	case engine.ToolCalled:
		return Wire{Kind: "tool_called", Agent: e.Agent, Call: e.Call}
	case engine.ToolResult:
		return Wire{Kind: "tool_result", Agent: e.Agent, Text: e.Text, Failed: e.Failed}
	case engine.TextDelta:
		return Wire{Kind: "text", Agent: e.Agent, Text: e.Text}
	case engine.ApprovalRequested:
		return Wire{Kind: "approval", Agent: e.Agent, Call: e.Call, Detail: e.Detail}
	case engine.TurnFinished:
		return Wire{Kind: "turn_finished", Agent: e.Agent, Summary: e.Summary, Tokens: e.Tokens, Cached: e.Cached,
			Cost: e.Cost, Millis: e.Elapsed.Milliseconds(), Passed: e.Passed, Model: e.Model, Plan: e.Plan}
	case engine.HandedOff:
		return Wire{Kind: "handed_off", From: e.From, To: e.To, Note: e.Note}
	case engine.SessionDone:
		return Wire{Kind: "done"}
	case engine.HopLimitReached:
		return Wire{Kind: "hop_limit", Limit: e.Limit}
	case engine.Failed:
		msg := ""
		if e.Err != nil {
			msg = e.Err.Error()
		}
		return Wire{Kind: "failed", Agent: e.Agent, Error: msg}
	}
	return Wire{Kind: "unknown"}
}

// Decode turns a wire event back into an engine event. An approval gets
// reply as its channel: whatever is sent there is the answer.
func (w Wire) Decode(reply chan<- bool) engine.Event {
	switch w.Kind {
	case "routed":
		return engine.Routed{Agents: w.Agents, By: w.By, Reason: w.Reason, Scores: w.Scores}
	case "turn_started":
		return engine.TurnStarted{Agent: w.Agent, From: w.From, Note: w.Note}
	case "tool_called":
		return engine.ToolCalled{Agent: w.Agent, Call: w.Call}
	case "tool_result":
		return engine.ToolResult{Agent: w.Agent, Text: w.Text, Failed: w.Failed}
	case "text":
		return engine.TextDelta{Agent: w.Agent, Text: w.Text}
	case "approval":
		return engine.ApprovalRequested{Agent: w.Agent, Call: w.Call, Detail: w.Detail, Reply: reply, ID: w.Approval}
	case "turn_finished":
		return engine.TurnFinished{Agent: w.Agent, Summary: w.Summary, Tokens: w.Tokens, Cached: w.Cached,
			Cost: w.Cost, Elapsed: time.Duration(w.Millis) * time.Millisecond, Passed: w.Passed, Model: w.Model, Plan: w.Plan}
	case "handed_off":
		return engine.HandedOff{From: w.From, To: w.To, Note: w.Note}
	case "done":
		return engine.SessionDone{}
	case "hop_limit":
		return engine.HopLimitReached{Limit: w.Limit}
	case "failed":
		return engine.Failed{Agent: w.Agent, Err: errors.New(w.Error)}
	}
	return nil
}

// Ends says whether the event closes the session.
func (w Wire) Ends() bool { return w.Kind == "done" || w.Kind == "hop_limit" || w.Kind == "failed" }
