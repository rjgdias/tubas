package ui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tubas/internal/agent"
	"tubas/internal/pane"
)

// One app, seven sections, always in the top bar. alt+1…7 jumps to a
// section from anywhere; outside the chat, 1…7 and tab work too, since
// there is nothing to type there.
//
// Every section but the chat looks the same: a list on the left, the
// selected item on the right, its keys in the bottom line.

// Section is one part of the app.
type Section int

const (
	SecChat Section = iota
	SecInbox
	SecAgents
	SecRoutines
	SecConnectors
	SecFiles
	SecSettings
	numSections
)

var sectionNames = [numSections]string{"chat", "inbox", "agents", "routines", "connectors", "files", "settings"}

func (s Section) String() string { return sectionNames[s] }

// sectionForKey reads "alt+3" (anywhere) or "3" (outside the chat).
func sectionForKey(key string, bare bool) (Section, bool) {
	digit := strings.TrimPrefix(key, "alt+")
	if digit == key && !bare {
		return 0, false
	}
	if len(digit) == 1 && digit[0] >= '1' && digit[0] <= '0'+byte(numSections) {
		return Section(digit[0] - '1'), true
	}
	return 0, false
}

// goTo opens a section. Files are read when you open them, not on every
// frame.
func (m Model) goTo(s Section) Model {
	m.section = s
	m.notice = ""
	if s == SecFiles {
		m.files = scanFiles(m.cfg.TeamDir, m.cfg.ReportsDir)
	}
	m.secIdx[s] = max(0, min(m.secIdx[s], m.sectionLen(s)-1))
	return m
}

// sectionLen is how many items the section's list has.
func (m Model) sectionLen(s Section) int {
	switch s {
	case SecInbox:
		return m.inboxCount()
	case SecAgents:
		return len(m.team)
	case SecRoutines:
		return len(m.routines.Routines)
	case SecConnectors:
		return len(m.cfg.Connectors)
	case SecFiles:
		return len(m.files)
	}
	return 0
}

// sectionKey handles a key outside the chat.
func (m Model) sectionKey(key string) (Model, tea.Cmd) {
	if key != "d" {
		m.confirm = ""
	}
	// Sub-views of agents take their own keys first.
	if m.section == SecAgents && m.permMode {
		return m.permKey(key), nil
	}
	if m.section == SecAgents && m.skillsMode {
		if s, ok := sectionForKey(key, true); ok {
			m.skillsMode = false
			return m.goTo(s), nil
		}
		return m.skillKey(key), nil
	}
	if s, ok := sectionForKey(key, true); ok {
		return m.goTo(s), nil
	}
	idx := m.secIdx[m.section]
	switch key {
	case "esc":
		return m.goTo(SecChat), nil
	case "tab":
		return m.goTo((m.section + 1) % numSections), nil
	case "shift+tab":
		return m.goTo((m.section + numSections - 1) % numSections), nil
	case "?":
		m.showHelp = true
		return m, nil
	case "up":
		m.secIdx[m.section] = max(0, idx-1)
		return m, nil
	case "down":
		m.secIdx[m.section] = max(0, min(m.sectionLen(m.section)-1, idx+1))
		return m, nil
	}
	switch m.section {
	case SecInbox:
		if idx < len(m.queue) {
			switch key {
			case "y", "n":
				m = m.answerAt(idx, key == "y")
				m.secIdx[SecInbox] = max(0, min(idx, m.inboxCount()-1))
			case "enter":
				if m.queue[idx].routine == "" {
					return m.goTo(SecChat), nil // the approval is there too, with the whole turn
				}
			}
		} else if n := idx - len(m.queue); n < len(m.notes) {
			return m.noteKey(n, key)
		}
	case SecAgents:
		switch key {
		case "n":
			m.form = m.agentForm(agent.Agent{}, "analyst")
		case "enter", "e":
			if idx < len(m.team) {
				m.form = m.agentForm(m.team[idx], "")
			}
		case "p":
			m.permMode, m.permRow, m.permCol = true, idx, 0
		case "s":
			m.skillsMode = true
			m = m.loadWork()
		}
	case SecRoutines:
		return m.routineKey(key)
	case SecFiles:
		if key == "r" {
			return m.goTo(SecFiles), nil
		}
	case SecSettings:
		switch key {
		case "e", "enter":
			if m.cfg.TeamDir != "" {
				m.form = m.settingsForm()
			}
		case "r":
			m.providers = nil
			return m, detectProviders(m.cfg.TeamDir)
		}
	}
	return m, nil
}

