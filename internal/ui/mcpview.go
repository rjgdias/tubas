package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tubas/internal/agent"
	"tubas/internal/mcp"
	"tubas/internal/team"
)

// ── /mcp: MCP servers ───────────────────────────────────────────────────
//
// The team's MCP servers, with who uses them, and below them the ones
// your Claude Code has (yours, the project's, .mcp.json) that the team
// doesn't yet. enter on one of those adds it: team.yaml gets
// `claude: NAME` (its tokens stay in Claude Code's file) and the
// assistant may use it, each tool that changes something waiting for
// your ok. enter on one of the team's starts it and counts its tools;
// d removes it.

type mcpRow struct {
	name    string
	runs    string // what starts it
	inTeam  bool
	server  team.ClaudeServer
	users   []string // agents with access
	status  string   // after a test: "12 tools" or the error
	ok      bool
	testing bool
}

type mcpView struct {
	rows []mcpRow
	idx  int
	err  string
}

type mcpProbedMsg struct {
	name  string
	tools int
	err   error
}

func (m Model) newMCPView() *mcpView {
	v := &mcpView{}
	t, err := team.Load(m.cfg.TeamDir)
	if err != nil {
		v.err = err.Error()
		return v
	}
	referenced := map[string]bool{}
	var names []string
	for name, c := range t.Connectors {
		if c.Kind() == "mcp" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		c := t.Connectors[name]
		row := mcpRow{name: name, runs: c.Runs(), inTeam: true}
		for _, a := range t.Agents {
			if access := a.Tools[name]; access == "read" || access == "write" {
				row.users = append(row.users, "@"+a.Name)
			}
		}
		if c.Claude != "" {
			referenced[c.Claude] = true
		}
		referenced[name] = true
		v.rows = append(v.rows, row)
	}
	for _, s := range team.ClaudeServers(m.cfg.WorkDir, m.cfg.TeamDir) {
		if referenced[s.Name] {
			continue
		}
		runs := s.Type + " · " + s.From
		if s.Type == "sse" {
			runs = "sse · " + s.From + " · tubas doesn't speak SSE"
		}
		v.rows = append(v.rows, mcpRow{name: s.Name, runs: runs, server: s})
	}
	if old := m.mcp; old != nil { // keep the cursor and the test results
		v.idx = min(old.idx, max(0, len(v.rows)-1))
		for i := range v.rows {
			for _, o := range old.rows {
				if o.name == v.rows[i].name && o.inTeam == v.rows[i].inTeam {
					v.rows[i].status, v.rows[i].ok = o.status, o.ok
				}
			}
		}
	}
	return v
}

func (m Model) mcpKey(key string) (tea.Model, tea.Cmd) {
	v := *m.mcp
	switch key {
	case "esc", "q", "ctrl+c":
		m.mcp = nil
		return m, nil
	case "up", "k":
		v.idx = max(0, v.idx-1)
	case "down", "j":
		v.idx = min(max(0, len(v.rows)-1), v.idx+1)
	case "enter", "a", "t":
		if v.idx >= len(v.rows) {
			break
		}
		row := v.rows[v.idx]
		if !row.inTeam {
			return m.addMCP(row.server)
		}
		v.rows[v.idx].testing, v.rows[v.idx].status = true, ""
		m.mcp = &v
		return m, m.probeMCP(row.name)
	case "d", "delete":
		if v.idx < len(v.rows) && v.rows[v.idx].inTeam {
			return m.removeMCP(v.rows[v.idx].name), nil
		}
	}
	m.mcp = &v
	return m, nil
}

var notName = regexp.MustCompile(`[^a-z0-9_-]+`)

// addMCP adds one of Claude Code's servers to the team and lets the
// assistant (or the lead) use it.
func (m Model) addMCP(s team.ClaudeServer) (Model, tea.Cmd) {
	if s.Type == "sse" {
		m.notice = badStyle.Render(s.Name + " uses the old SSE transport, which tubas doesn't speak")
		return m, nil
	}
	name := strings.Trim(notName.ReplaceAllString(strings.ToLower(s.Name), "-"), "-")
	if name == "" {
		name = "mcp"
	}
	t, _ := team.Load(m.cfg.TeamDir)
	for base, n := name, 2; t.Connectors[name].Name != ""; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	if err := team.AddConnector(m.cfg.TeamDir, team.Connector{Name: name, Claude: s.Name, ClaudeFrom: s.Dir}); err != nil {
		m.notice = badStyle.Render("could not add " + s.Name + ": " + err.Error())
		return m, nil
	}
	who := m.pickerAgent()
	if a := m.agentByName(who); a != nil {
		// write: tools that change something still wait for you.
		if err := m.editAgent(who, func(a *agent.Agent) { a.Tools[name] = "write" }); err != nil {
			m.notice = badStyle.Render("added, but could not give @" + who + " access: " + err.Error())
			return m, nil
		}
		m = m.reloadTeam()
	}
	m.cfg.Connectors = append(m.cfg.Connectors, name)
	if m.cfg.ConnectorKinds != nil {
		m.cfg.ConnectorKinds[name] = "mcp"
	}
	m.mcp = m.newMCPView()
	for i, r := range m.mcp.rows {
		if r.inTeam && r.name == name {
			m.mcp.idx = i
			m.mcp.rows[i].testing = true
		}
	}
	m.notice = okStyle.Render("✓ "+name+" added") + dimStyle.Render(" · @"+who+" can use it; tools that change something ask you first")
	return m, m.probeMCP(name) // and see that it starts
}

