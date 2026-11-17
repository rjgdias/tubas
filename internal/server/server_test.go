package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"tubas/internal/engine"
	"tubas/internal/pane"
	"tubas/internal/routine"
	"tubas/internal/team"
)

// testServer copies the example team and serves it with the fake engine.
func testServer(t *testing.T, scripts map[string]engine.Script) (*Server, *Client, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("..", "..", "examples", "dados")
	for _, name := range []string{"team.yaml", "agents/iris.md", "agents/leo.md", "agents/nico.md"} {
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(dst), 0o755)
		os.WriteFile(dst, data, 0o644)
	}
	s := New(Options{TeamDir: dir, Engine: engine.FakeEngine{Scripts: scripts}, Router: engine.RuleRouter{},
		Load: func() (team.Team, error) { return team.Load(dir) }})
	s.SetToken("secret")
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.Close(); hs.Close() })
	return s, &Client{URL: hs.URL, Token: "secret"}, dir
}

func approvalScripts() map[string]engine.Script {
	return map[string]engine.Script{"leo": {Summary: "publicado",
		Steps:    []engine.FakeStep{{Call: "run_sql(SELECT 1)", Result: "1"}},
		Approval: &engine.FakeApproval{Call: "publish_report(#dados)", OnApprove: "ok", OnDeny: "no"}}}
}