// ── top bar ─────────────────────────────────────────────────────────────

var (
	tabOn = lipgloss.NewStyle().Background(colHi).Foreground(colBg).Bold(true)
	badge = lipgloss.NewStyle().Background(colBad).Foreground(colBg).Bold(true)
)

// viewNav is "1 chat  2 inbox 1  3 agents …" with the open one lit.
func (m Model) viewNav(cw int) string {
	var b strings.Builder
	for s := range numSections {
		label := fmt.Sprintf(" %d %s ", s+1, s)
		if s == m.section {
			b.WriteString(tabOn.Render(label))
		} else {
			b.WriteString(dimStyle.Render(label))
		}
		if s == SecInbox && m.inboxCount() > 0 {
			b.WriteString(badge.Render(fmt.Sprintf(" %d ", m.inboxCount())))
		}
		if s == SecRoutines && len(m.runs) > 0 {
			b.WriteString(hiStyle.Render(spinner[m.frame%len(spinner)] + " "))
		}
	}
	right := faintStyle.Render("alt+1…7")
	if convs := m.conversationStates(); m.cfg.Remote != nil && len(convs) > 1 {
		if r := rollupText(convs); r != "" {
			right = r + faintStyle.Render(" · ctrl+g")
		} else {
			right = faintStyle.Render("ctrl+g conversations")
		}
	}
	if m.panes != nil && len(m.panes.infos) > 0 {
		infos := make([]pane.Info, 0, len(m.panes.infos))
		for _, i := range m.panes.infos {
			infos = append(infos, i)
		}
		if r := panesRollup(infos); r != "" {
			if m.cfg.Remote != nil && len(m.conversations()) > 1 {
				right = r + faintStyle.Render(" · ") + right
			} else {
				right = r // in place of the alt+1…7 reminder
			}
		}
	}
	return spread(b.String(), right, cw)
}

// sectionBody draws the open section in exactly h lines.
func (m Model) sectionBody(cw, h int) []string {
	var lines []string
	switch m.section {
	case SecInbox:
		lines = m.viewInbox(cw, h)
	case SecAgents:
		switch {
		case m.permMode:
			lines = m.viewPermissions(cw, h)
		case m.skillsMode:
			lines = m.viewSkills(cw, h)
		default:
			lines = m.viewAgents(cw, h)
		}
	case SecRoutines:
		lines = m.viewRoutines(cw, h)
	case SecConnectors:
		lines = m.viewConnectors(cw, h)
	case SecFiles:
		lines = m.viewFiles(cw, h)
	case SecSettings:
		lines = m.viewSettings(cw)
	}
	var out []string // a line too wide for a small terminal wraps
	for _, l := range lines {
		if lipgloss.Width(l) > cw {
			out = append(out, wrap(l, cw)...)
		} else {
			out = append(out, l)
		}
	}
	return fitHeight(out, h, false)
}

// sectionBottom is the rule and the keys of the open section.
func (m Model) sectionBottom(cw int) []string {
	keys, where := "", ""
	switch m.section {
	case SecInbox:
		keys = "↑↓ item · y/n answer"
		if m.inboxCount() == 0 {
			keys = "1 back to the chat"
		}
	case SecAgents:
		keys, where = "n new · enter edit · p permissions · s skills", "agents/*.md"
		if m.permMode {
			keys = "↑↓←→ move · space none/read/write · ctrl+s save · esc back"
		} else if m.skillsMode {
			keys, where = "↑↓ skill · enter edit · n new · a add · d archive/restore · esc back", ".tubas/skills/"
		} else if len(m.team) > 0 {
			where = "agents/" + filepath.Base(m.team[min(m.secIdx[SecAgents], len(m.team)-1)].File)
		}
	case SecRoutines:
		keys, where = "n new · enter edit · space on/off · r run now · d delete", "routines.yaml"
	case SecConnectors:
		keys, where = "↑↓ connector", "team.yaml"
	case SecFiles:
		keys, where = "↑↓ file · r read again", ".tubas/"
	case SecSettings:
		keys, where = "e edit · r look for AI again", "team.yaml"
	}
	if !(m.section == SecAgents && (m.permMode || m.skillsMode)) {
		keys += " · esc chat"
	}
	left := dimStyle.Render(keys)
	if m.notice != "" {
		left = m.notice
	}
	return []string{"", faintStyle.Render(strings.Repeat("─", cw)), spread(fit(left, cw-lipgloss.Width(where)-2), faintStyle.Render(where), cw)}
}

