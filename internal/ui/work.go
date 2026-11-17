package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tubas/internal/agent"
	"tubas/internal/engine"
	"tubas/internal/routine"
	"tubas/internal/skill"
)

// Work that runs on its own: routines on a schedule, what they bring
// back to the inbox, and the skills agents learn from repeated work.

const routineTickEvery = 15 * time.Second

// routineRun is a routine working right now, in the background.
type routineRun struct {
	id      int
	name    string
	retry   bool
	events  <-chan engine.Event
	cancel  context.CancelFunc
	session Session
}

// inboxNote is something in the inbox that is not an approval: a
// routine's result or a skill an agent suggests.
type inboxNote struct {
	kind     string // "routine" or "skill"
	title    string
	text     string
	at       time.Time
	failed   bool
	routine  string
	suggest  *skill.Suggestion
	serverID string // a note kept by the background server
}

type (
	routineTickMsg  struct{}
	routineEventMsg struct {
		id    int
		event engine.Event
	}
	routineClosedMsg struct{ id int }
	providersMsg     []engine.Detected
)

func routineTick() tea.Cmd {
	return tea.Tick(routineTickEvery, func(time.Time) tea.Msg { return routineTickMsg{} })
}

func waitRoutine(id int, events <-chan engine.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-events
		if !ok {
			return routineClosedMsg{id}
		}
		return routineEventMsg{id, e}
	}
}

func detectProviders(dir string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return providersMsg(engine.DetectProviders(ctx, dir))
	}
}

// loadWork reads routines, skills and the learning tracker from the team
// folder. Called at start and after changes.
func (m Model) loadWork() Model {
	if m.cfg.TeamDir == "" {
		return m
	}
	f, err := routine.Load(m.cfg.TeamDir)
	m.routinesErr = ""
	if err != nil {
		m.routinesErr = err.Error()
	} else {
		m.routines = f
	}
	m.skills, _ = skill.LoadAll(m.cfg.TeamDir)
	m.external = skill.External(m.cfg.TeamDir)
	if m.tracker == nil {
		m.tracker, _ = skill.OpenTracker(m.cfg.TeamDir, 3)
	}
	return m
}

// nextRun is when a routine runs next (zero when off or never).
func (m Model) nextRun(r routine.Routine) time.Time {
	if !r.On() {
		return time.Time{}
	}
	if at, ok := m.retryAt[r.Name]; ok {
		return at
	}
	s, err := routine.Parse(r.Every, r.Between)
	if err != nil {
		return time.Time{}
	}
	base := m.startedAt
	if last, ok := m.lastStart[r.Name]; ok && last.After(base) {
		base = last
	} else if run, ok := routine.LastRun(m.cfg.TeamDir, r.Name); ok && run.Started.After(base) {
		base = run.Started
	}
	return routine.NextOutsideQuiet(s, m.routines.QuietHours, base)
}

// checkRoutines starts the routines that are due.
func (m Model) checkRoutines(now time.Time) (Model, []tea.Cmd) {
	var cmds []tea.Cmd
	if m.cfg.Remote != nil {
		return m, nil // the background server runs them
	}
	for _, r := range m.routines.Routines {
		if _, running := m.runningRoutine(r.Name); running {
			continue
		}
		next := m.nextRun(r)
		if next.IsZero() || next.After(now) {
			continue
		}
		// The approval broker knows one turn per agent: wait while the
		// same agent works in the chat.
		if m.running && m.session.agentStatus(r.Agent) == Live {
			continue
		}
		var cmd tea.Cmd
		_, retry := m.retryAt[r.Name]
		m, cmd = m.startRoutine(r, retry)
		cmds = append(cmds, cmd)
	}
	return m, cmds
}

func (m Model) runningRoutine(name string) (int, bool) {
	for i, r := range m.runs {
		if r.name == name {
			return i, true
		}
	}
	return -1, false
}

