package ui

import (
	"net/http/httptest"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tubas/internal/engine"
	"tubas/internal/server"
	"tubas/internal/team"
)

// remoteModel is a screen attached to a real server (fake engine) over
// HTTP, like tubas with its background server.
func remoteModel(t *testing.T, scripts map[string]engine.Script) (Model, *server.Server, *server.Client) {
	t.Helper()
	local := teamModel(t, scripts)
	dir := local.cfg.TeamDir
	s := server.New(server.Options{TeamDir: dir, Engine: engine.FakeEngine{Scripts: scripts}, Router: engine.RuleRouter{},
		Load: func() (team.Team, error) { return team.Load(dir) }})
	s.SetToken("t")
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.Close(); hs.Close() })
	c := &server.Client{URL: hs.URL, Token: "t"}
	cfg := local.cfg
	cfg.Remote = c
	return New(local.team, engine.FakeEngine{}, cfg), s, c
}

func feed(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func status(t *testing.T, c *server.Client) statusMsg {
	st, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	return statusMsg(st)
}

func TestRemoteSessionRunsOnTheServer(t *testing.T) {
	m, _, c := remoteModel(t, approvalScriptsUI())
	m = m.Ask("@leo publica").Settle(0)
	t.Cleanup(m.Stop)
	if m.remoteID == "" {
		t.Fatal("the session should run on the server")
	}
	if len(m.queue) != 1 || m.queue[0].id == "" || m.session.Pending == nil {
		t.Fatalf("the approval should reach the chat with the server's id: %+v", m.queue)
	}
	// The server's status lists the same approval: no duplicate.
	m = feed(m, status(t, c))
	if len(m.queue) != 1 {
		t.Fatalf("the same approval must not show twice: %d", len(m.queue))
	}
	m = m.PressKeys("y").Settle(0)
	info, err := c.Wait(m.remoteID, []string{server.Done, server.Idle}, 5*time.Second)
	if err != nil || info.Outcome != "ok" {
		t.Fatalf("y should reach the server and the session finish: %+v, %v", info, err)
	}
	contains(t, m.Snapshot(128, 32), "publicado", "done")
}

// Closing the screen (or ctrl+b q) leaves the session running; opening
// it again shows the session that needs you.
func TestDetachAndAttachAgain(t *testing.T) {
	m, _, c := remoteModel(t, approvalScriptsUI())
	m = m.Ask("@leo publica").Settle(0)
	id := m.remoteID
	m.Stop() // what quitting does: stop listening, nothing else

	if _, err := c.Wait(id, []string{server.Blocked}, 5*time.Second); err != nil {
		t.Fatal("the session should still wait for the approval on the server")
	}
	again, _, _ := remoteModelSame(t, m)
	again = feed(again, status(t, c))
	if again.remoteID != id {
		t.Fatalf("opening again should attach to %s, got %q", id, again.remoteID)
	}
	again = again.Settle(0)
	t.Cleanup(again.Stop)
	if again.session.Pending == nil {
		t.Fatal("the replayed session should show its approval")
	}
	contains(t, again.Snapshot(128, 32), "publica", "publish_report")
}

// remoteModelSame opens a second screen on the same server.
func remoteModelSame(t *testing.T, m Model) (Model, *server.Server, *server.Client) {
	cfg := m.cfg
	return New(m.team, engine.FakeEngine{}, cfg), nil, cfg.Remote
}

func TestPickerSwitchesSessions(t *testing.T) {
	m, _, c := remoteModel(t, map[string]engine.Script{"leo": {Summary: "um"}, "nico": {Summary: "dois"}})
	m = m.Ask("@leo primeira").Settle(0)
	first := m.remoteID
	m = m.PressKeys("ctrl+n")
	if m.session.started() {
		t.Fatal("ctrl+n should clear the chat for a new session")
	}
	m = m.Ask("@nico segunda").Settle(0)
	if m.remoteID == first {
		t.Fatal("the second question should be a new session")
	}
	c.Wait(m.remoteID, []string{server.Done, server.Idle}, 5*time.Second)
	m = feed(m, status(t, c))
	m.picker = true
	frame := m.Snapshot(128, 32)
	contains(t, frame, "conversations", "primeira", "segunda", "on screen")

	idx := -1
	for i, c := range m.conversations() {
		if c.ID == first {
			idx = i
		}
	}
	m.pickIdx = idx
	next, _ := m.pickerKey("enter")
	m = next.Settle(0)
	t.Cleanup(m.Stop)
	if m.remoteID != first || m.session.Question != "primeira" {
		t.Fatalf("enter should attach to the first session: %s %q", m.remoteID, m.session.Question)
	}
	contains(t, m.Snapshot(128, 32), "um")
}

// An approval waiting in another session shows in the inbox, and the
// inbox answers it on the server.
func TestOtherSessionsApprovalsInTheInbox(t *testing.T) {
	m, _, c := remoteModel(t, approvalScriptsUI())
	other, err := c.Start(server.StartRequest{Target: "leo", Question: "publica pelo script"})
	if err != nil {
		t.Fatal(err)
	}
	c.Wait(other.ID, []string{server.Blocked}, 5*time.Second)
	m.status = &server.Status{} // not the first status: don't attach
	m = feed(m, status(t, c))
	if len(m.queue) != 1 || m.queue[0].routine == "" {
		t.Fatalf("the other session's approval should be in the inbox: %+v", m.queue)
	}
	contains(t, m.PressKeys("alt+2").Snapshot(128, 32), "publish_report", "publica pelo script")
	m = m.PressKeys("alt+2", "y")
	if _, err := c.Wait(other.ID, []string{server.Done}, 5*time.Second); err != nil {
		t.Fatal("the inbox's y should reach the server")
	}
	m = feed(m, status(t, c))
	if len(m.queue) != 0 {
		t.Error("an approval answered should leave the inbox")
	}
}

func TestCtrlBQDetaches(t *testing.T) {
	m, _, _ := remoteModel(t, approvalScriptsUI())
	m = m.PressKeys("ctrl+b")
	if !m.prefix {
		t.Fatal("ctrl+b should wait for the next key")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("ctrl+b q should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+b q should quit the screen")
	}
}

func approvalScriptsUI() map[string]engine.Script {
	return map[string]engine.Script{"leo": {Summary: "publicado",
		Approval: &engine.FakeApproval{Call: "publish_report(#dados)", OnApprove: "ok", OnDeny: "no"}}}
}
