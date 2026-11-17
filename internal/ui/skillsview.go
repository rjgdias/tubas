package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tubas/internal/agent"
	"tubas/internal/routine"
	"tubas/internal/skill"
	"tubas/internal/team"
)

var skillStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E9B8F5"))

// ── skills: agents › skills ─────────────────────────────────────────────

// skillRows is the list: the team's skills (archived last), then the ones
// found on this computer that tubas doesn't have yet.
func (m Model) skillRows() []skill.Skill {
	return append(slices.Clone(m.skills), m.external...)
}

func (m Model) whoHas(name string) []string {
	var who []string
	for _, a := range m.team {
		if slices.Contains(a.Skills, name) {
			who = append(who, a.Name)
		}
	}
	return who
}

func (m Model) viewSkills(cw, h int) []string {
	rows := m.skillRows()
	if len(rows) == 0 {
		return []string{"",
			boldStyle.Render("No skills yet."),
			"",
			dimStyle.Render("A skill is a way of doing recurring work: when to use it, the steps, the pitfalls."),
			dimStyle.Render("Agents suggest one when they repeat the same work 3 times (it lands in the inbox),"),
			dimStyle.Render("or press n to write one. Skills in ~/.claude/skills show up here to add."),
		}
	}
	lw := listWidth(cw) + 4
	idx := min(m.skillIdx, len(rows)-1)
	left := []string{dimStyle.Render(fmt.Sprintf("%d skills", len(m.skills))), ""}
	externalHead := false
	for i, s := range rows {
		if i >= len(m.skills) && !externalHead {
			left = append(left, "", dimStyle.Render("on this computer"))
			externalHead = true
		}
		mark := map[string]string{"learned": "✦ ", "written": "✎ ", "imported": "↓ "}[s.Origin]
		right := faintStyle.Render(strings.Join(m.whoHas(s.Name), " "))
		switch {
		case s.Archived:
			right = faintStyle.Render("archived")
		case i >= len(m.skills):
			mark, right = "○ ", faintStyle.Render("a adds it")
		}
		left = append(left, listRow(mark+s.Name, right, i == idx, lw))
	}

	s := rows[idx]
	rw := cw - lw - 3
	right := []string{spread(skillStyle.Bold(true).Render(s.Name), dimStyle.Render(fmt.Sprintf("v%d", s.Version)), rw)}
	for _, w := range wrap(s.Description, rw) {
		right = append(right, dimStyle.Render(w))
	}
	right = append(right, "")
	if idx >= len(m.skills) {
		right = append(right, kv("found in", textStyle.Render(s.Source)), kv("", dimStyle.Render("agents don't see it until you add it (a)")))
	} else {
		who := m.whoHas(s.Name)
		whoText := dimStyle.Render("nobody yet · enter to give it")
		if len(who) > 0 {
			parts := make([]string, len(who))
			for i, n := range who {
				parts[i] = m.agentStyle(n).Render(n)
			}
			whoText = strings.Join(parts, " ")
		}
		right = append(right, kv("came from", textStyle.Render(s.Origin+sourceText(s.Source))), kv("who has it", whoText))
		if s.Locked {
			right = append(right, kv("locked", dimStyle.Render("agents don't change it")))
		}
	}
	if len(s.Files) > 0 {
		right = append(right, kv("files", textStyle.Render(strings.Join(s.Files, " · "))))
	}
	right = append(right, "")
	for _, l := range strings.Split(strings.TrimSpace(s.Body), "\n") {
		for _, w := range wrap(l, rw) {
			right = append(right, faintStyle.Render(w))
		}
	}
	return panes(left, right, lw, cw, h)
}

func sourceText(src string) string {
	if src == "" {
		return ""
	}
	return " · " + src
}

func (m Model) skillKey(key string) Model {
	rows := m.skillRows()
	idx := min(m.skillIdx, max(0, len(rows)-1))
	switch key {
	case "up":
		m.skillIdx = max(0, idx-1)
	case "down":
		m.skillIdx = max(0, min(len(rows)-1, idx+1))
	case "esc", "s":
		m.skillsMode = false
	case "n":
		m.form = m.skillForm(skill.Skill{Origin: "written"}, nil, true)
	case "enter", "e":
		if idx < len(m.skills) {
			m.form = m.skillForm(rows[idx], m.whoHas(rows[idx].Name), false)
		}
	case "a":
		if idx >= len(m.skills) && idx < len(rows) {
			s, err := skill.Import(m.cfg.TeamDir, rows[idx].Source)
			if err != nil {
				m.notice = badStyle.Render(err.Error())
				return m
			}
			m = m.loadWork()
			m.notice = okStyle.Render("✓ added " + s.Name + " · enter gives it to agents")
		}
	case "d":
		if idx < len(m.skills) {
			s := rows[idx]
			var err error
			if s.Archived {
				err = skill.Restore(m.cfg.TeamDir, s.Name)
			} else {
				err = skill.Archive(m.cfg.TeamDir, s.Name)
			}
			if err != nil {
				m.notice = badStyle.Render(err.Error())
				return m
			}
			m = m.loadWork()
			m.notice = okStyle.Render("✓ done · d again undoes it")
		}
	}
	return m
}

func (m Model) skillForm(s skill.Skill, who []string, isNew bool) *form {
	f := &form{kind: "skill", title: "skill " + s.Name, target: s.Name, file: ".tubas/skills/" + s.Name + "/SKILL.md"}
	if isNew {
		f.target = ""
		f.title = "new skill"
	}
	f.fields = []field{
		{key: "name", label: "name", kind: fText, value: s.Name, hint: "lowercase-with-dashes"},
		{key: "description", label: "when to use it", kind: fText, value: s.Description, hint: "the line agents see in their prompt"},
		{key: "who", label: "who has it", kind: fText, value: strings.Join(who, ", "), hint: "agents, separated by commas"},
		{key: "locked", label: "locked", kind: fChoice, options: []string{"no", "yes"}, value: map[bool]string{true: "yes", false: "no"}[s.Locked]},
		{key: "body", label: "steps", kind: fLong, value: s.Body},
	}
	f.extra = s
	return f
}