// startRoutine runs a routine now, in its own goroutine.
func (m Model) startRoutine(r routine.Routine, retry bool) (Model, tea.Cmd) {
	if m.agentByName(r.Agent) == nil {
		m.notes = append(slices.Clone(m.notes), inboxNote{kind: "routine", title: r.Name, failed: true, at: time.Now(),
			routine: r.Name, text: "no agent named " + r.Agent})
		delete(m.retryAt, r.Name)
		m.lastStart[r.Name] = time.Now()
		return m, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan engine.Event, 64)
	team := map[string]agent.Agent{}
	for _, a := range m.team {
		team[a.Name] = a
	}
	go engine.Run(ctx, m.eng, engine.Request{Team: team, Target: r.Agent, Question: r.Prompt,
		MaxHops: m.cfg.MaxHops, Lead: m.cfg.Lead, Routing: "lead"}, events)
	m.runID++
	m.runs = append(slices.Clone(m.runs), &routineRun{id: m.runID, name: r.Name, retry: retry, events: events, cancel: cancel,
		session: Session{Team: m.cfg.Name, Target: r.Agent, Question: r.Prompt, MaxHops: m.cfg.MaxHops, StartedAt: time.Now(),
			AskedAt: time.Now().Format("15:04")}})
	m.lastStart[r.Name] = time.Now()
	delete(m.retryAt, r.Name)
	return m, waitRoutine(m.runID, events)
}

// routineEvent applies one event of a routine run.
func (m Model) routineEvent(msg routineEventMsg) (Model, tea.Cmd) {
	var run *routineRun
	for _, r := range m.runs {
		if r.id == msg.id {
			run = r
		}
	}
	if run == nil {
		return m, nil
	}
	run.session = run.session.apply(msg.event)
	if e, ok := msg.event.(engine.ApprovalRequested); ok {
		m.queue = append(slices.Clone(m.queue), pendingApproval{e.Agent, e.Call, e.Reply, time.Now(), e.Detail, run.name, e.ID, ""})
		m.notice = hiStyle.Render("◐ routine " + run.name + " waits for you in the inbox")
	}
	switch msg.event.(type) {
	case engine.SessionDone, engine.HopLimitReached, engine.Failed:
		return m.routineFinished(run), waitRoutine(run.id, run.events)
	}
	return m, waitRoutine(run.id, run.events)
}

// routineFinished records the run, delivers it and learns from it.
func (m Model) routineFinished(run *routineRun) Model {
	s := run.session
	tokens, _, cost, _ := s.usage()
	who, summary := s.finalAnswer()
	if who == "" {
		who = s.Target
	}
	rec := routine.Run{Routine: run.name, Agent: who, Started: s.StartedAt, Finished: time.Now(),
		Outcome: "ok", Summary: summary, Tokens: tokens, Cost: cost, Retry: run.retry}
	failed := s.Outcome != Finished
	if failed {
		rec.Outcome, rec.Error = "failed", s.Detail
	}
	routine.SaveRun(m.cfg.TeamDir, rec)
	if failed && m.routines.Retry && !run.retry {
		m.retryAt[run.name] = time.Now().Add(5 * time.Minute)
	}
	text := summary
	if failed {
		text = s.Detail
		if _, ok := m.retryAt[run.name]; ok {
			text += " · trying again at " + m.retryAt[run.name].Format("15:04")
		}
	}
	// A routine that answers PASS found nothing worth saying.
	if !failed && strings.EqualFold(strings.TrimSpace(summary), "PASS") {
		text = ""
	}
	if text != "" {
		m.notes = append(slices.Clone(m.notes), inboxNote{kind: "routine", title: run.name, text: text, at: time.Now(),
			failed: failed, routine: run.name})
	}
	m.runs = slices.DeleteFunc(slices.Clone(m.runs), func(r *routineRun) bool { return r.id == run.id })
	return m.learn(s)
}

