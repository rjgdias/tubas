package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tubas/internal/agent"
	"tubas/internal/pane"
	"tubas/internal/skill"
)

// ── slash commands ──────────────────────────────────────────────────────
//
// Type / in the chat for the commands, filtered as you type: the
// conversation (/new, /retry, /copy, /export…), the model and the account
// (/model, /usage, /doctor), tools (/mcp, /skills), the app's screens and
// /help. Every skill is a command too, your Claude Code ones included:
// "/pdf summarize report.pdf" asks the assistant to follow that skill.

type command struct {
	name    string
	aliases []string
	desc    string
	arg     bool // takes text after it: enter completes "/name " first
	skill   *skill.Skill
	outside bool // a skill from Claude Code, not in the team yet
}

var baseCommands = []command{
	{name: "new", aliases: []string{"clear"}, desc: "start a new conversation"},
	{name: "conversations", aliases: []string{"resume"}, desc: "your conversations (ctrl+g)"},
	{name: "retry", desc: "ask the last question again"},
	{name: "stop", desc: "stop the answer that is coming (esc)"},
	{name: "copy", desc: "copy the last answer"},
	{name: "export", desc: "save this conversation as a Markdown file"},
	{name: "model", desc: "change the model · /model haiku sets it at once"},
	{name: "usage", desc: "tokens and cost of this conversation"},
	{name: "doctor", desc: "check how agents reach a model"},
	{name: "mcp", desc: "MCP servers · add yours from Claude Code"},
	{name: "skills", desc: "skills · /name runs one, Claude Code's too"},
	{name: "agents", desc: "add a teammate (then they work together)"},
	{name: "inbox", desc: "what waits for you"},
	{name: "routines", desc: "agents on a schedule"},
	{name: "files", desc: "what the agents wrote"},
	{name: "settings", desc: "routing, notifications, providers"},
	{name: "terminal", desc: "a terminal next to the chat (ctrl+b v)"},
	{name: "help", desc: "keys and commands"},
	{name: "quit", aliases: []string{"exit"}, desc: "close the window · everything keeps running"},
}

// commands are the built-in ones, then a command per skill.
func (m Model) commands() []command {
	out := slices.Clone(baseCommands)
	taken := map[string]bool{}
	for _, c := range out {
		taken[c.name] = true
		for _, a := range c.aliases {
			taken[a] = true
		}
	}
	for i := range m.skills {
		s := m.skills[i]
		if !taken[s.Name] {
			taken[s.Name] = true
			out = append(out, command{name: s.Name, desc: "skill · " + s.Description, arg: true, skill: &s})
		}
	}
	for i := range m.external {
		s := m.external[i]
		if !taken[s.Name] {
			taken[s.Name] = true
			out = append(out, command{name: s.Name, desc: "skill from Claude Code · " + s.Description, arg: true, skill: &s, outside: true})
		}
	}
	return out
}

// findCommand is the command a name or alias means.
func (m Model) findCommand(name string) (command, bool) {
	for _, c := range m.commands() {
		if c.name == name || slices.Contains(c.aliases, name) {
			return c, true
		}
	}
	return command{}, false
}

// matchingCommands are the commands for what follows the /: names and
// aliases that start with it first, then those that contain it, in the
// name or the description.
func (m Model) matchingCommands() []command {
	s := string(m.input)
	if !strings.HasPrefix(s, "/") || strings.ContainsRune(s, ' ') {
		return nil
	}
	q := strings.ToLower(s[1:])
	var first, then []command
	for _, c := range m.commands() {
		prefix := strings.HasPrefix(c.name, q)
		for _, a := range c.aliases {
			prefix = prefix || strings.HasPrefix(a, q)
		}
		switch {
		case prefix:
			first = append(first, c)
		case len(q) >= 2 && (strings.Contains(c.name, q) || strings.Contains(strings.ToLower(c.desc), q)):
			then = append(then, c)
		}
	}
	return append(first, then...)
}