// saveSkill writes the skill and who has it.
func (m Model) saveSkill() Model {
	f := *m.form
	var s skill.Skill
	switch base := f.extra.(type) {
	case skill.Skill:
		s = base
	case int: // from a suggestion in the inbox
		if base < len(m.notes) && m.notes[base].suggest != nil {
			s = m.notes[base].suggest.Draft
		}
	}
	s.Name, s.Description, s.Body = f.get("name"), f.get("description"), f.get("body")
	s.Locked = f.get("locked") == "yes"
	who := splitList(f.get("who"))
	for _, n := range who {
		if m.agentByName(n) == nil {
			f.err = "no agent named " + n
			m.form = &f
			return m
		}
	}
	if f.target != "" && f.target != s.Name {
		f.err = "renaming a skill is not supported yet: make a new one"
		m.form = &f
		return m
	}
	m, err := m.saveSkillFor(s, who)
	if err != nil {
		f.err = err.Error()
		m.form = &f
		return m
	}
	if i, ok := f.extra.(int); ok && i < len(m.notes) {
		m.notes = slices.Delete(slices.Clone(m.notes), i, i+1)
	}
	m.form = nil
	m.notice = okStyle.Render("✓ saved the skill " + s.Name)
	return m
}

// saveSkillFor writes a skill and makes exactly these agents have it.
func (m Model) saveSkillFor(s skill.Skill, who []string) (Model, error) {
	if m.cfg.TeamDir == "" {
		return m, fmt.Errorf("this team has no folder to save to")
	}
	if _, err := skill.Save(m.cfg.TeamDir, s); err != nil {
		return m, err
	}
	for _, a := range m.team {
		has, want := slices.Contains(a.Skills, s.Name), slices.Contains(who, a.Name)
		if has == want {
			continue
		}
		err := m.editAgent(a.Name, func(a *agent.Agent) {
			a.Skills = slices.DeleteFunc(slices.Clone(a.Skills), func(n string) bool { return n == s.Name })
			if want {
				a.Skills = append(a.Skills, s.Name)
			}
		})
		if err != nil {
			return m, err
		}
	}
	m = m.reloadTeam().loadWork()
	return m, nil
}

// ── settings ────────────────────────────────────────────────────────────

func (m Model) settingsForm() *form {
	names := make([]string, len(m.team))
	for i, a := range m.team {
		names[i] = a.Name
	}
	retry := "no"
	if m.routines.Retry {
		retry = "yes"
	}
	return &form{kind: "settings", title: "settings", file: "team.yaml · routines.yaml", fields: []field{
		{key: "routing", label: "no @", kind: fChoice, options: []string{"lead", "router", "all"},
			show: []string{"the lead answers", "a small model picks", "everyone answers"}, value: orDefault(m.cfg.Routing, "lead")},
		{key: "lead", label: "lead", kind: fChoice, options: names, value: m.cfg.Lead, hint: "answers when nobody fits; may propose agents"},
		{key: "hops", label: "hops", kind: fText, value: strconv.Itoa(m.cfg.MaxHops), hint: "turns per question, 1 to 50"},
		{key: "quiet", label: "quiet hours", kind: fText, value: m.routines.QuietHours, hint: "routines don't run, like 22:00-07:00"},
		{key: "retry", label: "a failed run", kind: fChoice, options: []string{"yes", "no"}, show: []string{"retry once in 5 min", "just report"}, value: retry},
		{key: "notify", label: "notifications", kind: fChoice, options: []string{"all", "window", "off"},
			show: []string{"in the window, and the desktop when it is closed", "only in the window", "off"}, value: orDefault(m.cfg.Notify, "all"),
			hint: "when an agent or a pane waits for you, or finishes"},
	}}
}

func (m Model) saveSettings() Model {
	f := *m.form
	hops, err := strconv.Atoi(f.get("hops"))
	if err != nil {
		f.err = "hops must be a number"
		m.form = &f
		return m
	}
	s := team.Settings{Routing: f.get("routing"), Lead: f.get("lead"), MaxHops: hops, Notify: f.get("notify")}
	if err := team.SaveSettings(m.cfg.TeamDir, s); err != nil {
		f.err = err.Error()
		m.form = &f
		return m
	}
	rf := m.routines
	rf.QuietHours, rf.Retry = f.get("quiet"), f.get("retry") == "yes"
	if err := routine.Save(m.cfg.TeamDir, rf); err != nil {
		f.err = err.Error()
		m.form = &f
		return m
	}
	m.routines = rf
	m.cfg.Routing, m.cfg.Lead, m.cfg.MaxHops, m.cfg.Notify = s.Routing, s.Lead, s.MaxHops, s.Notify
	m.form = nil
	m = m.reloadTeam()
	m.notice = okStyle.Render("✓ saved")
	return m
}

// saveForm saves whatever the open form edits.
func (m Model) saveForm() (Model, tea.Cmd) {
	switch m.form.kind {
	case "agent", "agent-new":
		return m.saveAgent(), nil
	case "routine":
		return m.saveRoutine(), nil
	case "skill":
		return m.saveSkill(), nil
	case "settings":
		return m.saveSettings(), nil
	case "pane":
		return m.savePane()
	case "workspace":
		return m.saveWorkspace()
	}
	return m, nil
}