// learn shows each finished turn to the tracker. When an agent did the
// same work enough times, a skill suggestion lands in the inbox.
func (m Model) learn(s Session) Model {
	if m.tracker == nil {
		return m
	}
	for _, t := range s.Turns {
		if t.Status != Done {
			continue
		}
		var calls []skill.Call
		var failures []string
		for _, st := range t.Steps {
			switch st.Kind {
			case ToolCall:
				name, args := splitCall(st.Text)
				calls = append(calls, skill.Call{Tool: name, Args: args})
			case Failure:
				failures = append(failures, st.Text)
			}
		}
		sug, err := m.tracker.Observe(t.Agent, s.Question, calls, failures, true, time.Now())
		if err == nil && sug != nil {
			m.notes = append(slices.Clone(m.notes), inboxNote{kind: "skill", title: t.Agent, at: time.Now(), suggest: sug,
				text: fmt.Sprintf("%s did the same work %d times", t.Agent, len(sug.Examples))})
			m.notice = hiStyle.Render("✦ " + t.Agent + " could save that as a skill · it's in your inbox")
		}
	}
	return m
}

// ── the inbox's notes ───────────────────────────────────────────────────

func (m Model) inboxCount() int { return len(m.queue) + len(m.notes) }

// noteKey handles a key on a note in the inbox.
func (m Model) noteKey(i int, key string) (Model, tea.Cmd) {
	n := m.notes[i]
	drop := func(m Model) Model {
		m.notes = slices.Delete(slices.Clone(m.notes), i, i+1)
		m.secIdx[SecInbox] = max(0, min(m.secIdx[SecInbox], m.inboxCount()-1))
		return m
	}
	switch n.kind {
	case "routine":
		switch key {
		case "enter":
			m = drop(m)
			if _, idx := m.routines.Find(n.routine); idx >= 0 {
				m.secIdx[SecRoutines] = idx
			}
			return m.goTo(SecRoutines), nil
		case "x", "d", "y":
			if n.serverID != "" {
				go m.cfg.Remote.DismissNote(n.serverID)
			}
			return drop(m), nil
		}
	case "skill":
		sug := n.suggest
		switch key {
		case "y", "t":
			agents := []string{sug.Agent}
			if key == "t" {
				agents = nil
				for _, a := range m.team {
					agents = append(agents, a.Name)
				}
			}
			m, err := m.saveSkillFor(sug.Draft, agents)
			if err != nil {
				m.notice = badStyle.Render(err.Error())
				return m, nil
			}
			m.notice = okStyle.Render("✓ saved the skill " + sug.Draft.Name)
			return drop(m), nil
		case "e":
			m.form = m.skillForm(sug.Draft, []string{sug.Agent}, true)
			m.form.extra = i
			return m, nil
		case "n":
			m.tracker.Later(sug.Agent, sug.Signature)
			return drop(m), nil
		case "x":
			m.tracker.Never(sug.Agent, sug.Signature)
			return drop(m), nil
		}
	}
	return m, nil
}

func (m Model) viewNote(n inboxNote, rw int) []string {
	switch n.kind {
	case "routine":
		head := okStyle.Render("✓ ")
		if n.failed {
			head = badStyle.Render("✗ ")
		}
		lines := []string{head + boldStyle.Render("routine "+n.title) + dimStyle.Render(" · "+n.at.Format("15:04")), ""}
		for _, l := range strings.Split(n.text, "\n") {
			for _, w := range wrap(l, rw) {
				lines = append(lines, textStyle.Render(w))
			}
		}
		return append(lines, "", dimStyle.Render("enter open the routine · x dismiss"))
	case "skill":
		s := n.suggest
		lines := []string{skillStyle.Render("✦ ") + m.agentStyle(s.Agent).Bold(true).Render(s.Agent) +
			hiStyle.Render(fmt.Sprintf(" did the same work %d times", len(s.Examples))) + dimStyle.Render(" · save it as a skill?"), ""}
		for _, ex := range s.Examples {
			lines = append(lines, dimStyle.Render(ex.At.Format("Mon 15:04")+"  ")+textStyle.Render(fit(ex.Question, rw-12)))
		}
		lines = append(lines, "", dimStyle.Render("draft · ")+skillStyle.Render(s.Draft.Name), dimStyle.Render(fit(s.Draft.Description, rw)))
		for _, l := range strings.Split(strings.TrimSpace(s.Draft.Body), "\n") {
			if strings.HasPrefix(l, "# ") || l == "" {
				continue
			}
			lines = append(lines, faintStyle.Render(fit(l, rw)))
		}
		lines = append(lines, "", dimStyle.Render("made of what the agent did, never of text tools returned"), "",
			barBold.Render(" y ")+barStyle.Render(" save for "+s.Agent+" ")+"  "+barBold.Render(" t ")+barStyle.Render(" whole team ")+
				"  "+dimStyle.Render("e edit · n not now · x never this"))
		return lines
	}
	return nil
}