// commandCandidates are the matching commands, in the popup's form.
func (m Model) commandCandidates() []agent.Agent {
	cmds := m.matchingCommands()
	if cmds == nil {
		return nil
	}
	out := make([]agent.Agent, len(cmds))
	for i, c := range cmds {
		out[i] = agent.Agent{Name: c.name, Role: c.desc}
	}
	return out
}

// pickCommand is enter (or tab) on a command in the popup.
func (m Model) pickCommand(idx int) (Model, tea.Cmd) {
	cmds := m.matchingCommands()
	c := cmds[min(idx, len(cmds)-1)]
	if c.arg {
		m.input, m.popupIdx = []rune("/"+c.name+" "), 0 // then you type what it is for
		return m, nil
	}
	return m.runCommand(c.name, "")
}

// runCommand does what a /command says; arg is the text after it.
func (m Model) runCommand(name, arg string) (Model, tea.Cmd) {
	m.input, m.popupIdx = nil, 0
	c, ok := m.findCommand(name)
	if !ok {
		m.notice = badStyle.Render("no command /" + name + " · type / to see them")
		return m, nil
	}
	if c.skill != nil {
		return m.runSkill(c, arg)
	}
	switch c.name {
	case "new":
		if m.running {
			m.notice = badStyle.Render("an answer is still coming · esc stops it")
			return m, nil
		}
		return m.newSession(), nil
	case "conversations":
		if m.cfg.Remote == nil {
			m.notice = dimStyle.Render("conversations are kept by the background server (tubas without -local)")
			return m, nil
		}
		m.picker, m.pickIdx = true, 0
		return m, pollStatus(m.cfg.Remote)
	case "retry":
		switch {
		case m.running:
			m.notice = badStyle.Render("an answer is still coming · esc stops it")
		case m.last.question == "":
			m.notice = dimStyle.Render("nothing to ask again yet")
		default:
			return m.start(m.last)
		}
		return m, nil
	case "stop":
		if !m.running {
			m.notice = dimStyle.Render("nothing is running")
			return m, nil
		}
		m = m.escape()
		return m, m.save()
	case "copy":
		text := m.lastAnswer()
		if text == "" {
			m.notice = dimStyle.Render("no answer to copy yet")
			return m, nil
		}
		return m, copyText(text)
	case "export":
		return m.export(arg), nil
	case "model":
		if arg != "" {
			return m.setModel(m.pickerAgent(), arg), nil
		}
		return m.openModelPicker(), nil
	case "usage":
		m.notice = m.usageNotice()
		return m, nil
	case "doctor":
		exe, _ := os.Executable()
		return m.openTool(pane.Spec{Command: exe, Args: []string{"doctor", "-team", m.cfg.TeamDir}, Title: "doctor", Dir: m.cfg.TeamDir},
			"tubas doctor, in a terminal")
	case "terminal":
		return m.openTool(pane.Spec{Dir: orDefault(m.cfg.WorkDir, m.cfg.TeamDir)}, "a terminal")
	case "mcp":
		m.mcp = m.newMCPView()
		m.section = SecChat
		return m, nil
	case "skills":
		m.section, m.skillsMode = SecAgents, true
		return m.loadWork(), nil
	case "agents":
		m.section = SecAgents
		m.form = m.agentForm(agent.Agent{}, "analyst")
		return m, nil
	case "inbox":
		m.section = SecInbox
	case "routines":
		m.section = SecRoutines
	case "files":
		m.section = SecFiles
	case "settings":
		m.section = SecSettings
	case "help":
		m.showHelp = true
	case "quit":
		return m, tea.Quit // the background server keeps everything running
	}
	return m, nil
}

// openTool opens a pane running spec, when panes are on.
func (m Model) openTool(spec pane.Spec, what string) (Model, tea.Cmd) {
	if m.panes == nil {
		m.notice = dimStyle.Render("panes are off here · run " + what + " yourself")
		return m, nil
	}
	return m.openPane(spec, "right")
}

