package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"tubas/internal/agent"
	"tubas/internal/engine"
	"tubas/internal/notify"
	"tubas/internal/pane"
	"tubas/internal/repo"
	"tubas/internal/routine"
	"tubas/internal/team"
)

// States, the same everywhere (screen, status, wait):
//
//	blocked  an agent waits for your answer
//	working  agents are working
//	done     finished, and you have not looked yet
//	idle     finished and seen (or stopped)
const (
	Blocked = "blocked"
	Working = "working"
	Done    = "done"
	Idle    = "idle"
)

// Info is what a client sees of a session.
type Info struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Kind     string    `json:"kind"` // chat or routine
	Routine  string    `json:"routine,omitempty"`
	Target   string    `json:"target,omitempty"`
	Question string    `json:"question"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	State    string    `json:"state"`
	Outcome  string    `json:"outcome,omitempty"` // ok, failed, hop_limit, stopped
	Waiting  string    `json:"waiting,omitempty"` // the call waiting for you, when blocked
	Agent    string    `json:"agent,omitempty"`   // who answered last
	Summary  string    `json:"summary,omitempty"` // the last conclusion
	Error    string    `json:"error,omitempty"`
	Tokens   int       `json:"tokens,omitempty"`
	Cost     float64   `json:"cost,omitempty"`
	Events   int       `json:"events"`

	Worktrees []repo.Worktree `json:"worktrees,omitempty"` // where its agents changed code

	// Conversation groups a question with its follow-ups: the first
	// session's id. The screen lists conversations, not every message.
	Conversation string `json:"conversation,omitempty"`
}

// Pending is an approval waiting for you, in any session.
type Pending struct {
	ID      string    `json:"id"`
	Session string    `json:"session"`
	Title   string    `json:"title"`
	Routine string    `json:"routine,omitempty"`
	Agent   string    `json:"agent"`
	Call    string    `json:"call"`
	Detail  string    `json:"detail,omitempty"`
	At      time.Time `json:"at"`
}

// Note is something a routine brought back.
type Note struct {
	ID      string    `json:"id"`
	Routine string    `json:"routine"`
	Text    string    `json:"text"`
	Failed  bool      `json:"failed,omitempty"`
	At      time.Time `json:"at"`
}

// RoutineInfo is a routine's next run, as the server sees it.
type RoutineInfo struct {
	Name    string    `json:"name"`
	Agent   string    `json:"agent"`
	On      bool      `json:"on"`
	Next    time.Time `json:"next,omitempty"`
	Running bool      `json:"running,omitempty"`
}

// Status is the whole picture, for `tubas status` and the screen.
type Status struct {
	Team     string        `json:"team"`
	Dir      string        `json:"dir"`
	Started  time.Time     `json:"started"`
	PID      int           `json:"pid"`
	Sessions []Info        `json:"sessions"` // newest first
	Pending  []Pending     `json:"pending"`
	Notes    []Note        `json:"notes"`
	Routines []RoutineInfo `json:"routines"`
	Panes    []pane.Info   `json:"panes,omitempty"` // real terminals, oldest first
}

// StartRequest starts a session.
type StartRequest struct {
	Target   string `json:"target,omitempty"`
	Question string `json:"question"`
	Everyone bool   `json:"everyone,omitempty"`
	Context  string `json:"context,omitempty"`
	Title    string `json:"title,omitempty"`
	// Conversation, for a follow-up: the conversation it continues.
	Conversation string `json:"conversation,omitempty"`
}

// Options say how the server runs agents.
type Options struct {
	TeamDir string
	Engine  engine.Engine
	Router  engine.Router
	// Load reads the team again before each session, so changes made on
	// screen (a new agent, new access) apply without a restart.
	Load func() (team.Team, error)
}

type session struct {
	Info
	events  []Wire
	pending map[string]Pending
	replies map[string]chan<- bool
	cancel  context.CancelFunc
	running bool
	seen    bool
	retry   bool
	code    *repo.Set
}

// Server keeps the team's sessions and routines.
type Server struct {
	opt     Options
	token   string
	started time.Time

	mu        sync.Mutex
	sessions  map[string]*session
	nextID    int
	notes     []Note
	nextNote  int
	changed   chan struct{} // closed on every change, then replaced
	lastStart map[string]time.Time
	retryAt   map[string]time.Time
	done      chan struct{}
	pumps     sync.WaitGroup // sessions still recording, so closing waits for their save
	panes     *pane.Manager  // real terminals (see panes.go)

	// Notifications (see watch).
	lastClient time.Time         // when a window last asked for the status
	states     map[string]string // what each pane and session was at the last look
	Notifier   func(title, body string)
}

// New prepares a server and reads the sessions saved last time.
func New(opt Options) *Server {
	s := &Server{opt: opt, started: time.Now(), sessions: map[string]*session{}, changed: make(chan struct{}),
		lastStart: map[string]time.Time{}, retryAt: map[string]time.Time{}, done: make(chan struct{})}
	s.panes = pane.NewManager(opt.TeamDir)
	s.panes.Resolve = Resolve
	home, _ := os.UserHomeDir()
	s.panes.Detectors = pane.LoadDetectors(filepath.Join(home, ".tubas", "detect"), filepath.Join(opt.TeamDir, "detect"))
	s.states = map[string]string{}
	s.Notifier = func(title, body string) { notify.Send(title, body) }
	s.loadSaved()
	return s
}

func (s *Server) dir() string { return filepath.Join(s.opt.TeamDir, ".tubas") }

// notify wakes everyone waiting for a change. Call with s.mu held.
func (s *Server) notify() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (ss *session) state() string {
	switch {
	case len(ss.pending) > 0:
		return Blocked
	case ss.running:
		return Working
	case !ss.seen:
		return Done
	default:
		return Idle
	}
}

func (ss *session) info() Info {
	in := ss.Info
	if ss.code != nil && ss.running {
		in.Worktrees = ss.code.Worktrees()
	}
	in.State = ss.state()
	in.Events = len(ss.events)
	in.Waiting = ""
	for _, p := range ss.pending {
		in.Waiting = p.Call
	}
	return in
}

// Start runs a new session in the background.
func (s *Server) Start(req StartRequest) (Info, error) {
	return s.start(req, "chat", "", false)
}

func (s *Server) start(req StartRequest, kind, routineName string, retry bool) (Info, error) {
	if strings.TrimSpace(req.Question) == "" {
		return Info{}, errors.New("empty question")
	}
	t, err := s.opt.Load()
	if err != nil {
		return Info{}, err
	}
	members := map[string]agent.Agent{}
	for _, a := range t.Agents {
		members[a.Name] = a
	}
	if req.Target != "" {
		if _, ok := members[req.Target]; !ok {
			return Info{}, fmt.Errorf("no agent named %s", req.Target)
		}
	}
	routing := t.Routing
	if kind == "routine" {
		routing = "lead"
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan engine.Event, 64)

	s.mu.Lock()
	s.nextID++
	title := req.Title
	if title == "" {
		title = shortTitle(req.Question)
	}
	ss := &session{Info: Info{ID: "s" + strconv.Itoa(s.nextID), Title: title, Kind: kind, Routine: routineName,
		Target: req.Target, Question: req.Question, Created: time.Now(), Updated: time.Now()},
		pending: map[string]Pending{}, replies: map[string]chan<- bool{}, cancel: cancel, running: true, retry: retry}
	ss.Conversation = req.Conversation
	if ss.Conversation == "" {
		ss.Conversation = ss.ID // a new conversation starts with it
	}
	repos, specs := repoSpecs(t)
	ss.code = repo.NewSet(t.Dir, ss.ID, title, specs)
	s.sessions[ss.ID] = ss
	s.notify()
	info := ss.info()
	s.mu.Unlock()

	go engine.Run(ctx, s.opt.Engine, engine.Request{Team: members, Target: req.Target, Everyone: req.Everyone,
		Question: req.Question, MaxHops: t.MaxHops, Context: req.Context, Lead: t.Lead, Routing: routing, Router: s.opt.Router,
		Repos: repos, Workspace: ss.code.Workspace}, events)
	s.pumps.Add(1)
	go s.pump(ss, events)
	return info, nil
}

// pump records a session's events as they come.
func (s *Server) pump(ss *session, events <-chan engine.Event) {
	defer s.pumps.Done()
	for e := range events {
		w := Encode(e)
		s.mu.Lock()
		w.Seq = len(ss.events)
		ss.Updated = time.Now()
		switch e := e.(type) {
		case engine.ApprovalRequested:
			w.Approval = ss.ID + "-" + strconv.Itoa(w.Seq)
			ss.pending[w.Approval] = Pending{ID: w.Approval, Session: ss.ID, Title: ss.Title, Routine: ss.Routine,
				Agent: e.Agent, Call: e.Call, Detail: e.Detail, At: time.Now()}
			ss.replies[w.Approval] = e.Reply
			ss.seen = false
		case engine.TurnFinished:
			ss.Tokens += e.Tokens
			ss.Cost += e.Cost
			if e.Summary != "" {
				ss.Agent, ss.Summary = e.Agent, e.Summary
			}
		case engine.SessionDone:
			ss.Outcome = "ok"
		case engine.HopLimitReached:
			ss.Outcome = "hop_limit"
		case engine.Failed:
			ss.Outcome, ss.Error = "failed", w.Error
		}
		ss.events = append(ss.events, w)
		s.notify()
		s.mu.Unlock()
	}
	s.mu.Lock()
	ss.running = false
	ss.seen = false
	ss.Worktrees = ss.code.Worktrees()
	if ss.Outcome == "" {
		ss.Outcome = "stopped"
	}
	for id := range ss.pending { // nobody can answer a finished session
		delete(ss.pending, id)
		delete(ss.replies, id)
	}
	s.notify()
	s.mu.Unlock()
	if ss.Kind == "routine" {
		s.routineFinished(ss)
	}
	s.save(ss)
}

// Answer sends your answer to a waiting approval.
func (s *Server) Answer(id string, approved bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.sessions {
		reply, ok := ss.replies[id]
		if !ok {
			continue
		}
		reply <- approved // room for 1: never blocks
		delete(ss.replies, id)
		delete(ss.pending, id)
		w := Wire{Seq: len(ss.events), Kind: "answered", Approval: id, Failed: !approved}
		ss.events = append(ss.events, w)
		s.notify()
		return nil
	}
	return fmt.Errorf("no approval %s is waiting", id)
}

// Cancel stops a session.
func (s *Server) Cancel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("no session %s", id)
	}
	ss.cancel()
	return nil
}

// Seen marks a session as looked at: done becomes idle.
func (s *Server) Seen(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss, ok := s.sessions[id]; ok && !ss.seen {
		ss.seen = true
		s.notify()
	}
}

// Events returns a session's events from seq on. With wait > 0 and
// nothing new, it waits for news that long (long polling).
func (s *Server) Events(id string, from int, wait time.Duration) ([]Wire, Info, error) {
	deadline := time.Now().Add(wait)
	for {
		s.mu.Lock()
		ss, ok := s.sessions[id]
		if !ok {
			s.mu.Unlock()
			return nil, Info{}, fmt.Errorf("no session %s", id)
		}
		if from < len(ss.events) || !ss.running || time.Now().After(deadline) {
			out := append([]Wire(nil), ss.events[min(from, len(ss.events)):]...)
			info := ss.info()
			s.mu.Unlock()
			return out, info, nil
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(time.Until(deadline)):
		case <-s.done:
			return nil, Info{}, errors.New("server stopped")
		}
	}
}

// Wait returns when the session reaches one of the states, or timeout.
func (s *Server) Wait(id string, states []string, timeout time.Duration) (Info, error) {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		ss, ok := s.sessions[id]
		if !ok {
			s.mu.Unlock()
			return Info{}, fmt.Errorf("no session %s", id)
		}
		info := ss.info()
		ch := s.changed
		s.mu.Unlock()
		for _, st := range states {
			if info.State == st {
				return info, nil
			}
		}
		if timeout > 0 && time.Now().After(deadline) {
			return info, errors.New("timeout")
		}
		wait := 30 * time.Second
		if timeout > 0 {
			wait = time.Until(deadline)
		}
		select {
		case <-ch:
		case <-time.After(wait):
		case <-s.done:
			return info, errors.New("server stopped")
		}
	}
}

// Status is everything, newest sessions first.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Dir: s.opt.TeamDir, Started: s.started, PID: os.Getpid(), Notes: append([]Note(nil), s.notes...)}
	if t, err := s.opt.Load(); err == nil {
		st.Team = t.Name
	}
	for _, ss := range s.sessions {
		st.Sessions = append(st.Sessions, ss.info())
		for _, p := range ss.pending {
			st.Pending = append(st.Pending, p)
		}
	}
	sort.Slice(st.Sessions, func(i, j int) bool { return st.Sessions[i].Created.After(st.Sessions[j].Created) })
	sort.Slice(st.Pending, func(i, j int) bool { return st.Pending[i].At.Before(st.Pending[j].At) })
	st.Panes, _ = s.panes.List()
	if f, err := routine.Load(s.opt.TeamDir); err == nil {
		for _, r := range f.Routines {
			ri := RoutineInfo{Name: r.Name, Agent: r.Agent, On: r.On(), Next: s.nextRun(r, f.QuietHours)}
			for _, ss := range s.sessions {
				ri.Running = ri.Running || (ss.Routine == r.Name && ss.running)
			}
			st.Routines = append(st.Routines, ri)
		}
	}
	return st
}

// DismissNote removes a routine's note.
func (s *Server) DismissNote(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.notes {
		if n.ID == id {
			s.notes = append(s.notes[:i:i], s.notes[i+1:]...)
			s.notify()
			return
		}
	}
}

// ── routines ────────────────────────────────────────────────────────────

// nextRun is when a routine runs next. Call with s.mu held.
func (s *Server) nextRun(r routine.Routine, quiet string) time.Time {
	if !r.On() {
		return time.Time{}
	}
	if at, ok := s.retryAt[r.Name]; ok {
		return at
	}
	sched, err := routine.Parse(r.Every, r.Between)
	if err != nil {
		return time.Time{}
	}
	base := s.started
	if last, ok := s.lastStart[r.Name]; ok && last.After(base) {
		base = last
	} else if run, ok := routine.LastRun(s.opt.TeamDir, r.Name); ok && run.Started.After(base) {
		base = run.Started
	}
	return routine.NextOutsideQuiet(sched, quiet, base)
}

// RunRoutine starts a routine now.
func (s *Server) RunRoutine(name string) (Info, error) {
	f, err := routine.Load(s.opt.TeamDir)
	if err != nil {
		return Info{}, err
	}
	r, i := f.Find(name)
	if i < 0 {
		return Info{}, fmt.Errorf("no routine named %s", name)
	}
	return s.startRoutine(r, false)
}

func (s *Server) startRoutine(r routine.Routine, retry bool) (Info, error) {
	s.mu.Lock()
	s.lastStart[r.Name] = time.Now()
	delete(s.retryAt, r.Name)
	s.mu.Unlock()
	return s.start(StartRequest{Target: r.Agent, Question: r.Prompt, Title: r.Name}, "routine", r.Name, retry)
}

// checkRoutines starts the routines that are due.
func (s *Server) checkRoutines(now time.Time) {
	f, err := routine.Load(s.opt.TeamDir)
	if err != nil {
		return
	}
	for _, r := range f.Routines {
		s.mu.Lock()
		running := false
		for _, ss := range s.sessions {
			running = running || (ss.Routine == r.Name && ss.running)
		}
		next := s.nextRun(r, f.QuietHours)
		_, retry := s.retryAt[r.Name]
		s.mu.Unlock()
		if running || next.IsZero() || next.After(now) {
			continue
		}
		if _, err := s.startRoutine(r, retry); err != nil {
			s.addNote(Note{Routine: r.Name, Text: err.Error(), Failed: true})
		}
	}
}

func (s *Server) routineFinished(ss *session) {
	failed := ss.Outcome != "ok"
	rec := routine.Run{Routine: ss.Routine, Agent: ss.Agent, Started: ss.Created, Finished: time.Now(),
		Outcome: "ok", Summary: ss.Summary, Tokens: ss.Tokens, Cost: ss.Cost, Retry: ss.retry}
	if rec.Agent == "" {
		rec.Agent = ss.Target
	}
	if failed {
		rec.Outcome, rec.Error = "failed", orText(ss.Error, ss.Outcome)
	}
	routine.SaveRun(s.opt.TeamDir, rec)
	text := ss.Summary
	if failed {
		text = rec.Error
		if f, err := routine.Load(s.opt.TeamDir); err == nil && f.Retry && !ss.retry {
			at := time.Now().Add(5 * time.Minute)
			s.mu.Lock()
			s.retryAt[ss.Routine] = at
			s.mu.Unlock()
			text += " · trying again at " + at.Format("15:04")
		}
	}
	if !failed && strings.EqualFold(strings.TrimSpace(ss.Summary), "PASS") {
		return // nothing worth saying
	}
	s.addNote(Note{Routine: ss.Routine, Text: text, Failed: failed})
}

func (s *Server) addNote(n Note) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextNote++
	n.ID, n.At = "n"+strconv.Itoa(s.nextNote), time.Now()
	s.notes = append(s.notes, n)
	s.notify()
}

func orText(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ── saving sessions across restarts ─────────────────────────────────────

type saved struct {
	Info   Info   `json:"info"`
	Events []Wire `json:"events"`
}

// save writes a finished session, so a restart brings it back.
func (s *Server) save(ss *session) {
	s.mu.Lock()
	rec := saved{Info: ss.info(), Events: compact(ss.events)}
	s.mu.Unlock()
	dir := filepath.Join(s.dir(), "sessions")
	os.MkdirAll(dir, 0o755)
	data, _ := json.Marshal(rec)
	tmp := filepath.Join(dir, ss.ID+".json.tmp")
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, filepath.Join(dir, ss.ID+".json"))
	}
}

// compact joins consecutive text pieces of the same agent: a saved
// session doesn't need every token as its own event.
func compact(events []Wire) []Wire {
	var out []Wire
	for _, w := range events {
		if n := len(out); n > 0 && w.Kind == "text" && out[n-1].Kind == "text" && out[n-1].Agent == w.Agent {
			out[n-1].Text += w.Text
			continue
		}
		out = append(out, w)
	}
	for i := range out {
		out[i].Seq = i
	}
	return out
}

func (s *Server) loadSaved() {
	files, _ := filepath.Glob(filepath.Join(s.dir(), "sessions", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rec saved
		if json.Unmarshal(data, &rec) != nil || rec.Info.ID == "" {
			continue
		}
		ss := &session{Info: rec.Info, events: rec.Events, pending: map[string]Pending{}, replies: map[string]chan<- bool{},
			cancel: func() {}, seen: rec.Info.State == Idle}
		s.sessions[ss.ID] = ss
		if n, err := strconv.Atoi(strings.TrimPrefix(ss.ID, "s")); err == nil && n > s.nextID {
			s.nextID = n
		}
	}
}

// ── serving ─────────────────────────────────────────────────────────────

// File is .tubas/server.json: how clients find the server.
type File struct {
	URL     string    `json:"url"`
	Token   string    `json:"token"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Build   string    `json:"build,omitempty"` // which tubas binary runs it: see Build
}

