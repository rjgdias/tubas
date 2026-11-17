package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tubas/internal/agent"
	"tubas/internal/engine"
)

// ── commands and the model picker ───────────────────────────────────────
//
// Type / in the chat for the commands: /model changes the model, /new
// starts a new conversation, /agents adds a teammate, /help shows the
// keys. /model (or ctrl+o) lists every model the agent can use: Claude's
// short names, your company gateway's own (from its /v1/models), the
// provider's; type to filter, enter to pick. The choice is saved in the
// agent's file and used from the next message.

var slashCommands = []agent.Agent{
	{Name: "model", Role: "change the model"},
	{Name: "new", Role: "start a new conversation"},
	{Name: "agents", Role: "add a teammate (then they work together)"},
	{Name: "help", Role: "keys and commands"},
}

// commandCandidates are the commands matching what you typed after /.
func (m Model) commandCandidates() []agent.Agent {
	s := string(m.input)
	if !strings.HasPrefix(s, "/") || strings.ContainsRune(s, ' ') {
		return nil
	}
	var out []agent.Agent
	for _, c := range slashCommands {
		if strings.HasPrefix(c.Name, s[1:]) {
			out = append(out, c)
		}
	}
	return out
}

// runCommand does what a /command says.
func (m Model) runCommand(name string) (Model, tea.Cmd) {
	m.input, m.popupIdx = nil, 0
	switch name {
	case "model":
		return m.openModelPicker(), nil
	case "new":
		if m.running {
			m.notice = badStyle.Render("an answer is still coming · esc stops it")
			return m, nil
		}
		return m.newSession(), nil
	case "agents":
		m.section = SecAgents
		m.form = m.agentForm(agent.Agent{}, "analyst")
		return m, nil
	case "help":
		m.showHelp = true
		return m, nil
	}
	m.notice = badStyle.Render("no command /" + name + " · type / to see them")
	return m, nil
}

type modelPicker struct {
	agent  string
	models []string
	idx    int
	filter string
	means  map[string]string // "sonnet" → the gateway's own name for it
}

// pickerAgent is who /model changes: the assistant, or the team's lead.
func (m Model) pickerAgent() string {
	if len(m.team) == 0 {
		return ""
	}
	if m.solo() || m.cfg.Lead == "" {
		return m.team[0].Name
	}
	return m.cfg.Lead
}

func (m Model) openModelPicker() Model {
	name := m.pickerAgent()
	a := m.agentByName(name)
	if a == nil {
		m.notice = badStyle.Render("no agent to change")
		return m
	}
	mp := &modelPicker{agent: name, models: m.modelsFor(orDefault(a.Harness, "claude"), orDefault(a.Provider, "anthropic"), a.Model),
		means: m.aliases}
	for i, model := range mp.models {
		if model == a.Model {
			mp.idx = i
		}
	}
	m.modelPick = mp
	m.section = SecChat
	return m
}

// shown are the models matching the filter.
func (p *modelPicker) shown() []string {
	if p.filter == "" {
		return p.models
	}
	var out []string
	for _, m := range p.models {
		if strings.Contains(strings.ToLower(m), strings.ToLower(p.filter)) {
			out = append(out, m)
		}
	}
	return out
}

func (m Model) modelPickerKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := *m.modelPick
	list := p.shown()
	switch key := msg.String(); key {
	case "esc", "ctrl+c":
		m.modelPick = nil
		return m, nil
	case "up":
		p.idx = max(0, p.idx-1)
	case "down":
		p.idx = max(0, min(len(list)-1, p.idx+1))
	case "tab": // the next agent, in a team
		if len(m.team) > 1 {
			for i, a := range m.team {
				if a.Name == p.agent {
					next := m.team[(i+1)%len(m.team)]
					p.agent = next.Name
					p.models = m.modelsFor(orDefault(next.Harness, "claude"), orDefault(next.Provider, "anthropic"), next.Model)
					p.idx, p.filter = 0, ""
					break
				}
			}
		}
	case "backspace":
		if r := []rune(p.filter); len(r) > 0 {
			p.filter = string(r[:len(r)-1])
			p.idx = 0
		}
	case "enter":
		choice := ""
		if p.idx < len(list) {
			choice = list[p.idx]
		} else if p.filter != "" {
			choice = p.filter // a name that isn't listed: use it as typed
		}
		m.modelPick = nil
		return m.setModel(p.agent, choice), nil
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			p.filter += typedText(msg)
			p.idx = 0
		}
	}
	m.modelPick = &p
	return m, nil
}

// setModel saves an agent's model in its file.
func (m Model) setModel(name, model string) Model {
	a := m.agentByName(name)
	if a == nil || m.cfg.TeamDir == "" {
		return m
	}
	changed := *a
	changed.Model = model
	if _, err := agent.Save(m.agentsDir(), changed); err != nil {
		m.notice = badStyle.Render("could not save: " + err.Error())
		return m
	}
	m = m.reloadTeam()
	m.notice = okStyle.Render("✓ " + name + " now answers with " + orDefault(model, "its default model"))
	return m
}

// aliasMeanings reads what your Claude settings make sonnet, opus and
// haiku mean: a gateway often maps them to its own model names.
func aliasMeanings(workDir string) map[string]string {
	p := engine.FindProvider(workDir)
	out := map[string]string{}
	for _, alias := range []string{"sonnet", "opus", "haiku"} {
		if v := p.Get("ANTHROPIC_DEFAULT_" + strings.ToUpper(alias) + "_MODEL"); v != "" {
			out[alias] = v
		}
	}
	return out
}

func (m Model) viewModelPicker(cw, h int) []string {
	p := m.modelPick
	a := m.agentByName(p.agent)
	where := "anthropic"
	if a != nil && a.Provider != "" {
		where = a.Provider
	}
	title := boldStyle.Render("model") + dimStyle.Render(" for "+p.agent+" · "+where)
	if len(m.team) > 1 {
		title += dimStyle.Render(" · tab: another agent")
	}
	lines := []string{title, ""}
	filter := dimStyle.Render("type to filter")
	if p.filter != "" {
		filter = textStyle.Render(p.filter) + barStyle.Render(" ")
	}
	lines = append(lines, hiStyle.Render("› ")+filter, "")
	list := p.shown()
	if len(list) == 0 {
		lines = append(lines, dimStyle.Render("  nothing matches · enter uses \""+p.filter+"\" as the model name"))
	}
	start := 0
	if room := h - 8; room > 0 && p.idx >= room {
		start = p.idx - room + 1
	}
	for i := start; i < len(list) && len(lines) < h-3; i++ {
		name := list[i]
		label := name
		if name == "" {
			label = "default"
		}
		if means := p.means[name]; means != "" {
			label += "  → " + means
		}
		current := ""
		if a != nil && a.Model == name {
			current = dimStyle.Render("  · now")
		}
		if i == p.idx {
			lines = append(lines, barBold.Render(" "+label+" ")+current)
		} else {
			lines = append(lines, "  "+textStyle.Render(label)+current)
		}
	}
	if m.gatewayNote != "" {
		lines = append(lines, "", dimStyle.Render(m.gatewayNote))
	}
	count := fmt.Sprintf("%d models", len(list))
	if len(list) == 1 {
		count = "1 model"
	}
	lines = append(lines, "", dimStyle.Render(count+" · ↑↓ choose · enter picks · esc closes"))
	return fitHeight(lines, h, false)
}