// ── routines section ────────────────────────────────────────────────────

func (m Model) viewRoutines(cw, h int) []string {
	rs := m.routines.Routines
	if m.routinesErr != "" {
		return []string{"", badStyle.Render("routines.yaml: " + m.routinesErr)}
	}
	if len(rs) == 0 {
		return []string{"",
			boldStyle.Render("No routines yet."),
			"",
			dimStyle.Render("A routine is an agent working on its own on a schedule: every hour, every"),
			dimStyle.Render("weekday at 08:00, or a cron line. Press n to make one. Anything that writes"),
			dimStyle.Render("still waits for you in the inbox. Routines run while tubas is open."),
		}
	}
	lw := listWidth(cw) + 6
	idx := min(m.secIdx[SecRoutines], len(rs)-1)
	quiet := ""
	if m.routines.QuietHours != "" {
		quiet = "quiet " + m.routines.QuietHours
	}
	left := []string{spread(dimStyle.Render(fmt.Sprintf("%d routines", len(rs))), faintStyle.Render(quiet), lw), ""}
	now := time.Now()
	for i, r := range rs {
		state := dimStyle.Render("off")
		switch _, running := m.runningRoutine(r.Name); {
		case running:
			state = hiStyle.Render(spinner[m.frame%len(spinner)] + " running")
		case r.On():
			if next := m.nextRun(r); !next.IsZero() {
				state = dimStyle.Render("in " + until(next, now))
			}
		}
		left = append(left, listRow(r.Name, state, i == idx, lw))
	}

	r := rs[idx]
	rw := cw - lw - 3
	sched := r.Every
	if s, err := routine.Parse(r.Every, r.Between); err == nil {
		sched = s.String()
	}
	on := okStyle.Render("● on")
	if !r.On() {
		on = dimStyle.Render("○ off")
	}
	right := []string{spread(boldStyle.Render(r.Name), on, rw), "",
		kv("runs as", m.agentStyle(r.Agent).Render(r.Agent)),
		kv("when", textStyle.Render(sched)),
		kv("delivers", textStyle.Render(orDefault(r.Deliver, "chat")+" · in the inbox")),
	}
	if next := m.nextRun(r); !next.IsZero() {
		right = append(right, kv("next", textStyle.Render(next.Format("Mon 15:04"))))
	}
	right = append(right, "", dimStyle.Render("asks, every run"))
	for _, l := range strings.Split(r.Prompt, "\n") {
		for _, w := range wrap(l, rw) {
			right = append(right, textStyle.Render(w))
		}
	}
	runs, _ := routine.LastRuns(m.cfg.TeamDir, r.Name, 5)
	if len(runs) > 0 {
		right = append(right, "", dimStyle.Render("last runs"))
	}
	for _, run := range runs {
		mark := okStyle.Render("✓ ")
		if run.Outcome != "ok" {
			mark = badStyle.Render("✗ ")
		}
		text := run.Summary
		if run.Error != "" {
			text = run.Error
		}
		first, _, _ := strings.Cut(text, "\n")
		right = append(right, mark+dimStyle.Render(run.Started.Local().Format("Mon 15:04")+"  ")+textStyle.Render(fit(first, rw-14)))
	}
	return panes(left, right, lw, cw, h)
}