// panes puts a list and a detail side by side, with a thin rule between.
func panes(left, right []string, lw, cw, h int) []string {
	left, right = fitHeight(left, h, false), fitHeight(right, h, false)
	rw := max(0, cw-lw-3)
	out := make([]string, h)
	for i := range h {
		out[i] = padRight(fit(left[i], lw), lw) + faintStyle.Render(" │ ") + fit(right[i], rw)
	}
	return out
}

// listRow is one line of a list: the selected one is lit.
func listRow(text, right string, selected bool, w int) string {
	if selected {
		return spread(hiStyle.Render("› ")+boldStyle.Render(text), right, w)
	}
	return spread("  "+textStyle.Render(text), right, w)
}

func kv(k, v string) string { return dimStyle.Render(fmt.Sprintf("%-13s", k)) + v }

func listWidth(cw int) int { return max(24, min(36, cw/3)) }

// ── inbox ───────────────────────────────────────────────────────────────

func (m Model) viewInbox(cw, h int) []string {
	if m.inboxCount() == 0 {
		return []string{"",
			boldStyle.Render("Nothing waits for you."),
			"",
			dimStyle.Render("When an agent wants to run a tool that writes, it waits here and in the chat"),
			dimStyle.Render("until you answer. Routine results and the skills agents suggest land here too."),
		}
	}
	lw := listWidth(cw)
	idx := min(m.secIdx[SecInbox], m.inboxCount()-1)
	left := []string{dimStyle.Render(fmt.Sprintf("%d waiting", m.inboxCount())), ""}
	for i, p := range m.queue {
		name, _ := splitCall(p.call)
		left = append(left, listRow("◐ "+p.agent+" "+name, dimStyle.Render(ago(p.at)), i == idx, lw))
	}
	for i, n := range m.notes {
		mark := "✓ "
		switch {
		case n.kind == "skill":
			mark = "✦ "
		case n.failed:
			mark = "✗ "
		}
		label := n.title
		if n.kind == "skill" {
			label = n.title + " · a skill?"
		}
		left = append(left, listRow(mark+label, dimStyle.Render(ago(n.at)), len(m.queue)+i == idx, lw))
	}
	rw := cw - lw - 3
	if idx >= len(m.queue) {
		return panes(left, m.viewNote(m.notes[idx-len(m.queue)], rw), lw, cw, h)
	}
	return panes(left, m.viewApproval(m.queue[idx], rw), lw, cw, h)
}

func (m Model) viewApproval(p pendingApproval, rw int) []string {
	name, args := splitCall(p.call)
	role, writes := "", ""
	if a := m.agentByName(p.agent); a != nil {
		role = " · " + a.Role
		var ws []string
		for tool, access := range a.Tools {
			if access == "write" {
				ws = append(ws, tool)
			}
		}
		sort.Strings(ws)
		writes = strings.Join(ws, ", ")
	}
	right := []string{
		m.agentStyle(p.agent).Bold(true).Render(p.agent) + dimStyle.Render(role) + hiStyle.Render(" wants to run ") + boldStyle.Render(name),
		"",
	}
	detail := p.detail
	if detail == "" {
		detail = args
	}
	for _, l := range strings.Split(detail, "\n") {
		style := textStyle
		if strings.HasPrefix(l, "↑ ") {
			style = warnStyle
		}
		for _, w := range wrap(l, rw) {
			right = append(right, style.Render(w))
		}
	}
	right = append(right, "", kv("waiting", dimStyle.Render("since "+p.at.Format("15:04")+" · "+ago(p.at))))
	if p.routine != "" {
		right = append(right, kv("from", dimStyle.Render("the routine "+p.routine)))
	} else if q := m.session.Question; q != "" {
		right = append(right, kv("asked", dimStyle.Render(q)))
	}
	if writes != "" {
		right = append(right, kv("can write", dimStyle.Render(writes+" · every write waits for you")))
	}
	return append(right, "", barBold.Render(" y ")+barStyle.Render(" approve ")+"  "+barBold.Render(" n ")+barStyle.Render(" deny "))
}

// ago is "now", "3m", "2h".
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// ── agents ──────────────────────────────────────────────────────────────

