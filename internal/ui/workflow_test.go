package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"tubas/internal/agent"
	"tubas/internal/engine"
	"tubas/internal/routine"
	"tubas/internal/team"
)

// teamModel copies the example team to a temporary folder, so tests can
// save agents, routines and skills for real.
func teamModel(t *testing.T, scripts map[string]engine.Script) Model {
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
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tm, err := team.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Name: tm.Name, Lead: tm.Lead, MaxHops: tm.MaxHops, Routing: tm.Routing, Connectors: tm.ConnectorNames(), TeamDir: tm.Dir}
	return New(tm.Agents, engine.FakeEngine{Scripts: scripts}, cfg)
}

func typed(text string) []string {
	keys := make([]string, 0, len(text))
	for _, r := range text {
		if r == ' ' {
			keys = append(keys, "space")
		} else {
			keys = append(keys, string(r))
		}
	}
	return keys
}

// drive runs a command and feeds what it returns to the model, the way
// Bubble Tea would, until nothing is left (or limit steps).
func drive(m Model, cmd tea.Cmd, limit int) Model {
	for i := 0; cmd != nil && i < limit; i++ {
		msg := cmd()
		if msg == nil {
			return m
		}
		next, c := m.Update(msg)
		m, cmd = next.(Model), c
	}
	return m
}

func TestCreateAnAgentOnScreen(t *testing.T) {
	m := teamModel(t, engine.DemoScripts()).PressKeys("alt+3", "n")
	if m.form == nil || m.form.kind != "agent-new" {
		t.Fatal("n should open the new agent form")
	}
	frame := m.Snapshot(128, 40)
	contains(t, frame, "new agent", "start from", "analyst", "ctrl+s save")

	m = m.PressKeys("down") // name
	m = m.PressKeys(typed("ana")...)
	m = m.PressKeys("ctrl+s")
	if m.form != nil {
		t.Fatalf("saving should close the form: %s", m.form.err)
	}
	a := m.agentByName("ana")
	if a == nil {
		t.Fatal("ana should be in the team")
	}
	if a.Role != "data analyst" || a.Tools["data"] != "read" || a.Tools["workspace"] != "write" {
		t.Errorf("the analyst template should fill role and access: %+v", a)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.TeamDir, "agents", "ana.md")); err != nil {
		t.Error("agents/ana.md should exist")
	}
	if _, err := team.Load(m.cfg.TeamDir); err != nil {
		t.Errorf("the team should still load: %v", err)
	}
}

