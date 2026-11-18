package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"tubas/internal/pane"
)

// ── panes: real terminals that live in the server ───────────────────────
//
// GET    /panes                         every pane
// POST   /panes                         {spec, w, h}: open one
// GET    /panes/{id}/screen?since&wait  its screen, once newer than since (wait in ms)
// GET    /panes/{id}/text               its screen as plain text
// POST   /panes/{id}/keys               {keys}: type into it
// POST   /panes/{id}/resize             {w, h}
// DELETE /panes/{id}                    stop it
// GET    /layout, PUT /layout           how the screen arranges them

// OpenRequest opens a pane.
type OpenRequest struct {
	Spec pane.Spec `json:"spec"`
	W    int       `json:"w"`
	H    int       `json:"h"`
}

// openPane fills in a repository connector's folder from team.yaml.
func (s *Server) openPane(req OpenRequest) (pane.Info, error) {
	spec := req.Spec
	if spec.Repo != "" && spec.Dir == "" {
		t, err := s.opt.Load()
		if err != nil {
			return pane.Info{}, err
		}
		r, ok := t.Repos()[spec.Repo]
		if !ok {
			return pane.Info{}, fmt.Errorf("no repository named %s in team.yaml", spec.Repo)
		}
		spec.Dir, spec.Base, spec.Setup = r.Path, r.Base, r.Setup
	}
	if spec.Dir == "" {
		spec.Dir = s.opt.TeamDir
	}
	return s.panes.Open(spec, req.W, req.H)
}

func (s *Server) paneRoutes(mux *http.ServeMux, reply func(http.ResponseWriter, any, error)) {
	ok := map[string]bool{"ok": true}
	mux.HandleFunc("GET /panes", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.panes.List()
		reply(w, list, err)
	})
	mux.HandleFunc("POST /panes", func(w http.ResponseWriter, r *http.Request) {
		var req OpenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		info, err := s.openPane(req)
		reply(w, info, err)
	})
	mux.HandleFunc("GET /panes/{id}/screen", func(w http.ResponseWriter, r *http.Request) {
		since, err := strconv.Atoi(r.URL.Query().Get("since"))
		if err != nil {
			since = -1
		}
		ms, _ := strconv.Atoi(r.URL.Query().Get("wait"))
		scr, err := s.panes.Screen(r.PathValue("id"), since, time.Duration(min(ms, 60000))*time.Millisecond)
		reply(w, scr, err)
	})
	mux.HandleFunc("POST /panes/{id}/seen", func(w http.ResponseWriter, r *http.Request) {
		reply(w, ok, s.panes.Seen(r.PathValue("id")))
	})
	mux.HandleFunc("GET /panes/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		lines, plain, err := s.panes.History(r.PathValue("id"))
		reply(w, map[string][]string{"lines": lines, "plain": plain}, err)
	})
	mux.HandleFunc("GET /panes/{id}/text", func(w http.ResponseWriter, r *http.Request) {
		text, err := s.panes.Text(r.PathValue("id"))
		reply(w, map[string]string{"text": text}, err)
	})
	mux.HandleFunc("POST /panes/{id}/keys", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Keys []pane.Key `json:"keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			reply(w, nil, err)
			return
		}
		reply(w, ok, s.panes.Send(r.PathValue("id"), body.Keys))
	})
	mux.HandleFunc("POST /panes/{id}/resize", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ W, H int }
		json.NewDecoder(r.Body).Decode(&body)
		reply(w, ok, s.panes.Resize(r.PathValue("id"), body.W, body.H))
	})
	mux.HandleFunc("DELETE /panes/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, ok, s.panes.Close(r.PathValue("id")))
	})
	mux.HandleFunc("GET /layout", func(w http.ResponseWriter, r *http.Request) {
		l, err := s.panes.Layout()
		reply(w, map[string]string{"layout": l}, err)
	})
	mux.HandleFunc("PUT /layout", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Layout string `json:"layout"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		reply(w, ok, s.panes.SetLayout(body.Layout))
	})
}

// ── the client side ─────────────────────────────────────────────────────

// Panes is the server's panes, as a pane.Host for the screen.
func (c *Client) Panes() pane.Host { return remotePanes{c} }

// PaneText is a pane's screen as plain text.
func (c *Client) PaneText(id string) (string, error) {
	var out struct {
		Text string `json:"text"`
	}
	err := c.do("GET", "/panes/"+url.PathEscape(id)+"/text", nil, &out, 10*time.Second)
	return out.Text, err
}

type remotePanes struct{ c *Client }

func (r remotePanes) Open(spec pane.Spec, w, h int) (pane.Info, error) {
	var info pane.Info
	// A new worktree runs the repository's setup, which can take minutes.
	err := r.c.do("POST", "/panes", OpenRequest{spec, w, h}, &info, 20*time.Minute)
	return info, err
}

func (r remotePanes) List() ([]pane.Info, error) {
	var list []pane.Info
	err := r.c.do("GET", "/panes", nil, &list, 10*time.Second)
	return list, err
}

func (r remotePanes) Screen(id string, since int, wait time.Duration) (pane.Screen, error) {
	var scr pane.Screen
	path := fmt.Sprintf("/panes/%s/screen?since=%d&wait=%d", url.PathEscape(id), since, wait.Milliseconds())
	err := r.c.do("GET", path, nil, &scr, wait+15*time.Second)
	return scr, err
}

func (r remotePanes) Send(id string, keys []pane.Key) error {
	return r.c.do("POST", "/panes/"+url.PathEscape(id)+"/keys", map[string]any{"keys": keys}, nil, 10*time.Second)
}

func (r remotePanes) Resize(id string, w, h int) error {
	return r.c.do("POST", "/panes/"+url.PathEscape(id)+"/resize", map[string]int{"W": w, "H": h}, nil, 10*time.Second)
}

func (r remotePanes) Close(id string) error {
	return r.c.do("DELETE", "/panes/"+url.PathEscape(id), nil, nil, 10*time.Second)
}

func (r remotePanes) Seen(id string) error {
	return r.c.do("POST", "/panes/"+url.PathEscape(id)+"/seen", nil, nil, 10*time.Second)
}

func (r remotePanes) History(id string) ([]string, []string, error) {
	var out struct {
		Lines []string `json:"lines"`
		Plain []string `json:"plain"`
	}
	err := r.c.do("GET", "/panes/"+url.PathEscape(id)+"/history", nil, &out, 30*time.Second)
	return out.Lines, out.Plain, err
}

func (r remotePanes) Layout() (string, error) {
	var out struct {
		Layout string `json:"layout"`
	}
	err := r.c.do("GET", "/layout", nil, &out, 10*time.Second)
	return out.Layout, err
}

func (r remotePanes) SetLayout(layout string) error {
	return r.c.do("PUT", "/layout", map[string]string{"layout": layout}, nil, 10*time.Second)
}
