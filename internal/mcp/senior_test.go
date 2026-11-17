package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"tubas/internal/agent"
	"tubas/internal/routine"
	"tubas/internal/team"
)

// approver is a broker that answers every approval with yes, and keeps
// the last request it got.
func approver(t *testing.T, last *atomic.Value) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		last.Store(req)
		w.Write([]byte(`{"approved":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The lead proposes an agent: after your yes, its file exists, and it can
// never get team access itself.
func TestLeadProposesAnAgent(t *testing.T) {
	tm := testTeam(t) // reader is the lead: team: none in its file, so use writer
	writer, _ := tm.Agent("writer")
	writer.Tools["team"] = "write"
	if _, err := agent.Save(filepath.Join(tm.Dir, "agents"), writer); err != nil {
		t.Fatal(err)
	}
	tm, _ = team.Load(tm.Dir)
	var last atomic.Value
	broker := approver(t, &last)
	g := gateway(t, tm, "writer", broker.URL, "secret")

	if !strings.Contains(strings.Join(g.ToolNames(), ","), "team__propose_agent") {
		t.Fatalf("writer should see the team tools: %v", g.ToolNames())
	}
	args := `{"name":"vera","role":"watcher","model":"haiku","access":{"data":"read"},"instructions":"Watch."}`
	text, isErr := testResultText(talk(t, g, call("team__propose_agent", args))[0])
	if isErr || !strings.Contains(text, "created @vera") {
		t.Fatalf("propose_agent = %q (error=%v)", text, isErr)
	}
	req := last.Load().(map[string]string)
	if !strings.Contains(req["detail"], "instructions: Watch.") {
		t.Errorf("the inbox should get the whole proposal, got %q", req["detail"])
	}
	tm, err := team.Load(tm.Dir)
	if err != nil {
		t.Fatal(err)
	}
	vera, ok := tm.Agent("vera")
	if !ok || vera.Model != "haiku" || vera.Tools["data"] != "read" {
		t.Errorf("vera = %+v", vera)
	}

	text, isErr = testResultText(talk(t, g, call("team__propose_agent", `{"name":"boss","role":"x","model":"opus","access":{"team":"write"},"instructions":"x"}`))[0])
	if !isErr || !strings.Contains(text, "team access") {
		t.Errorf("a new agent must not get team access: %q", text)
	}

	text, isErr = testResultText(talk(t, g, call("team__propose_routine", `{"name":"watch","agent":"vera","every":"1h","between":"08:00-20:00","prompt":"look"}`))[0])
	if isErr {
		t.Fatalf("propose_routine = %q", text)
	}
	f, _ := routine.Load(tm.Dir)
	if r, i := f.Find("watch"); i < 0 || r.Agent != "vera" {
		t.Errorf("routines.yaml = %+v", f)
	}
}

func escalatingTeam(t *testing.T, on ...string) team.Team {
	tm := testTeam(t)
	w, _ := tm.Agent("writer")
	w.Escalate = &agent.Escalation{To: "opus", On: on, PerDay: 2}
	w.Tools["data"] = "read"
	if _, err := agent.Save(filepath.Join(tm.Dir, "agents"), w); err != nil {
		t.Fatal(err)
	}
	tm, err := team.Load(tm.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func fakeSenior(g *Gateway, calls *atomic.Int32) {
	g.senior.run = func(_ context.Context, model, prompt string) (string, float64, error) {
		calls.Add(1)
		return "try describe_table first", 0.01, nil
	}
}

func TestSecondFailureBringsAdvice(t *testing.T) {
	g := gateway(t, escalatingTeam(t, "tool_fails"), "writer", "", "")
	var calls atomic.Int32
	fakeSenior(g, &calls)
	bad := call("data__run_sql", `{"query":"SELECT * FROM nowhere"}`)
	rs := talk(t, g, bad, bad, bad)
	first, _ := testResultText(rs[0])
	if strings.Contains(first, "↑") {
		t.Error("one failure should not escalate")
	}
	second := rs[1]["result"].(map[string]any)["content"].([]any)
	last := second[len(second)-1].(map[string]any)["text"].(string)
	if !strings.HasPrefix(last, "↑ opus · $0.010: try describe_table first") {
		t.Errorf("the second failure should carry the advice, got %q", last)
	}
	if calls.Load() != 1 {
		t.Errorf("the senior should be asked once, was asked %d times", calls.Load())
	}
}

func TestWriteGetsReviewedBeforeYouSeeIt(t *testing.T) {
	tm := escalatingTeam(t, "before_write")
	var last atomic.Value
	broker := approver(t, &last)
	g := gateway(t, tm, "writer", broker.URL, "secret")
	var calls atomic.Int32
	fakeSenior(g, &calls)
	talk(t, g, call("reports__publish_report", `{"channel":"#t","title":"T","body":"B"}`))
	req := last.Load().(map[string]string)
	if !strings.Contains(req["detail"], "↑ opus") || !strings.Contains(req["detail"], "title: T") {
		t.Errorf("the approval should carry the call and the review: %q", req["detail"])
	}
}

func TestEscalationDailyLimit(t *testing.T) {
	g := gateway(t, escalatingTeam(t, "ask"), "writer", "", "")
	var calls atomic.Int32
	fakeSenior(g, &calls)
	ask := call("escalate__ask_senior", `{"question":"help"}`)
	rs := talk(t, g, ask, ask, ask)
	third, _ := testResultText(rs[2])
	if calls.Load() != 2 || !strings.Contains(third, "daily limit") {
		t.Errorf("per_day 2: asked %d times, third answer %q", calls.Load(), third)
	}
	if n := EscalationsToday(g.senior.teamDir, "writer"); n != 2 {
		t.Errorf("EscalationsToday = %d", n)
	}
	os.RemoveAll(filepath.Join(g.senior.teamDir, ".tubas"))
}