// FilePath is where a team's server.json lives.
func FilePath(teamDir string) string { return filepath.Join(teamDir, ".tubas", "server.json") }

// Run serves until ctx ends or a client asks it to stop.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	secret := make([]byte, 16)
	rand.Read(secret)
	s.token = hex.EncodeToString(secret)
	srv := &http.Server{Handler: s.Handler()}
	f := File{URL: "http://" + ln.Addr().String(), Token: s.token, PID: os.Getpid(), Started: s.started, Build: Build()}
	os.MkdirAll(s.dir(), 0o755)
	data, _ := json.Marshal(f)
	if err := os.WriteFile(FilePath(s.opt.TeamDir), data, 0o600); err != nil {
		return err
	}
	defer os.Remove(FilePath(s.opt.TeamDir))

	// Panes that were open when the server went down come back.
	if n, errs := s.panes.Restore(); n > 0 || len(errs) > 0 {
		fmt.Printf("restored %d panes\n", n)
		for _, e := range errs {
			fmt.Println("  not restored:", e)
		}
	}
	go srv.Serve(ln)
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	look := time.NewTicker(time.Second)
	defer look.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.shutdown(srv)
		case <-s.done:
			return s.shutdown(srv)
		case now := <-tick.C:
			s.checkRoutines(now)
		case <-look.C:
			s.watch()
		}
	}
}