func (m Model) removeMCP(name string) Model {
	if err := team.RemoveConnector(m.cfg.TeamDir, name); err != nil {
		m.notice = badStyle.Render(err.Error())
		return m
	}
	for _, a := range m.team {
		if _, ok := a.Tools[name]; !ok {
			continue
		}
		m.editAgent(a.Name, func(a *agent.Agent) { delete(a.Tools, name) })
	}
	m = m.reloadTeam()
	m.mcp = m.newMCPView()
	m.notice = okStyle.Render("✓ "+name+" removed") + dimStyle.Render(" · enter adds it back")
	return m
}

// probeMCP starts a server in the background and counts its tools.
func (m Model) probeMCP(name string) tea.Cmd {
	dir := m.cfg.TeamDir
	return func() tea.Msg {
		t, err := team.Load(dir)
		if err != nil {
			return mcpProbedMsg{name: name, err: err}
		}
		n, err := mcp.Probe(t.Connectors[name], dir)
		return mcpProbedMsg{name: name, tools: n, err: err}
	}
}

func (m Model) mcpProbed(msg mcpProbedMsg) Model {
	if m.mcp == nil {
		return m
	}
	v := *m.mcp
	v.rows = append([]mcpRow(nil), v.rows...)
	for i := range v.rows {
		if v.rows[i].inTeam && v.rows[i].name == msg.name {
			v.rows[i].testing = false
			if msg.err != nil {
				v.rows[i].status, v.rows[i].ok = msg.err.Error(), false
			} else {
				v.rows[i].status, v.rows[i].ok = plural(msg.tools, "tool"), true
			}
		}
	}
	m.mcp = &v
	return m
}

func (m Model) viewMCP(cw, h int) []string {
	v := m.mcp
	lines := []string{boldStyle.Render("MCP servers") + dimStyle.Render(" · tools for the agents"), ""}
	if v.err != "" {
		lines = append(lines, badStyle.Render(v.err))
	}
	inTeam, outside := 0, 0
	for _, r := range v.rows {
		if r.inTeam {
			inTeam++
		} else {
			outside++
		}
	}
	nameW := 14
	for _, r := range v.rows {
		nameW = max(nameW, min(24, len(r.name)+2))
	}
	row := func(i int, r mcpRow) string {
		name := fmt.Sprintf("%-*s", nameW, fit(r.name, nameW-1))
		var right string
		switch {
		case r.testing:
			right = hiStyle.Render(spinner[m.frame%len(spinner)] + " starting it…")
		case r.status != "" && r.ok:
			right = okStyle.Render("✓ " + r.status)
		case r.status != "":
			right = badStyle.Render("✗ " + r.status)
		case r.inTeam && len(r.users) > 0:
			right = dimStyle.Render(strings.Join(r.users, " "))
		case r.inTeam:
			right = warnStyle.Render("nobody uses it · /agents gives access")
		}
		left := " " + textStyle.Render(name) + " " + dimStyle.Render(r.runs)
		if i == v.idx {
			left = barBold.Render(" "+name) + " " + dimStyle.Render(r.runs)
		}
		if right == "" {
			return fit(left, cw)
		}
		return spread(fit(left, cw-lipgloss.Width(right)-2), right, cw)
	}
	lines = append(lines, dimStyle.Render("in tubas"))
	if inTeam == 0 {
		lines = append(lines, dimStyle.Render("  none yet · add one of yours below"))
	}
	for i, r := range v.rows {
		if r.inTeam {
			lines = append(lines, row(i, r))
		}
	}
	lines = append(lines, "", dimStyle.Render("from your Claude Code · enter adds it"))
	if outside == 0 {
		lines = append(lines, dimStyle.Render("  none: claude mcp add puts one there, or add command:/url: under connectors: in team.yaml"))
	}
	for i, r := range v.rows {
		if !r.inTeam {
			lines = append(lines, row(i, r))
		}
	}
	hint := "↑↓ choose · enter adds or tests it · d removes · esc closes"
	lines = append(lines, "", dimStyle.Render(hint))
	return fitHeight(lines, h, false)
}
