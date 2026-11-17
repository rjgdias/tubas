package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tubas/internal/engine"
	"tubas/internal/server"
)

// clickAt presses and lets go of the left button at a screen cell.
func clickAt(m Model, x, y int) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = next.(Model)
	next, cmd2 := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	return next.(Model), tea.Batch(cmd, cmd2)
}

func TestClickTheTopBarAndAList(t *testing.T) {
	m := feed(teamModel(t, nil), tea.WindowSizeMsg{Width: 120, Height: 32})
	// " 1 chat " " 2 inbox " take 17 cells; " 3 agents " comes next.
	m, _ = clickAt(m, bodyX+20, 1)
	if m.section != SecAgents {
		t.Fatalf("a click on 3 agents opens it, section %v", m.section)
	}
	m, _ = clickAt(m, bodyX+2, bodyY+3) // the second agent in the list
	if m.secIdx[SecAgents] != 1 {
		t.Errorf("a click on a row selects it, got %d", m.secIdx[SecAgents])
	}
	m, _ = clickAt(m, bodyX+3, 1)
	if m.section != SecChat {
		t.Error("a click on 1 chat goes back")
	}
}

func TestClickAnswersAnApproval(t *testing.T) {
	m := feed(teamModel(t, approvalScriptsUI()), tea.WindowSizeMsg{Width: 120, Height: 32})
	m = m.Ask("@leo publica").Settle(0)
	t.Cleanup(m.Stop)
	if m.session.Pending == nil {
		t.Fatal("leo should wait for an approval")
	}
	bar := bodyY + m.bodyHeight() + len(m.viewPopup())
	cw := m.width - 2
	start := cw - len("y approve   n deny   v details ")
	m, _ = clickAt(m, bodyX+start+len("y approve   ")+1, bar) // on "n deny"
	if len(m.queue) != 0 {
		t.Fatal("a click on n denies")
	}
	m = m.Settle(0)
	contains(t, ansi.Strip(m.Snapshot(120, 32)), "no")
}

func TestClickPaneHeader(t *testing.T) {
	m, _ := paneModelSized(t, 100, 30, nil)
	m = openPane(t, m, "v")
	// The chat's header: a double click zooms it.
	m, _ = clickAt(m, bodyX+5, bodyY)
	m, _ = clickAt(m, bodyX+5, bodyY)
	if !m.panes.cur().Zoom || m.panes.focus() != chatPane {
		t.Fatal("a double click on a header zooms that pane")
	}
	m, _ = clickAt(m, bodyX+5, bodyY)
	m, _ = clickAt(m, bodyX+5, bodyY)
	if m.panes.cur().Zoom {
		t.Fatal("another double click goes back")
	}
	m.panes.lastClick.at = time.Time{}
	// p1 takes columns 49 to 97 of the body; its ✕ is at the end.
	m, _ = clickAt(m, bodyX+60, bodyY+5)
	m, _ = clickAt(m, bodyX+97, bodyY)
	if m.panes.closing != "p1" {
		t.Errorf("the ✕ of a running pane asks before closing, closing = %q", m.panes.closing)
	}
}

func TestClickSessionsInTheSidebar(t *testing.T) {
	scripts := map[string]engine.Script{"leo": {Summary: "feito"}}
	m, _, c := remoteModel(t, scripts)
	host := &fakeHost{}
	m.cfg.Panes = host
	m.panes = newPaneState(host)
	m = feed(m, tea.WindowSizeMsg{Width: 140, Height: 32})
	m, _ = m.panesLoaded(panesLoadedMsg{})
	var ids []string
	for _, q := range []string{"@leo corrige o timeout", "@leo revisa o PR"} {
		info, err := c.Start(server.StartRequest{Target: "leo", Question: q})
		if err != nil {
			t.Fatal(err)
		}
		c.Wait(info.ID, []string{server.Done}, 5*time.Second)
		ids = append(ids, info.ID)
	}
	m = feed(m, status(t, c))
	t.Cleanup(m.Stop)
	if !m.sidebarShown() {
		t.Fatal("with sessions on the server, the sidebar shows them")
	}
	frame := ansi.Strip(m.Snapshot(140, 32))
	contains(t, frame, "sessions", "corrige o timeout", "revisa o PR", "+ new session")
	_, rows := m.sidebarLines(sidebarW, m.bodyHeight())
	row := -1
	for i, r := range rows {
		if r.session == ids[0] {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("the session should have a row")
	}
	m, _ = clickAt(m, bodyX+4, bodyY+row)
	if m.remoteID != ids[0] || m.panes.focus() != chatPane {
		t.Errorf("a click on a session shows it in the chat: on screen %q", m.remoteID)
	}
	// The picker (ctrl+g) takes clicks too.
	m, _ = key(m, "ctrl+g")
	if !m.picker {
		t.Fatal("ctrl+g opens the picker")
	}
	m, _ = clickAt(m, bodyX+4, bodyY+2) // the newest session, first in the list
	if m.picker || m.remoteID != ids[1] {
		t.Errorf("a click on a picker row shows that session: %q", m.remoteID)
	}
	if !strings.Contains(frame, "+ new tab") {
		t.Error("the sidebar offers a new tab")
	}
}