// until is "23m", "2h 5m", "3d".
func until(t, now time.Time) string {
	d := t.Sub(now)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func (m Model) routineKey(key string) (Model, tea.Cmd) {
	rs := m.routines.Routines
	idx := m.secIdx[SecRoutines]
	switch key {
	case "n":
		m.form = m.routineForm(routine.Routine{})
		return m, nil
	case "enter", "e":
		if idx < len(rs) {
			m.form = m.routineForm(rs[idx])
		}
	case " ", "space":
		if idx < len(rs) {
			f := m.routines
			f.Routines = slices.Clone(f.Routines)
			on := !f.Routines[idx].On()
			f.Routines[idx].Enabled = &on
			if err := routine.Save(m.cfg.TeamDir, f); err != nil {
				m.notice = badStyle.Render(err.Error())
				return m, nil
			}
			m.routines = f
		}
	case "r":
		if idx < len(rs) && m.cfg.Remote != nil {
			if _, err := m.cfg.Remote.RunRoutine(rs[idx].Name); err != nil {
				m.notice = badStyle.Render(err.Error())
			} else {
				m.notice = okStyle.Render("✓ " + rs[idx].Name + " is running on the server · ctrl+g shows it")
			}
			return m, nil
		}
		if idx < len(rs) {
			if _, running := m.runningRoutine(rs[idx].Name); !running {
				return m.startRoutine(rs[idx], false)
			}
		}
	case "d":
		if idx < len(rs) {
			if m.confirm != "delete routine "+rs[idx].Name {
				m.confirm = "delete routine " + rs[idx].Name
				m.notice = warnStyle.Render("press d again to delete " + rs[idx].Name)
				return m, nil
			}
			f := m.routines
			f.Routines = slices.Delete(slices.Clone(f.Routines), idx, idx+1)
			if err := routine.Save(m.cfg.TeamDir, f); err != nil {
				m.notice = badStyle.Render(err.Error())
				return m, nil
			}
			m.routines, m.confirm = f, ""
			m.secIdx[SecRoutines] = max(0, idx-1)
			m.notice = okStyle.Render("✓ deleted")
		}
	}
	return m, nil
}

func (m Model) routineForm(r routine.Routine) *form {
	names := make([]string, len(m.team))
	for i, a := range m.team {
		names[i] = a.Name
	}
	agentName := r.Agent
	if agentName == "" && len(names) > 0 {
		agentName = names[0]
	}
	on := "on"
	if !r.On() {
		on = "off"
	}
	f := &form{kind: "routine", title: "routine " + r.Name, target: r.Name, file: "routines.yaml"}
	if r.Name == "" {
		f.title = "new routine"
	}
	f.fields = []field{
		{key: "name", label: "name", kind: fText, value: r.Name, hint: "lowercase-with-dashes"},
		{key: "agent", label: "runs as", kind: fChoice, options: names, value: agentName},
		{key: "every", label: "when", kind: fText, value: orDefault(r.Every, "weekdays 08:00"),
			hint: `1h · 30m · daily 09:30 · weekdays 08:00 · mon,wed 10:00 · cron: 0 8 * * 1-5`},
		{key: "between", label: "between", kind: fText, value: r.Between, hint: "only for intervals, like 08:00-20:00"},
		{key: "deliver", label: "delivers", kind: fChoice, options: []string{"chat", "report"}, value: orDefault(r.Deliver, "chat")},
		{key: "on", label: "on", kind: fChoice, options: []string{"on", "off"}, value: on},
		{key: "prompt", label: "asks", kind: fLong, value: r.Prompt},
	}
	return f
}

func (m Model) saveRoutine() Model {
	f := *m.form
	r := routine.Routine{Name: f.get("name"), Agent: f.get("agent"), Every: f.get("every"), Between: f.get("between"),
		Deliver: f.get("deliver"), Prompt: f.get("prompt")}
	if f.get("on") == "off" {
		off := false
		r.Enabled = &off
	}
	file := m.routines
	file.Routines = slices.Clone(file.Routines)
	if _, i := file.Find(f.target); f.target != "" && i >= 0 {
		file.Routines[i] = r
	} else {
		file.Routines = append(file.Routines, r)
	}
	if err := routine.Save(m.cfg.TeamDir, file); err != nil {
		f.err = err.Error()
		m.form = &f
		return m
	}
	m.form, m.routines = nil, file
	if _, i := file.Find(r.Name); i >= 0 {
		m.secIdx[SecRoutines] = i
	}
	sched, _ := routine.Parse(r.Every, r.Between)
	m.notice = okStyle.Render("✓ saved · " + r.Agent + " runs it " + sched.String())
	return m
}
