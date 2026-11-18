package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"sync"
)

// Broker carries approval requests from the agents' tools to the TUI.
//
// The tool that wants approval (publish_report) runs inside the MCP
// server, a separate process that claude starts. It cannot reach the TUI
// directly, so it calls this small HTTP server on 127.0.0.1. The broker
// turns the call into an ApprovalRequested event on the agent's current
// turn, waits for your y or n, and answers the HTTP call with it.
type Broker struct {
	URL   string // where the MCP server sends requests
	Token string // shared secret, so no other local program can approve

	mu    sync.Mutex
	turns map[string]turnLink // agent name -> its running turn
	srv   *http.Server
}

type turnLink struct {
	ctx context.Context
	out chan<- Event
}

// ApprovalRequest is what the MCP server sends.
type ApprovalRequest struct {
	Agent  string `json:"agent"`
	Turn   string `json:"turn,omitempty"` // the turn that asked (see ClaudeEngine.Tools)
	Call   string `json:"call"`
	Detail string `json:"detail,omitempty"` // the whole call, and a senior model's review when there is one
}

// ApprovalAnswer is what it gets back.
type ApprovalAnswer struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

// NewBroker starts the broker on a free local port.
func NewBroker() (*Broker, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // port 0: the OS picks a free one
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	b := &Broker{
		URL:   "http://" + ln.Addr().String() + "/approve",
		Token: hex.EncodeToString(secret),
		turns: map[string]turnLink{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /approve", b.handle)
	b.srv = &http.Server{Handler: mux}
	go b.srv.Serve(ln)
	return b, nil
}

// Close stops the broker.
func (b *Broker) Close() error { return b.srv.Close() }

// Attach routes a turn's approvals to out while it runs. key is the turn
// id the gateway sends (or the agent's name, for gateways that send none).
// Call the returned function when the turn ends.
func (b *Broker) Attach(ctx context.Context, key string, out chan<- Event) func() {
	b.mu.Lock()
	b.turns[key] = turnLink{ctx, out}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.turns, key)
		b.mu.Unlock()
	}
}

// newTurnID is a random id for one turn.
func newTurnID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (b *Broker) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+b.Token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req ApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	approved, reason := b.ask(r.Context(), req)
	writeAnswer(w, ApprovalAnswer{Approved: approved, Reason: reason})
}

// Ask asks you to approve a call of a turn running in this process (the
// native tools), the same way the gateway's calls are asked.
func (b *Broker) Ask(ctx context.Context, turn, agentName, call, detail string) (bool, string, error) {
	approved, reason := b.ask(ctx, ApprovalRequest{Agent: agentName, Turn: turn, Call: call, Detail: detail})
	if !approved && ctx.Err() != nil {
		return false, "", ctx.Err()
	}
	return approved, reason, nil
}

// ask puts the request on the turn's screen and waits for your answer.
func (b *Broker) ask(ctx context.Context, req ApprovalRequest) (bool, string) {
	b.mu.Lock()
	key := req.Turn
	if key == "" {
		key = req.Agent
	}
	link, ok := b.turns[key]
	b.mu.Unlock()
	if !ok {
		return false, "no running turn for " + req.Agent
	}
	reply := make(chan bool, 1)
	if send(link.ctx, link.out, ApprovalRequested{Agent: req.Agent, Call: req.Call, Reply: reply, Detail: req.Detail}) != nil {
		return false, "session stopped"
	}
	select {
	case approved := <-reply:
		return approved, ""
	case <-link.ctx.Done():
		return false, "session stopped"
	case <-ctx.Done(): // the caller gave up
		return false, "the call was cancelled"
	}
}

func writeAnswer(w http.ResponseWriter, a ApprovalAnswer) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(a)
}
