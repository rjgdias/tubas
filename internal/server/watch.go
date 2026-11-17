package server

import (
	"fmt"
	"time"

	"tubas/internal/pane"
)

// ── notifications ───────────────────────────────────────────────────────
//
// Every second the server looks at each pane and session. When one starts
// waiting for you (or an agent finishes) and no window is open, it sends
// a desktop notification. With a window open, the window tells you
// itself. team.yaml's notify says how loud: all (default), window (only
// in the window) or off.

// Alert is something worth telling you.
type Alert struct {
	Key, Text string
}

// alerts compares the states now with the last look.
func (s *Server) alerts() []Alert {
	panes, _ := s.panes.List()
	st := s.Status()
	now := map[string]string{}
	var out []Alert
	change := func(key, state string) (string, bool) {
		now[key] = state
		was, seen := s.states[key]
		return was, seen && was != state
	}
	for _, p := range panes {
		was, changed := change("pane:"+p.ID, p.State)
		switch {
		case changed && p.State == pane.Blocked:
			out = append(out, Alert{"pane:" + p.ID, p.Title + " is waiting for you"})
		case changed && p.State == pane.Done && was == pane.Working && p.Exit == "":
			out = append(out, Alert{"pane:" + p.ID, p.Title + " finished"})
		}
	}
	for _, ss := range st.Sessions {
		_, changed := change("session:"+ss.ID, ss.State)
		switch {
		case changed && ss.State == Blocked:
			out = append(out, Alert{"session:" + ss.ID, ss.Title + ": " + ss.Agent + " wants to run " + ss.Waiting})
		case changed && ss.State == Done:
			out = append(out, Alert{"session:" + ss.ID, ss.Title + " is done"})
		}
	}
	s.states = now
	return out
}

// watch sends the alerts as desktop notifications when nobody is looking.
func (s *Server) watch() {
	alerts := s.alerts()
	if len(alerts) == 0 {
		return
	}
	s.mu.Lock()
	away := time.Since(s.lastClient) > 8*time.Second
	s.mu.Unlock()
	mode, name := "all", "tubas"
	if t, err := s.opt.Load(); err == nil {
		if t.Notify != "" {
			mode = t.Notify
		}
		name = "tubas · " + t.Name
	}
	if mode != "all" || !away || s.Notifier == nil {
		return
	}
	for _, a := range alerts {
		fmt.Println(time.Now().Format(time.RFC3339), "notified:", a.Text)
		s.Notifier(name, a.Text)
	}
}