func (m Model) viewAgents(cw, h int) []string {
	if len(m.team) == 0 {
		return []string{"", dimStyle.Render("no agents yet · add a file in agents/")}
	}
	lw := listWidth(cw)
	idx := m.secIdx[SecAgents]
	left := []string{dimStyle.Render(fmt.Sprintf("%d agents", len(m.team))), ""}
	for i, a := range m.team {
		name := m.agentStyle(a.Name).Bold(i == idx).Render(a.Name)
		mark := "  "
		if i == idx {
			mark = hiStyle.Render("› ")
		}
		lead := ""
		if a.Name == m.cfg.Lead {
			lead = faintStyle.Render("lead")
		}
		left = append(left, spread(mark+name+dimStyle.Render("  "+a.Role), lead, lw))
	}

	a := m.team[idx]
	rw := cw - lw - 3
	or := func(v, d string) string {
		if v == "" {
			return dimStyle.Render(d)
		}
		return textStyle.Render(v)
	}
	right := []string{
		m.agentStyle(a.Name).Bold(true).Render(a.Name) + dimStyle.Render(" · "+a.Role),
		"",
		kv("model", or(a.Model, "default")),
		kv("effort", or(a.Effort, "model's default")),
		kv("runs on", or(a.Harness, "claude")+dimStyle.Render(" · ")+or(a.Provider, "anthropic")),
	}
	if a.TokenLimit > 0 {
		right = append(right, kv("token limit", textStyle.Render(fmtTokens(a.TokenLimit))))
	}
	tools := sortedKeys(a.Tools)
	if len(tools) == 0 {
		right = append(right, kv("tools", dimStyle.Render("none")))
	}
	for i, t := range tools {
		k := ""
		if i == 0 {
			k = "tools"
		}
		access := textStyle.Render(a.Tools[t])
		if a.Tools[t] == "write" {
			access += dimStyle.Render(" · asks you")
		}
		right = append(right, kv(k, textStyle.Render(fmt.Sprintf("%-12s", t))+access))
	}
	calls := dimStyle.Render("nobody")
	if len(a.CanCall) > 0 {
		names := make([]string, len(a.CanCall))
		for i, n := range a.CanCall {
			names[i] = m.agentStyle(n).Render(n)
		}
		calls = strings.Join(names, " ")
	}
	right = append(right, kv("can call", calls), "", dimStyle.Render("instructions"))
	for _, l := range strings.Split(strings.TrimSpace(a.Instructions), "\n") {
		for _, w := range wrap(l, rw) {
			right = append(right, textStyle.Render(w))
		}
	}
	return panes(left, right, lw, cw, h)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ── connectors ──────────────────────────────────────────────────────────

func (m Model) viewConnectors(cw, h int) []string {
	names := m.cfg.Connectors
	if len(names) == 0 {
		return []string{"", dimStyle.Render("no connectors · add them under connectors: in team.yaml")}
	}
	lw := listWidth(cw)
	idx := m.secIdx[SecConnectors]
	left := []string{dimStyle.Render(fmt.Sprintf("%d connectors", len(names))), ""}
	for i, n := range names {
		left = append(left, listRow(n, dimStyle.Render(firstWord(m.cfg.ConnectorKinds[n])), i == idx, lw))
	}

	n := names[idx]
	right := []string{boldStyle.Render(n), ""}
	if kind := m.cfg.ConnectorKinds[n]; kind != "" {
		right = append(right, kv("runs", textStyle.Render(kind)))
	}
	right = append(right, "", dimStyle.Render("who uses it"))
	for _, a := range m.team {
		access, ok := a.Tools[n]
		if !ok {
			access = "none"
		}
		style := textStyle
		if access == "none" {
			style = dimStyle
		}
		right = append(right, m.agentStyle(a.Name).Render(fmt.Sprintf("%-12s", a.Name))+style.Render(access))
	}
	right = append(right, "", dimStyle.Render("tools that write wait for your y, whoever calls them"))
	return panes(left, right, lw, cw, h)
}

// firstWord is the kind alone: "sqlite" of "sqlite · data/tubas.db".
func firstWord(s string) string {
	w, _, _ := strings.Cut(s, " · ")
	return w
}

// ── files ───────────────────────────────────────────────────────────────

// fileEntry is one file the agents wrote: in their workspaces, the shared
// folder or the reports.
type fileEntry struct {
	rel  string // path from the team folder
	size int64
	mod  time.Time
}

// scanFiles lists what the agents wrote, newest first.
func scanFiles(teamDir, reportsDir string) []fileEntry {
	if teamDir == "" {
		return nil
	}
	var out []fileEntry
	roots := []string{filepath.Join(teamDir, ".tubas", "workspaces"), filepath.Join(teamDir, ".tubas", "shared")}
	if reportsDir != "" {
		roots = append(roots, filepath.Join(teamDir, reportsDir))
	}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || len(out) >= 500 {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(teamDir, path)
			out = append(out, fileEntry{filepath.ToSlash(rel), info.Size(), info.ModTime()})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	return out
}

func (m Model) viewFiles(cw, h int) []string {
	if len(m.files) == 0 {
		return []string{"",
			boldStyle.Render("No files yet."),
			"",
			dimStyle.Render("What the agents write shows up here: each one's workspace in .tubas/workspaces,"),
			dimStyle.Render("the team's shared folder in .tubas/shared, and the reports."),
		}
	}
	lw := max(32, min(48, cw*2/5))
	idx := m.secIdx[SecFiles]
	left := []string{dimStyle.Render(fmt.Sprintf("%d files", len(m.files))), ""}
	for i, f := range m.files {
		path := strings.TrimPrefix(strings.TrimPrefix(f.rel, ".tubas/workspaces/"), ".tubas/")
		left = append(left, listRow(path, dimStyle.Render(ago(f.mod)), i == idx, lw))
	}
	start := max(0, idx-(h-4)) // keep the selected file in view
	left = append(left[:2], left[2+start:]...)

	f := m.files[idx]
	right := []string{boldStyle.Render(f.rel), dimStyle.Render(fmt.Sprintf("%s · %s", fmtSize(f.size), f.mod.Format("02 Jan 15:04"))), ""}
	right = append(right, preview(filepath.Join(m.cfg.TeamDir, filepath.FromSlash(f.rel)), cw-lw-3, h-3)...)
	return panes(left, right, lw, cw, h)
}

// preview is the start of a text file, or why it can't be shown.
func preview(path string, w, h int) []string {
	file, err := os.Open(path)
	if err != nil {
		return []string{dimStyle.Render("can't read it: " + err.Error())}
	}
	defer file.Close()
	buf := make([]byte, 8192)
	n, _ := file.Read(buf)
	buf = buf[:n]
	if !utf8.Valid(buf) || strings.ContainsRune(string(buf), 0) {
		return []string{dimStyle.Render("not a text file")}
	}
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(string(buf), "\r\n", "\n"), "\n") {
		l = strings.ReplaceAll(l, "\t", "    ")
		l = strings.Map(func(r rune) rune { // no escape codes from a file
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, l)
		out = append(out, textStyle.Render(fit(l, w)))
		if len(out) >= h {
			break
		}
	}
	return out
}

func fmtSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/1024/1024)
	}
}

