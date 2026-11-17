package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Client talks to a team's server.
type Client struct {
	URL, Token string
	HTTP       *http.Client

	// Window says this client is a tubas window: while one asks for the
	// status, the server leaves notifications to it.
	Window bool

	// Outdated says the server runs an older tubas than this one and was
	// kept, because something was running in it.
	Outdated bool
}

// Build tells tubas binaries apart: the executable's size and time. A
// new go install changes it, so a window can tell that the server in the
// background still runs the old code.
var Build = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	st, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", st.Size(), st.ModTime().UnixNano())
})

// Discover finds a running server for the team, or says why not.
func Discover(teamDir string) (*Client, error) {
	c, _, _, err := discover(teamDir)
	return c, err
}

func discover(teamDir string) (*Client, File, Status, error) {
	var f File
	data, err := os.ReadFile(FilePath(teamDir))
	if err != nil {
		return nil, f, Status{}, errors.New("no server running")
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, f, Status{}, err
	}
	c := &Client{URL: f.URL, Token: f.Token, HTTP: &http.Client{}}
	st, err := c.statusWithin(2 * time.Second)
	if err != nil {
		return nil, f, st, fmt.Errorf("the server in %s does not answer: %w", FilePath(teamDir), err)
	}
	return c, f, st, nil
}

type serverAction int

const (
	useIt        serverAction = iota // it runs this tubas
	replaceIt                        // an older tubas, idle: start a new one
	keepOutdated                     // an older tubas, busy: keep it, say so
)

// whatToDo decides about a running server, given the build it runs.
func whatToDo(f File, st Status, build string) serverAction {
	switch {
	case f.Build == build:
		return useIt
	case busy(st):
		return keepOutdated
	}
	return replaceIt
}

// busy says whether stopping the server would interrupt you: a session
// running or waiting for you, or a terminal pane open.
func busy(st Status) bool {
	for _, s := range st.Sessions {
		if s.State == Working || s.State == Blocked {
			return true
		}
	}
	return len(st.Panes) > 0
}

// Ensure returns the team's server, starting one in the background when
// none runs. started says whether this call started it.
//
// A server started by an older tubas (before a go install) is replaced
// when nothing runs in it, so a new version never talks to old code;
// otherwise it is kept, and Outdated says so.
func Ensure(teamDir, exe string, args ...string) (c *Client, started bool, err error) {
	if c, f, st, err := discover(teamDir); err == nil {
		switch whatToDo(f, st, Build()) {
		case useIt:
			return c, false, nil
		case keepOutdated:
			c.Outdated = true
			return c, false, nil
		}
		c.StopServer()
		for range 50 { // up to 5 s for it to go
			if _, err := os.Stat(FilePath(teamDir)); err != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	os.Remove(FilePath(teamDir)) // a stale file from a server that died
	os.MkdirAll(filepath.Join(teamDir, ".tubas"), 0o755)
	logf, err := os.OpenFile(filepath.Join(teamDir, ".tubas", "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, false, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, append([]string{"serve", "-team", teamDir}, args...)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	detach(cmd) // its own process group: it outlives this terminal
	if err := cmd.Start(); err != nil {
		return nil, false, fmt.Errorf("starting the server: %w", err)
	}
	cmd.Process.Release()
	for range 100 { // up to 10 s
		time.Sleep(100 * time.Millisecond)
		if c, err := Discover(teamDir); err == nil {
			return c, true, nil
		}
	}
	return nil, false, fmt.Errorf("the server did not start: see %s", filepath.Join(teamDir, ".tubas", "server.log"))
}

func (c *Client) do(method, path string, body, out any, timeout time.Duration) error {
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.URL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	if timeout > 0 {
		cl := *client
		cl.Timeout = timeout
		client = &cl
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) statusWithin(d time.Duration) (Status, error) {
	var st Status
	path := "/status"
	if c.Window {
		path += "?window=1"
	}
	err := c.do("GET", path, nil, &st, d)
	return st, err
}

// Status is the whole picture.
func (c *Client) Status() (Status, error) { return c.statusWithin(10 * time.Second) }

// Start runs a new session.
func (c *Client) Start(req StartRequest) (Info, error) {
	var info Info
	err := c.do("POST", "/sessions", req, &info, 30*time.Second)
	return info, err
}

// Events returns a session's events from seq on, waiting up to wait for
// news when there is none yet.
func (c *Client) Events(id string, from int, wait time.Duration) ([]Wire, Info, error) {
	var out struct {
		Events []Wire `json:"events"`
		Info   Info   `json:"info"`
	}
	path := fmt.Sprintf("/sessions/%s/events?from=%d&wait=%d", url.PathEscape(id), from, int(wait.Seconds()))
	err := c.do("GET", path, nil, &out, wait+15*time.Second)
	return out.Events, out.Info, err
}

// Cancel stops a session.
func (c *Client) Cancel(id string) error {
	return c.do("POST", "/sessions/"+url.PathEscape(id)+"/cancel", nil, nil, 10*time.Second)
}

// Seen marks a session as looked at.
func (c *Client) Seen(id string) error {
	return c.do("POST", "/sessions/"+url.PathEscape(id)+"/seen", nil, nil, 10*time.Second)
}

// Answer answers an approval.
func (c *Client) Answer(id string, approved bool) error {
	return c.do("POST", "/approvals/"+url.PathEscape(id), map[string]bool{"approved": approved}, nil, 10*time.Second)
}

// Wait returns when the session reaches one of the states (timeout 0:
// wait as long as it takes).
func (c *Client) Wait(id string, states []string, timeout time.Duration) (Info, error) {
	var info Info
	for {
		chunk := 50 * time.Second
		if timeout > 0 && timeout < chunk {
			chunk = timeout
		}
		path := fmt.Sprintf("/sessions/%s/wait?until=%s&timeout=%d", url.PathEscape(id), url.QueryEscape(strings.Join(states, ",")), int(chunk.Seconds()))
		err := c.do("GET", path, nil, &info, chunk+15*time.Second)
		if err == nil {
			return info, nil
		}
		if err.Error() != "timeout" {
			return info, err
		}
		if timeout > 0 {
			if timeout -= chunk; timeout <= 0 {
				return info, err
			}
		}
	}
}

// RunRoutine starts a routine now.
func (c *Client) RunRoutine(name string) (Info, error) {
	var info Info
	err := c.do("POST", "/routines/"+url.PathEscape(name)+"/run", nil, &info, 30*time.Second)
	return info, err
}

// DismissNote removes a routine's note.
// RenameConversation gives a conversation a new title.
func (c *Client) RenameConversation(id, title string) error {
	return c.do("PATCH", "/conversations/"+url.PathEscape(id), map[string]string{"title": title}, nil, 10*time.Second)
}

// DeleteConversation removes a conversation and every message in it.
func (c *Client) DeleteConversation(id string) error {
	return c.do("DELETE", "/conversations/"+url.PathEscape(id), nil, nil, 10*time.Second)
}

func (c *Client) DismissNote(id string) error {
	return c.do("DELETE", "/notes/"+url.PathEscape(id), nil, nil, 10*time.Second)
}

// StopServer ends the server and every session in it.
func (c *Client) StopServer() error { return c.do("POST", "/stop", nil, nil, 10*time.Second) }