func (s *Server) shutdown(srv *http.Server) error {
	s.mu.Lock()
	for _, ss := range s.sessions {
		if ss.running {
			ss.cancel()
		}
	}
	s.mu.Unlock()
	s.panes.CloseAll()
	s.waitPumps(3 * time.Second) // let stopped sessions save
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// Close stops every session and waits until each one is saved.
func (s *Server) Close() {
	s.Stop()
	s.mu.Lock()
	for _, ss := range s.sessions {
		if ss.running {
			ss.cancel()
		}
	}
	s.mu.Unlock()
	s.panes.CloseAll()
	s.waitPumps(10 * time.Second)
}

func (s *Server) waitPumps(limit time.Duration) {
	done := make(chan struct{})
	go func() { s.pumps.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
	}
}

// Stop ends Run.
func (s *Server) Stop() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// SetToken is for tests that serve the handler themselves.
func (s *Server) SetToken(t string) { s.token = t }

// Handler is the HTTP API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any, err error) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("window") == "1" {
			s.mu.Lock()
			s.lastClient = time.Now() // a window is open
			s.mu.Unlock()
		}
		reply(w, s.Status(), nil)
	})
	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		var req StartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		info, err := s.Start(req)
		reply(w, info, err)
	})
	mux.HandleFunc("GET /sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		from, _ := strconv.Atoi(r.URL.Query().Get("from"))
		secs, _ := strconv.Atoi(r.URL.Query().Get("wait"))
		events, info, err := s.Events(r.PathValue("id"), from, time.Duration(min(secs, 60))*time.Second)
		reply(w, map[string]any{"events": events, "info": info}, err)
	})
	mux.HandleFunc("POST /sessions/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]bool{"ok": true}, s.Cancel(r.PathValue("id")))
	})
	mux.HandleFunc("POST /sessions/{id}/seen", func(w http.ResponseWriter, r *http.Request) {
		s.Seen(r.PathValue("id"))
		reply(w, map[string]bool{"ok": true}, nil)
	})
	mux.HandleFunc("GET /sessions/{id}/wait", func(w http.ResponseWriter, r *http.Request) {
		secs, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
		states := strings.Split(r.URL.Query().Get("until"), ",")
		info, err := s.Wait(r.PathValue("id"), states, time.Duration(secs)*time.Second)
		reply(w, info, err)
	})
	mux.HandleFunc("POST /approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Approved bool `json:"approved"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		reply(w, map[string]bool{"ok": true}, s.Answer(r.PathValue("id"), body.Approved))
	})
	mux.HandleFunc("POST /routines/{name}/run", func(w http.ResponseWriter, r *http.Request) {
		info, err := s.RunRoutine(r.PathValue("name"))
		reply(w, info, err)
	})
	mux.HandleFunc("DELETE /notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.DismissNote(r.PathValue("id"))
		reply(w, map[string]bool{"ok": true}, nil)
	})
	s.paneRoutes(mux, reply)
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]bool{"ok": true}, nil)
		go s.Stop()
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// repoSpecs are the team's repositories, for the engine and the worktrees.
func repoSpecs(t team.Team) (map[string]string, map[string]repo.Spec) {
	paths, specs := map[string]string{}, map[string]repo.Spec{}
	for name, r := range t.Repos() {
		paths[name] = r.Path
		specs[name] = repo.Spec{Path: r.Path, Base: r.Base, Setup: r.Setup}
	}
	return paths, specs
}

// shortTitle is the first words of a question, for the session list.
func shortTitle(q string) string {
	q = strings.TrimSpace(q)
	if strings.HasPrefix(q, "@") { // "@leo corrige o timeout" -> "corrige o timeout"
		if _, rest, ok := strings.Cut(q, " "); ok {
			q = rest
		}
	}
	// The whole question when it fits; else cut at a word, with "…".
	t := strings.Join(strings.Fields(q), " ")
	if r := []rune(t); len(r) > 40 {
		cut := string(r[:40])
		if i := strings.LastIndex(cut, " "); i > 20 {
			cut = cut[:i]
		}
		t = strings.TrimRight(cut, " ,.;:") + "…"
	}
	return t
}