func TestSessionLifecycle(t *testing.T) {
	_, c, _ := testServer(t, approvalScripts())
	info, err := c.Start(StartRequest{Target: "leo", Question: "@leo publica o relatório"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "publica o relatório" {
		t.Errorf("title = %q", info.Title)
	}
	blocked, err := c.Wait(info.ID, []string{Blocked}, 5*time.Second)
	if err != nil || blocked.Waiting == "" {
		t.Fatalf("the session should block on the approval: %+v, %v", blocked, err)
	}
	st, _ := c.Status()
	if len(st.Pending) != 1 || st.Pending[0].Agent != "leo" {
		t.Fatalf("status should list the approval: %+v", st.Pending)
	}
	if err := c.Answer(st.Pending[0].ID, true); err != nil {
		t.Fatal(err)
	}
	done, err := c.Wait(info.ID, []string{Done}, 5*time.Second)
	if err != nil || done.Outcome != "ok" || done.Summary != "publicado" {
		t.Fatalf("the session should finish: %+v, %v", done, err)
	}
	c.Seen(info.ID)
	if st, _ := c.Status(); st.Sessions[0].State != Idle {
		t.Errorf("after seen, done should be idle: %s", st.Sessions[0].State)
	}

	// A client that attaches later replays everything.
	events, _, err := c.Events(info.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, w := range events {
		kinds = append(kinds, w.Kind)
	}
	got := strings.Join(kinds, ",")
	for _, want := range []string{"turn_started", "tool_called", "approval", "answered", "turn_finished", "done"} {
		if !strings.Contains(got, want) {
			t.Errorf("events should include %s: %s", want, got)
		}
	}
}

func TestEventsLongPoll(t *testing.T) {
	_, c, _ := testServer(t, map[string]engine.Script{"leo": {Summary: "ok"}})
	info, _ := c.Start(StartRequest{Target: "leo", Question: "oi"})
	var all []Wire
	for len(all) == 0 || !all[len(all)-1].Ends() {
		events, _, err := c.Events(info.ID, len(all), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 0 {
			t.Fatal("a long poll should not come back empty while the session runs")
		}
		all = append(all, events...)
	}
	for i, w := range all {
		if w.Seq != i {
			t.Fatalf("event %d has seq %d", i, w.Seq)
		}
	}
}

func TestCancelStopsTheSession(t *testing.T) {
	_, c, _ := testServer(t, approvalScripts())
	info, _ := c.Start(StartRequest{Target: "leo", Question: "publica"})
	c.Wait(info.ID, []string{Blocked}, 5*time.Second)
	if err := c.Cancel(info.ID); err != nil {
		t.Fatal(err)
	}
	end, err := c.Wait(info.ID, []string{Done, Idle}, 5*time.Second)
	if err != nil || end.Outcome == "ok" {
		t.Fatalf("a cancelled session should end without ok: %+v, %v", end, err)
	}
	if st, _ := c.Status(); len(st.Pending) != 0 {
		t.Error("a stopped session leaves nothing waiting")
	}
}

func TestRoutineRunsAndLeavesANote(t *testing.T) {
	_, c, dir := testServer(t, map[string]engine.Script{"leo": {Summary: "job ok"}})
	if err := routine.Save(dir, routine.File{Routines: []routine.Routine{{Name: "daily", Agent: "leo", Every: "1h", Prompt: "check"}}}); err != nil {
		t.Fatal(err)
	}
	info, err := c.RunRoutine("daily")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "routine" || info.Title != "daily" {
		t.Errorf("info = %+v", info)
	}
	c.Wait(info.ID, []string{Done}, 5*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, _ := c.Status()
		if len(st.Notes) == 1 {
			if st.Notes[0].Text != "job ok" {
				t.Errorf("note = %+v", st.Notes[0])
			}
			c.DismissNote(st.Notes[0].ID)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the routine should leave a note")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run, ok := routine.LastRun(dir, "daily"); !ok || run.Outcome != "ok" {
		t.Errorf("the run should be saved: %+v", run)
	}
	if st, _ := c.Status(); len(st.Notes) != 0 || len(st.Routines) != 1 || st.Routines[0].Next.IsZero() {
		t.Errorf("status = %+v", st)
	}
}

func TestSessionsSurviveARestart(t *testing.T) {
	s, c, dir := testServer(t, map[string]engine.Script{"leo": {Summary: "feito"}})
	info, _ := c.Start(StartRequest{Target: "leo", Question: "faz"})
	c.Wait(info.ID, []string{Done}, 5*time.Second)
	time.Sleep(50 * time.Millisecond) // save runs right after the last event
	s.Stop()

	again := New(Options{TeamDir: dir, Engine: engine.FakeEngine{}, Load: func() (team.Team, error) { return team.Load(dir) }})
	t.Cleanup(again.Close) // its session saves when it ends: before the folder goes
	st := again.Status()
	if len(st.Sessions) != 1 || st.Sessions[0].Summary != "feito" || st.Sessions[0].ID != info.ID {
		t.Fatalf("the session should come back: %+v", st.Sessions)
	}
	next, err := again.Start(StartRequest{Target: "leo", Question: "de novo"})
	if err != nil || next.ID == info.ID {
		t.Errorf("new ids continue after the saved ones: %s", next.ID)
	}
}

func TestWireRoundTrip(t *testing.T) {
	reply := make(chan bool, 1)
	events := []engine.Event{
		engine.Routed{Agents: []string{"leo"}, By: "router", Reason: "pipeline", Scores: map[string]float64{"leo": 0.8}},
		engine.TurnStarted{Agent: "leo", From: "iris", Note: "olha"},
		engine.ToolCalled{Agent: "leo", Call: "run_sql(x)"},
		engine.ToolResult{Agent: "leo", Text: "1 row", Failed: true},
		engine.TextDelta{Agent: "leo", Text: "oi"},
		engine.TurnFinished{Agent: "leo", Summary: "ok", Tokens: 10, Cached: 5, Cost: 0.01, Elapsed: 2 * time.Second, Passed: true},
		engine.HandedOff{From: "leo", To: "nico", Note: "confere"},
		engine.SessionDone{},
		engine.HopLimitReached{Limit: 6},
	}
	for _, e := range events {
		if back := Encode(e).Decode(reply); back == nil || Encode(back).Kind != Encode(e).Kind {
			t.Errorf("%T did not survive the trip", e)
		}
	}
	if tf := Encode(events[5]).Decode(nil).(engine.TurnFinished); tf.Elapsed != 2*time.Second || tf.Cached != 5 {
		t.Errorf("turn finished = %+v", tf)
	}
	ap := Encode(engine.ApprovalRequested{Agent: "leo", Call: "x", Detail: "d"}).Decode(reply).(engine.ApprovalRequested)
	ap.Reply <- true
	if !<-reply || ap.Detail != "d" {
		t.Error("an approval's answer should go to the reply channel")
	}
}

func TestTokenIsRequired(t *testing.T) {
	_, c, _ := testServer(t, nil)
	bad := &Client{URL: c.URL, Token: "wrong"}
	if _, err := bad.Status(); err == nil {
		t.Error("a wrong token must be refused")
	}
}

func TestPanesThroughTheServer(t *testing.T) {
	_, c, _ := testServer(t, nil)
	spec := pane.Spec{Command: "/bin/sh", Args: []string{"-c", "echo pane-ok; sleep 5"}}
	if runtime.GOOS == "windows" {
		spec = pane.Spec{Command: "cmd.exe", Args: []string{"/c", "echo pane-ok & ping -n 6 127.0.0.1 >NUL"}}
	}
	host := c.Panes()
	info, err := host.Open(spec, 50, 8)
	if err != nil {
		t.Fatal(err)
	}
	since, deadline := -1, time.Now().Add(15*time.Second)
	for {
		scr, err := host.Screen(info.ID, since, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(ansi.Strip(strings.Join(scr.Lines, "\n")), "pane-ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the pane's output should reach the client")
		}
		since = scr.Version
	}
	if text, _ := c.PaneText(info.ID); !strings.Contains(text, "pane-ok") {
		t.Errorf("text = %q", text)
	}
	if st, _ := c.Status(); len(st.Panes) != 1 || st.Panes[0].Dir == "" {
		t.Errorf("status should list the pane, in the team folder: %+v", st.Panes)
	}
	host.SetLayout(`{"pane":"chat"}`)
	if l, _ := host.Layout(); l != `{"pane":"chat"}` {
		t.Errorf("layout = %q", l)
	}
	if err := host.Close(info.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := host.List(); len(list) != 0 {
		t.Error("the pane should be gone")
	}
}

func TestNotifiesWhenNobodyLooks(t *testing.T) {
	s, c, _ := testServer(t, nil)
	var got []string
	s.Notifier = func(title, body string) { got = append(got, body) }
	spec := pane.Spec{Title: "deploy", Command: "/bin/sh", Args: []string{"-c", "sleep 1; printf 'Continue (y/n)? '; sleep 20"}}
	if runtime.GOOS == "windows" {
		spec = pane.Spec{Title: "deploy", Command: "powershell.exe", Args: []string{"-NoProfile", "-Command", "Start-Sleep 1; Read-Host 'Continue (y/n)'"}}
	}
	if _, err := c.Panes().Open(spec, 60, 8); err != nil {
		t.Fatal(err)
	}
	s.watch() // the first look only learns the states
	deadline := time.Now().Add(15 * time.Second)
	for len(got) == 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		s.watch()
	}
	if len(got) != 1 || got[0] != "deploy is waiting for you" {
		t.Fatalf("a blocked pane should notify once when no window is open: %q", got)
	}
}
