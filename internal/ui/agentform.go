package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"tubas/internal/agent"
)

// ── creating and editing agents ─────────────────────────────────────────

var (
	colorNames  = []string{"orange", "blue", "purple", "green", "pink", "cyan"}
	modelNames  = []string{"haiku", "sonnet", "opus"}
	effortNames = []string{"", "low", "medium", "high"}
	effortShow  = []string{"default", "", "", ""}
	accessNames = []string{"none", "read", "write"}
)

func (m Model) agentsDir() string { return filepath.Join(m.cfg.TeamDir, "agents") }

// connectorsForForms is every connector an agent can be given, the
// built-in ones included.
func (m Model) connectorsForForms() []string {
	names := slices.Clone(m.cfg.Connectors)
	for _, n := range []string{"workspace", "team", "skills"} {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}

// agentForm builds the form for a (new when a.Name == "").
func (m Model) agentForm(a agent.Agent, template string) *form {
	isNew := a.Name == ""
	f := &form{kind: "agent", title: "agent " + a.Name, target: a.Name, file: "agents/" + a.Name + ".md"}
	if isNew {
		f.kind, f.title, f.file = "agent-new", "new agent", "agents/<name>.md"
		names := make([]string, len(agent.Templates))
		for i, t := range agent.Templates {
			names[i] = t.Key
		}
		f.fields = append(f.fields, field{key: "template", label: "start from", kind: fChoice, options: names, value: template,
			hint: "fills the rest; you can change any of it"})
	}
	provider := orDefault(a.Provider, "anthropic")
	providers := m.providerNames()
	if !slices.Contains(providers, provider) {
		providers = append(providers, provider)
	}
	models := m.modelsFor(provider, a.Model)
	color := a.Color
	if color == "" {
		color = m.freeColor()
	}
	f.fields = append(f.fields,
		field{key: "name", label: "name", kind: fText, value: a.Name, hint: "lowercase, no spaces: how you call it, @name"},
		field{key: "role", label: "role", kind: fText, value: a.Role, hint: "a few words, shown next to the name"},
		field{key: "color", label: "color", kind: fChoice, options: colorNames, value: color},
		field{key: "provider", label: "provider", kind: fChoice, options: providers, value: provider,
			hint: "where the model is served · /connect adds one · anthropic: your key or company gateway"},
		field{key: "model", label: "model", kind: fChoice, options: models, show: modelShow(models), value: modelValue(models, a.Model),
			hint: modelHint(provider)},
		field{key: "effort", label: "effort", kind: fChoice, options: effortNames, show: effortShow, value: a.Effort,
			hint: "thinking is the priciest token"},
	)
	for _, c := range m.connectorsForForms() {
		access := a.Tools[c]
		if access == "" {
			access = "none"
		}
		hint := ""
		switch c {
		case "team":
			hint = "propose new agents and routines (each waits for you)"
		case "skills":
			hint = "load the skills listed below"
		case "workspace":
			hint = "its own folder and the team's shared one"
		}
		f.fields = append(f.fields, field{key: "tool:" + c, label: "· " + c, kind: fChoice, options: accessNames, value: access, hint: hint})
	}
	escalate, triggers, perDay := "off", "tool_fails, before_write, ask", ""
	if e := a.Escalate; e != nil {
		escalate, triggers = e.To, strings.Join(e.Triggers(), ", ")
		if e.PerDay > 0 {
			perDay = strconv.Itoa(e.PerDay)
		}
	}
	escOptions := []string{"off", "sonnet", "opus"}
	if !slices.Contains(escOptions, escalate) {
		escOptions = append(escOptions, escalate)
	}
	f.fields = append(f.fields,
		field{key: "can_call", label: "can call", kind: fText, value: strings.Join(a.CanCall, ", "), hint: "teammates, separated by commas"},
		field{key: "skills", label: "skills", kind: fText, value: strings.Join(a.Skills, ", "), hint: "skill names, separated by commas"},
		field{key: "escalate", label: "escalation", kind: fChoice, options: escOptions, value: escalate,
			hint: "a stronger model for the hard step, then back"},
		field{key: "escalate_on", label: "· when", kind: fText, value: triggers, hint: "tool_fails, before_write, ask"},
		field{key: "escalate_day", label: "· at most a day", kind: fText, value: perDay, hint: "empty = 5"},
		field{key: "instructions", label: "instructions", kind: fLong, value: a.Instructions},
	)
	if isNew {
		m.applyTemplate(f, template)
	}
	return f
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// freeColor picks a color no agent has yet.
func (m Model) freeColor() string {
	for _, c := range []string{"green", "pink", "cyan", "orange", "blue", "purple"} {
		taken := false
		for _, a := range m.team {
			taken = taken || a.Color == c
		}
		if !taken {
			return c
		}
	}
	return "cyan"
}

// applyTemplate fills a new agent's form from a template.
func (m Model) applyTemplate(f *form, key string) {
	for _, t := range agent.Templates {
		if t.Key != key {
			continue
		}
		f.set("role", t.Role)
		f.set("model", t.Model)
		f.set("effort", t.Effort)
		f.set("instructions", t.Instructions)
		for _, c := range m.connectorsForForms() {
			access := t.Access
			switch c {
			case "workspace":
				access = "write"
			case "team", "skills":
				access = "none"
			}
			if access == "" {
				access = "none"
			}
			f.set("tool:"+c, access)
		}
	}
}

// providerNames are the model providers the form offers.
func (m Model) providerNames() []string {
	names := []string{"anthropic"}
	for n := range m.cfg.Providers {
		if n != "anthropic" {
			names = append(names, n)
		}
	}
	slices.Sort(names[1:])
	return names
}

// modelsFor are the models to offer for a provider; "" is the
// provider's own default.
func (m Model) modelsFor(provider, current string) []string {
	var models []string
	switch {
	case provider == "anthropic" || provider == "":
		// Claude's short names, then your gateway's own. A gateway serves
		// its own names: a short name only works when your settings map it
		// (ANTHROPIC_DEFAULT_SONNET_MODEL…), so only those are listed.
		for _, alias := range modelNames {
			if !m.onGateway || m.aliases[alias] != "" {
				models = append(models, alias)
			}
		}
		models = append(models, m.cfg.Providers["anthropic"]...)
	default:
		models = append([]string{""}, m.cfg.Providers[provider]...)
	}
	if current != "" && !slices.Contains(models, current) {
		models = append(models, current)
	}
	return models
}

func modelValue(models []string, current string) string {
	if slices.Contains(models, current) {
		return current
	}
	if slices.Contains(models, "sonnet") {
		return "sonnet"
	}
	return models[0]
}

func modelShow(models []string) []string {
	show := make([]string, len(models))
	for i, m := range models {
		if m == "" {
			show[i] = "default"
		}
	}
	return show
}

func modelHint(provider string) string {
	if provider == "anthropic" || provider == "" {
		return "haiku: cheap, watching · sonnet: everyday · opus: hard digging"
	}
	return provider + "'s models · default: its first one"
}

// formChanged reacts to a choice: a new template refills the form.
func (m Model) formChanged(key string) Model {
	if key == "run" && m.form != nil && m.form.kind == "pane" {
		return m.paneFormRunChanged()
	}
	if key == "provider" && m.form != nil {
		f := *m.form
		f.fields = slices.Clone(f.fields)
		models := m.modelsFor(f.get("provider"), "")
		for i := range f.fields {
			if f.fields[i].key == "model" {
				f.fields[i].options, f.fields[i].show = models, modelShow(models)
				f.fields[i].value = modelValue(models, f.fields[i].value)
				f.fields[i].hint = modelHint(f.get("provider"))
			}
		}
		m.form = &f
		return m
	}
	if key == "template" && m.form != nil {
		f := *m.form
		f.fields = slices.Clone(f.fields)
		m.applyTemplate(&f, f.get("template"))
		m.form = &f
	}
	return m
}

// agentFromForm reads the form back into an agent.
func (m Model) agentFromForm(f *form) (agent.Agent, error) {
	a := agent.Agent{Tools: map[string]string{}}
	if f.target != "" {
		if old := m.agentByName(f.target); old != nil {
			a = *old
			a.Tools = map[string]string{}
		}
	}
	a.Name, a.Role, a.Color = f.get("name"), f.get("role"), f.get("color")
	a.Model, a.Effort, a.Instructions = f.get("model"), f.get("effort"), f.get("instructions")
	a.Harness, a.Provider = "", f.get("provider")
	if a.Provider == "anthropic" {
		a.Provider = "" // the default: your Claude setup
	}
	for _, fl := range f.fields {
		if c, ok := strings.CutPrefix(fl.key, "tool:"); ok && fl.value != "none" {
			a.Tools[c] = fl.value
		}
	}
	a.CanCall = nil
	for _, n := range splitList(f.get("can_call")) {
		n = strings.TrimPrefix(n, "@")
		if n == a.Name {
			return a, errors.New("an agent can't call itself")
		}
		if m.agentByName(n) == nil {
			return a, fmt.Errorf("can call: no agent named %s", n)
		}
		a.CanCall = append(a.CanCall, n)
	}
	a.Skills = splitList(f.get("skills"))
	a.Escalate = nil
	if to := f.get("escalate"); to != "off" && to != "" {
		e := &agent.Escalation{To: to, On: splitList(f.get("escalate_on"))}
		if d := f.get("escalate_day"); d != "" {
			n, err := strconv.Atoi(d)
			if err != nil || n < 1 {
				return a, errors.New("escalation: at most a day must be a number")
			}
			e.PerDay = n
		}
		a.Escalate = e
	}
	if f.kind == "agent-new" || a.Name != f.target {
		if m.agentByName(a.Name) != nil {
			return a, fmt.Errorf("an agent named %s already exists", a.Name)
		}
	}
	return a, nil
}

// editAgent changes one agent's file as it is on disk, not the copy on
// screen: that one has the team's defaults filled in (workspace access,
// say), which would end up written in the file.
func (m Model) editAgent(name string, change func(a *agent.Agent)) error {
	agents, err := agent.LoadTeam(m.agentsDir())
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.Name == name {
			if a.Tools == nil {
				a.Tools = map[string]string{}
			}
			change(&a)
			_, err := agent.Save(m.agentsDir(), a)
			return err
		}
	}
	return fmt.Errorf("no agent named %s", name)
}

// saveAgent writes the agent's file and reloads the team.
func (m Model) saveAgent() Model {
	f := *m.form
	if m.cfg.TeamDir == "" {
		f.err = "this team has no folder to save to"
		m.form = &f
		return m
	}
	a, err := m.agentFromForm(&f)
	if err == nil {
		a, err = agent.Save(m.agentsDir(), a)
	}
	if err != nil {
		f.err = err.Error()
		m.form = &f
		return m
	}
	// Renaming: teammates that called the old name now call the new one.
	if f.target != "" && f.target != a.Name {
		for _, other := range m.team {
			if slices.Contains(other.CanCall, f.target) {
				m.editAgent(other.Name, func(o *agent.Agent) {
					if i := slices.Index(o.CanCall, f.target); i >= 0 {
						o.CanCall = slices.Clone(o.CanCall)
						o.CanCall[i] = a.Name
					}
				})
			}
		}
	}
	m.form = nil
	m = m.reloadTeam()
	for i, t := range m.team {
		if t.Name == a.Name {
			m.secIdx[SecAgents] = i
		}
	}
	m.notice = okStyle.Render("✓ saved agents/" + a.Name + ".md")
	return m
}

// reloadTeam reads the agents again after a change on disk. Built-in
// access (the workspace for everyone, team for the lead) is added the way
// team.Load adds it.
func (m Model) reloadTeam() Model {
	if m.cfg.TeamDir == "" {
		return m
	}
	team, err := agent.LoadTeam(m.agentsDir())
	if err != nil {
		m.notice = badStyle.Render(err.Error())
		return m
	}
	for i := range team {
		a := &team[i]
		if _, ok := a.Tools["workspace"]; !ok {
			a.Tools["workspace"] = "write"
		}
		if _, ok := a.Tools["team"]; !ok && a.Name == m.cfg.Lead && len(team) > 1 { // as team.Load does
			a.Tools["team"] = "write"
		}
		if _, ok := a.Tools["skills"]; !ok && len(a.Skills) > 0 {
			a.Tools["skills"] = "read"
		}
	}
	m.team = team
	for _, a := range team {
		m.colors[a.Name] = palette[a.Color]
	}
	return m
}

// ── permissions: every agent, every connector ───────────────────────────

// permKey handles keys in the permissions table.
func (m Model) permKey(key string) Model {
	conns := m.connectorsForForms()
	switch key {
	case "up":
		m.permRow = max(0, m.permRow-1)
	case "down":
		m.permRow = min(len(m.team)-1, m.permRow+1)
	case "left":
		m.permCol = max(0, m.permCol-1)
	case "right":
		m.permCol = min(len(conns)-1, m.permCol+1)
	case " ", "space":
		if m.permRow < len(m.team) && m.permCol < len(conns) {
			team := slices.Clone(m.team)
			a := team[m.permRow]
			tools := map[string]string{}
			for k, v := range a.Tools {
				tools[k] = v
			}
			c := conns[m.permCol]
			cur := tools[c]
			if cur == "" {
				cur = "none"
			}
			tools[c] = accessNames[(slices.Index(accessNames, cur)+1)%len(accessNames)]
			a.Tools = tools
			team[m.permRow] = a
			m.team = team
			m.permDirty = true
		}
	case "ctrl+s":
		if m.cfg.TeamDir == "" {
			m.notice = badStyle.Render("this team has no folder to save to")
			return m
		}
		for _, a := range m.team {
			if _, err := agent.Save(m.agentsDir(), a); err != nil {
				m.notice = badStyle.Render(err.Error())
				return m
			}
		}
		m.permDirty = false
		m = m.reloadTeam()
		m.notice = okStyle.Render("✓ saved every agent's access")
	case "esc", "p":
		if m.permDirty {
			m = m.reloadTeam() // drop what was not saved
			m.permDirty = false
			m.notice = dimStyle.Render("changes dropped")
		}
		m.permMode = false
	}
	return m
}

func (m Model) viewPermissions(cw, h int) []string {
	conns := m.connectorsForForms()
	cell := max(9, min(14, (cw-14)/max(1, len(conns))))
	head := dimStyle.Render(fmt.Sprintf("%-12s", "who"))
	for _, c := range conns {
		head += dimStyle.Render(fmt.Sprintf("%-*s", cell, fit(c, cell-1)))
	}
	lines := []string{boldStyle.Render("permissions") + dimStyle.Render(" · every agent, every connector"), "", head,
		faintStyle.Render(strings.Repeat("─", min(cw, 12+cell*len(conns))))}
	for r, a := range m.team {
		row := m.agentStyle(a.Name).Bold(true).Render(fmt.Sprintf("%-12s", a.Name))
		for c, conn := range conns {
			v := a.Tools[conn]
			if v == "" {
				v = "none"
			}
			text := fmt.Sprintf("%-*s", cell, v)
			switch {
			case r == m.permRow && c == m.permCol:
				text = barBold.Render(fmt.Sprintf(" %-*s", cell-1, v))
			case v == "write":
				text = hiStyle.Render(text)
			case v == "none":
				text = faintStyle.Render(fmt.Sprintf("%-*s", cell, "—"))
			default:
				text = textStyle.Render(text)
			}
			row += text
		}
		lines = append(lines, row)
	}
	state := dimStyle.Render("write always waits for your y in the inbox · team: propose agents and routines")
	if m.permDirty {
		state = warnStyle.Render("● not saved") + dimStyle.Render(" · ctrl+s saves · esc drops")
	}
	lines = append(lines, "", state)
	return fitHeight(lines, h, false)
}