// runSkill asks the assistant (or the lead) to follow a skill: one from
// Claude Code is copied into the team first, and the agent gets it.
func (m Model) runSkill(c command, arg string) (Model, tea.Cmd) {
	if m.running {
		m.notice = badStyle.Render("an answer is still coming · esc stops it")
		return m, nil
	}
	name := c.skill.Name
	if c.outside {
		s, err := skill.Import(m.cfg.TeamDir, c.skill.Source)
		if err != nil {
			m.notice = badStyle.Render("could not bring the skill in: " + err.Error())
			return m, nil
		}
		name = s.Name
		m = m.loadWork()
	}
	who := m.pickerAgent()
	if a := m.agentByName(who); a != nil && !slices.Contains(a.Skills, name) {
		if err := m.editAgent(who, func(a *agent.Agent) { a.Skills = append(slices.Clone(a.Skills), name) }); err != nil {
			m.notice = badStyle.Render("could not give @" + who + " the skill: " + err.Error())
			return m, nil
		}
		m = m.reloadTeam()
	}
	question := fmt.Sprintf("Use the skill %q for this", name)
	if arg = strings.TrimSpace(arg); arg != "" {
		question += ": " + arg
	} else {
		question += "."
	}
	if m.session.started() {
		m.past = append(slices.Clone(m.past), m.session)
	}
	m.scroll = 0
	target := who
	if m.solo() {
		target = "" // alone, it answers anyway: no "@tubas" on screen
	}
	return m.start(request{target: target, question: question})
}

// lastAnswer is the newest answer on screen.
func (m Model) lastAnswer() string {
	sessions := append(slices.Clone(m.past), m.session)
	for i := len(sessions) - 1; i >= 0; i-- {
		turns := sessions[i].Turns
		for j := len(turns) - 1; j >= 0; j-- {
			if t := strings.TrimSpace(turns[j].Summary); t != "" {
				return t
			}
		}
	}
	return ""
}

// usageNotice sums the conversation's tokens and cost.
func (m Model) usageNotice() string {
	var tokens, cached int
	var cost float64
	plan := false
	n := 0
	for _, s := range append(slices.Clone(m.past), m.session) {
		if !s.started() {
			continue
		}
		n++
		t, c, co, p := s.usage()
		tokens, cached, cost, plan = tokens+t, cached+c, cost+co, plan || p
	}
	if n == 0 {
		return dimStyle.Render("nothing used yet in this conversation")
	}
	return textStyle.Render(fmt.Sprintf("this conversation: %s · %s", plural(n, "message"), usageText(tokens, cached, cost, plan)))
}

// export writes the conversation as Markdown, in the folder you are in.
func (m Model) export(path string) Model {
	sessions := append(slices.Clone(m.past), m.session)
	var b strings.Builder
	title := ""
	for _, s := range sessions {
		if !s.started() {
			continue
		}
		if title == "" {
			title = s.Question
			fmt.Fprintf(&b, "# %s\n\n", firstLineText(title))
		}
		fmt.Fprintf(&b, "**you** · %s\n\n%s\n\n", s.AskedAt, s.Question)
		for _, t := range s.Turns {
			if t.Summary == "" {
				continue
			}
			who := t.Agent
			if t.Model != "" {
				who += " · " + t.Model
			}
			fmt.Fprintf(&b, "**%s**\n\n%s\n\n", who, strings.TrimSpace(t.Summary))
		}
	}
	if title == "" {
		m.notice = dimStyle.Render("nothing to export yet")
		return m
	}
	if path == "" {
		path = filepath.Join(orDefault(m.cfg.WorkDir, "."), "tubas-"+time.Now().Format("2006-01-02-150405")+".md")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		m.notice = badStyle.Render("could not save: " + err.Error())
		return m
	}
	shown := path
	if filepath.Dir(path) == filepath.Clean(orDefault(m.cfg.WorkDir, ".")) {
		shown = filepath.Base(path) // in the folder you are in: the name says enough
	}
	m.notice = okStyle.Render("✓ saved " + shown)
	return m
}

func firstLineText(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return first
}