func TestChangeAnAgentsModel(t *testing.T) {
	m := teamModel(t, engine.DemoScripts()).PressKeys("alt+3", "down", "enter") // leo
	if m.form == nil || m.form.target != "leo" {
		t.Fatal("enter should open leo's form")
	}
	for m.form.fields[m.form.idx].key != "model" {
		m = m.PressKeys("down")
	}
	m = m.PressKeys("right", "ctrl+s") // sonnet -> opus
	data, _ := os.ReadFile(filepath.Join(m.cfg.TeamDir, "agents", "leo.md"))
	back, err := agent.Parse(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if back.Model != "opus" {
		t.Errorf("leo's model = %s, want opus", back.Model)
	}
	if !strings.Contains(back.Instructions, "data engineer") {
		t.Error("the instructions should be kept")
	}
}

func TestPermissionsTable(t *testing.T) {
	m := teamModel(t, engine.DemoScripts()).PressKeys("alt+3", "p")
	contains(t, m.Snapshot(128, 32), "permissions", "data", "workspace", "team")
	conns := m.connectorsForForms()
	col := 0
	for i, c := range conns {
		if c == "reports" {
			col = i
		}
	}
	for range col {
		m = m.PressKeys("right")
	}
	m = m.PressKeys("space", "space") // iris: reports none -> read -> write
	contains(t, m.Snapshot(128, 32), "not saved")
	m = m.PressKeys("ctrl+s")
	data, _ := os.ReadFile(filepath.Join(m.cfg.TeamDir, "agents", "iris.md"))
	if a, _ := agent.Parse(string(data)); a.Tools["reports"] != "write" {
		t.Errorf("iris should have reports: write, file says %q", a.Tools["reports"])
	}
	m = m.PressKeys("space", "esc") // a change not saved is dropped
	if m.agentByName("iris").Tools["reports"] != "write" || m.permMode {
		t.Error("esc should drop the unsaved change and leave the table")
	}
}

func TestRoutineFromFormToRun(t *testing.T) {
	scripts := map[string]engine.Script{"leo": {Steps: []engine.FakeStep{{Call: "get_job_runs(job=nightly)", Result: "SUCCESS"}}, Summary: "job ok às 07:41"}}
	m := teamModel(t, scripts).PressKeys("alt+4")
	contains(t, m.Snapshot(128, 32), "No routines yet.")

	m = m.PressKeys("n")
	m = m.PressKeys(typed("databricks-daily")...)
	m = m.PressKeys("down", "right") // runs as: iris -> leo
	for m.form.fields[m.form.idx].key != "prompt" {
		m = m.PressKeys("down")
	}
	f := *m.form
	f.set("prompt", "Check the nightly job.")
	m.form = &f
	m = m.PressKeys("ctrl+s")
	if m.form != nil {
		t.Fatalf("saving should close the form: %s", m.form.err)
	}
	file, err := routine.Load(m.cfg.TeamDir)
	if err != nil || len(file.Routines) != 1 || file.Routines[0].Agent != "leo" {
		t.Fatalf("routines.yaml = %+v, %v", file, err)
	}
	contains(t, m.Snapshot(128, 32), "databricks-daily", "weekdays at 08:00", "Check the nightly job.")

	// r runs it now; the result lands in the inbox and in the run log.
	next, cmd := m.sectionKey("r")
	m = drive(next, cmd, 50)
	if len(m.runs) != 0 {
		t.Fatal("the run should have finished")
	}
	if len(m.notes) != 1 || !strings.Contains(m.notes[0].text, "job ok") {
		t.Fatalf("the result should be in the inbox: %+v", m.notes)
	}
	if run, ok := routine.LastRun(m.cfg.TeamDir, "databricks-daily"); !ok || run.Outcome != "ok" {
		t.Errorf("the run should be saved: %+v", run)
	}
	contains(t, m.PressKeys("alt+2").Snapshot(128, 32), "databricks-daily", "job ok")
}

// A routine's approval waits in the inbox and never moves the chat.
func TestRoutineApprovalStaysOutOfTheChat(t *testing.T) {
	scripts := map[string]engine.Script{"leo": {Summary: "postado",
		Approval: &engine.FakeApproval{Call: "post_message(#dados)", OnApprove: "ok", OnDeny: "no"}}}
	m := teamModel(t, scripts)
	m, cmd := m.startRoutine(routine.Routine{Name: "aviso", Agent: "leo", Every: "1h", Prompt: "avise"}, false)
	for i := 0; i < 20 && len(m.queue) == 0; i++ {
		next, c := m.Update(cmd())
		m, cmd = next.(Model), c
	}
	if len(m.queue) != 1 || m.queue[0].routine != "aviso" {
		t.Fatalf("the routine's approval should wait in the queue: %+v", m.queue)
	}
	if m.session.Pending != nil {
		t.Error("the chat should not show a routine's approval")
	}
	m = m.PressKeys("y") // in the chat, y answers only the chat's approvals
	if len(m.queue) != 1 {
		t.Fatal("y in the chat should not answer the routine")
	}
	m = m.PressKeys("alt+2", "y")
	m = drive(m, cmd, 50)
	if len(m.queue) != 0 || len(m.runs) != 0 {
		t.Fatalf("the inbox should answer it and the run should end: queue %d, runs %d", len(m.queue), len(m.runs))
	}
}

func TestRepeatedWorkSuggestsASkill(t *testing.T) {
	scripts := map[string]engine.Script{"leo": {Steps: []engine.FakeStep{
		{Call: "get_job_runs(job=nightly limit=1)", Result: "run 88412 FAILED"},
		{Call: "list_run_tasks(run=88412)", Result: "load_entregas timeout"},
	}, Summary: "load_entregas falhou"}}
	m := teamModel(t, scripts)
	for i := range 3 {
		m = m.Ask("@leo o job rodou?").Settle(0)
		m = m.learn(m.session)
		if i < 2 && len(m.notes) != 0 {
			t.Fatalf("no suggestion before the third time (run %d)", i+1)
		}
	}
	if len(m.notes) != 1 || m.notes[0].kind != "skill" {
		t.Fatalf("the third time should suggest a skill: %+v", m.notes)
	}
	frame := m.PressKeys("alt+2").Snapshot(128, 40)
	contains(t, frame, "did the same work 3 times", "get_job_runs", "save for leo")

	m = m.PressKeys("alt+2", "y")
	leo := m.agentByName("leo")
	if leo == nil || len(leo.Skills) != 1 {
		t.Fatalf("leo should have the skill now: %+v", leo)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.TeamDir, ".tubas", "skills", leo.Skills[0], "SKILL.md")); err != nil {
		t.Error("the skill file should exist")
	}
	if leo.Tools["skills"] != "read" {
		t.Error("an agent with skills should get the skills connector")
	}
	contains(t, m.PressKeys("alt+3", "s").Snapshot(128, 32), leo.Skills[0], "learned")
}

func TestSettingsSaveToTeamYAML(t *testing.T) {
	m := teamModel(t, engine.DemoScripts()).PressKeys("alt+7")
	contains(t, m.Snapshot(128, 40), "CONVERSATION", "HARNESSES AND PROVIDERS", "ROUTINES")
	m = m.PressKeys("e")
	if m.form == nil || m.form.kind != "settings" {
		t.Fatal("e should open the settings form")
	}
	m = m.PressKeys("right") // routing: router -> all
	m = m.PressKeys("ctrl+s")
	if m.form != nil {
		t.Fatalf("saving should close the form: %s", m.form.err)
	}
	tm, err := team.Load(m.cfg.TeamDir)
	if err != nil || tm.Routing != "all" {
		t.Errorf("team.yaml routing = %q, %v", tm.Routing, err)
	}
	data, _ := os.ReadFile(filepath.Join(m.cfg.TeamDir, "team.yaml"))
	if !strings.Contains(string(data), "#") {
		t.Error("team.yaml should keep its comments")
	}
}

func TestEscalationAdviceShowsInTheChat(t *testing.T) {
	scripts := map[string]engine.Script{"leo": {Steps: []engine.FakeStep{
		{Call: "get_run_output(run=1)", Result: "timeout\n↑ opus · $0.011: use get_task_log with tail=200"},
	}, Summary: "achei"}}
	m := teamModel(t, scripts).Ask("@leo por que falhou?").Settle(0)
	contains(t, m.PressKeys("up", "enter").Snapshot(128, 32), "↑ opus", "get_task_log")
}