// ── settings ────────────────────────────────────────────────────────────

func (m Model) viewSettings(cw int) []string {
	sec := func(s string) string { return dimStyle.Render(strings.ToUpper(s)) }
	routing := map[string]string{
		"lead":   "the lead answers",
		"router": "a small model picks who answers",
		"all":    "everyone answers",
	}[m.cfg.Routing]
	if routing == "" {
		routing = "the lead answers"
	}
	lines := []string{
		sec("conversation"),
		kv("no @", textStyle.Render(routing)),
		kv("lead", m.agentStyle(m.cfg.Lead).Render(m.cfg.Lead)+dimStyle.Render(" · answers when nobody fits, may propose agents")),
		kv("hops", textStyle.Render(fmt.Sprintf("%d per question", m.cfg.MaxHops))),
		"",
		sec("Harnesses and providers"),
		kv("available", textStyle.Render(orDefault(strings.Join(m.cfg.Harnesses, " · "), m.engineName()))),
	}
	if m.providers == nil && m.cfg.TeamDir != "" {
		lines = append(lines, kv("", dimStyle.Render("looking…")))
	}
	for _, p := range m.providers {
		mark := dimStyle.Render("○ ")
		if p.Ready {
			mark = okStyle.Render("● ")
		}
		lines = append(lines, kv("", mark+textStyle.Render(fmt.Sprintf("%-14s", p.Name))+dimStyle.Render(p.Detail)))
	}
	quiet := m.routines.QuietHours
	if quiet == "" {
		quiet = "none"
	}
	retry := "just report"
	if m.routines.Retry {
		retry = "retry once in 5 min"
	}
	lines = append(lines, "",
		sec("routines"),
		kv("run", textStyle.Render(map[bool]string{true: "in the background server, window open or not", false: "while tubas is open"}[m.cfg.Remote != nil])),
		kv("quiet hours", textStyle.Render(quiet)),
		kv("a failed run", textStyle.Render(retry)),
		"",
		sec("this team"),
		kv("folder", textStyle.Render(orDefault(m.cfg.TeamDir, "—"))),
		kv("conversation", dimStyle.Render(orDefault(m.cfg.HistoryFile, "not saved"))),
	)
	return lines
}
