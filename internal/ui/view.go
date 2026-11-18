package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"tubas/internal/tools"
)

const minWidth, minHeight = 60, 18

// View draws the screen. Every view is a list of lines, exactly as tall
// as the terminal:
//
//	header      tubas · #dados                 iris ✓  leo ✓  nico ◐ · hops
//	rule
//	body        the log, the start screen or the grid
//	popup       the @mention menu, while typing @
//	approval    the reverse bar (and its details with v)
//	rule
//	input       › what you type                          hints
func (m Model) View() string {
	if m.width == 0 {
		return "" // no WindowSizeMsg yet
	}
	if m.width < minWidth || m.height < minHeight {
		return m.viewSmall()
	}
	cw := m.width - 2 // one column of margin on each side
	lines := m.screen(cw)
	if m.showHelp {
		lines = m.withHelp(lines, cw)
	}
	for i, l := range lines {
		lines[i] = " " + l
	}
	if m.menu != nil {
		lines = m.withMenu(lines)
	}
	return strings.Join(lines, "\n")
}

func (m Model) screen(cw int) []string {
	top := []string{m.viewHeader(cw), m.viewNav(cw), faintStyle.Render(strings.Repeat("─", cw)), ""}
	if m.form != nil {
		bottom := m.formBottom(cw)
		body := m.viewForm(cw, max(0, m.height-len(top)-len(bottom)))
		return append(append(top, body...), bottom...)
	}
	if m.section != SecChat {
		bottom := m.sectionBottom(cw)
		body := m.sectionBody(cw, max(0, m.height-len(top)-len(bottom)))
		return append(append(top, body...), bottom...)
	}
	bottom := m.viewBottom(cw)
	bodyH := max(0, m.height-len(top)-len(bottom))

	var body []string
	switch {
	case m.connect != nil:
		body = m.viewConnect(cw, bodyH)
	case m.modelPick != nil:
		body = m.viewModelPicker(cw, bodyH)
	case m.mcp != nil:
		body = m.viewMCP(cw, bodyH)
	case m.picker:
		body = m.viewPicker(cw, bodyH)
	case m.review:
		body = m.viewReview(cw, bodyH)
	case m.split():
		body = m.viewPanes(cw, bodyH)
	default:
		body = m.chatBody(cw, bodyH)
	}
	return append(append(top, body...), bottom...)
}

// chatBody is the chat in cw × h: the start screen, the grid or the log.
func (m Model) chatBody(cw, h int) []string {
	switch {
	case !m.session.started() && len(m.past) == 0:
		return fitHeight(m.viewStart(cw, h), h, false)
	case m.grid:
		return m.viewGrid(cw, h)
	default:
		return fitHeight(m.viewLog(cw, h), h, true)
	}
}

// ── header ──────────────────────────────────────────────────────────────

func (m Model) viewHeader(cw int) string {
	left := boldStyle.Render("tubas")
	if m.cfg.Name != "" {
		left += dimStyle.Render("  " + m.cfg.Name)
	}
	if name := m.engineName(); name != "" {
		left += faintStyle.Render(" · " + name)
	}
	if m.grid && m.session.started() {
		left += dimStyle.Render(" · grid")
	}
	s := m.session
	if m.solo() {
		right := dimStyle.Render(m.soloModel())
		if s.started() {
			right += dimStyle.Render(" · " + usageText(s.usage()))
		}
		return spread(left, right, cw)
	}
	if !s.started() {
		return spread(left, m.teamStatus()+dimStyle.Render("     no session yet"), cw)
	}

	hops := fmt.Sprintf("%d/%d hops", len(s.Turns), s.MaxHops)
	hopsText := dimStyle.Render(hops)
	if s.Outcome == HopLimit {
		hopsText = badStyle.Render(hops)
	}
	who := m.teamStatus()
	if m.grid {
		who = m.flowText()
	}
	right := who + dimStyle.Render("     ") + hopsText +
		dimStyle.Render(" · "+usageText(s.usage()))
	return spread(left, right, cw)
}

// engineName is "native · …" or "fake", when the engine says.
func (m Model) engineName() string {
	if named, ok := m.eng.(interface{ Name() string }); ok {
		return named.Name()
	}
	return ""
}

// teamStatus is "iris ✓   leo ⠹   nico ○".
func (m Model) teamStatus() string {
	parts := make([]string, len(m.team))
	for i, a := range m.team {
		parts[i] = m.agentStyle(a.Name).Render(a.Name) + " " + m.icon(m.session.agentStatus(a.Name))
	}
	return strings.Join(parts, "   ")
}

// flowText is "you › iris › leo › nico".
func (m Model) flowText() string {
	out := dimStyle.Render("you")
	for _, t := range m.session.Turns {
		out += dimStyle.Render(" › ") + m.agentStyle(t.Agent).Render(t.Agent)
	}
	return out
}

// ── start screen ────────────────────────────────────────────────────────

func (m Model) viewStart(cw, h int) []string {
	if m.solo() {
		return m.viewSoloStart(cw, h)
	}
	head := fmt.Sprintf("team %s · %d agents", m.cfg.Name, len(m.team))
	connectors := ""
	if len(m.cfg.Connectors) > 0 {
		connectors = "connectors: " + strings.Join(m.cfg.Connectors, ", ")
	}
	var lines []string
	if cw >= tubaleiaWidth+30 && h >= 20 {
		// The tubaleia, with the team beside it.
		side := []string{"", boldStyle.Render("tubas"), dimStyle.Render(head), dimStyle.Render(connectors),
			faintStyle.Render("ctrl+b v opens a terminal next to the chat")}
		lines = append(lines, "")
		for i, row := range tubaleia() {
			text := ""
			if i < len(side) {
				text = side[i]
			}
			lines = append(lines, row+"   "+text)
		}
		lines = append(lines, "")
	} else {
		if connectors != "" {
			head += " · " + connectors
		}
		lines = []string{"", dimStyle.Render(head), ""}
	}
	for _, a := range m.team {
		calls := dimStyle.Render("calls nobody")
		if len(a.CanCall) > 0 {
			names := make([]string, len(a.CanCall))
			for i, n := range a.CanCall {
				names[i] = m.agentStyle(n).Render(n)
			}
			calls = dimStyle.Render("calls ") + strings.Join(names, dimStyle.Render(", "))
		}
		// Pad before styling: fmt counts letters, not color codes.
		row := m.agentStyle(a.Name).Bold(true).Render(fmt.Sprintf("%-8s", a.Name)) +
			textStyle.Render(fmt.Sprintf("%-16s", a.Role)) +
			dimStyle.Render(fmt.Sprintf("%-30s ", toolsText(a))) + calls
		lines = append(lines, fit(row, cw))
	}
	lines = append(lines, "", "",
		dimStyle.Render(m.routingHelp()),
		m.colorMention(m.example),
		"",
		dimStyle.Render("tools that write wait for your ok before they run."),
	)
	for i := range lines {
		lines[i] = fit(lines[i], cw)
	}
	return lines
}

// viewSoloStart greets you in a plain chat: just ask.
func (m Model) viewSoloStart(cw, h int) []string {
	side := []string{"", boldStyle.Render("tubas"), dimStyle.Render("ask anything · " + m.soloModel()),
		dimStyle.Render("/model changes the model · /agents builds a team"), faintStyle.Render("/ for every command · ? for keys")}
	lines := []string{""}
	if cw >= tubaleiaWidth+30 && h >= 12 {
		for i, row := range tubaleia() {
			text := ""
			if i < len(side) {
				text = side[i]
			}
			lines = append(lines, row+"   "+text)
		}
	} else {
		lines = append(lines, side[1:]...)
	}
	for i := range lines {
		lines[i] = fit(lines[i], cw)
	}
	return lines
}

// soloModel is the model of a plain chat's assistant, as shown.
func (m Model) soloModel() string {
	if len(m.team) == 0 {
		return ""
	}
	a := m.team[0]
	model := orDefault(a.Model, "sonnet")
	if a.Provider != "" && a.Provider != "anthropic" {
		model += " · " + a.Provider
	}
	return model
}

// colorMention paints the leading @agent of a text in the agent's color.
func (m Model) colorMention(text string) string {
	if !strings.HasPrefix(text, "@") {
		return textStyle.Render(text)
	}
	name, rest, hasRest := strings.Cut(text[1:], " ")
	if m.agentByName(name) == nil {
		return textStyle.Render(text)
	}
	out := m.agentStyle(name).Render("@" + name)
	if hasRest {
		out += " " + textStyle.Render(rest)
	}
	return out
}

// ── the log ─────────────────────────────────────────────────────────────

// viewLog renders the conversation in h lines. Answers are never cut
// down: when they don't fit, the view scrolls (pgup, pgdown, the wheel),
// following the bottom unless you scrolled back.
func (m Model) viewLog(cw, h int) []string {
	lines := m.logLines(cw, false)
	if len(lines) <= h {
		return lines
	}
	scroll := max(0, min(m.scroll, len(lines)-h))
	end := len(lines) - scroll
	window := lines[end-h : end]
	if scroll > 0 && h > 0 {
		window = append(window[:h-1:h-1], faintStyle.Render(fmt.Sprintf("  ↓ %d more lines · pgdown", scroll)))
	}
	return window
}

// solo says whether the team is one agent: then tubas is a plain chat,
// with none of the orchestration on screen.
func (m Model) solo() bool { return len(m.team) == 1 }

// scrollBy moves the conversation view (positive: back in time).
func (m Model) scrollBy(n int) Model {
	m.scroll = max(0, m.scroll+n)
	return m
}

func (m Model) logLines(cw int, compact bool) []string {
	iw := cw - 2 // two columns of gutter for the selection mark
	s := m.session
	var out []string
	emit := func(block []string, selected bool, agentName string) {
		mark := "  "
		if selected {
			mark = m.agentStyle(agentName).Render("▎") + " "
		}
		for _, l := range block {
			out = append(out, mark+l)
		}
	}

	// Earlier sessions of the conversation: whole in a plain chat, one
	// line per turn when a team worked on them.
	for i, p := range m.past {
		if i > 0 {
			out = append(out, "")
		}
		if m.solo() {
			emit(append([]string{boldStyle.Render("you") + dimStyle.Render("  "+p.AskedAt)}, wrap(m.questionText(p), iw)...), false, "")
			for _, t := range p.Turns {
				out = append(out, "")
				emit(m.turnBlock(t, iw, false), false, t.Agent)
			}
			continue
		}
		lines := []string{fit(dimStyle.Render("you")+"   "+m.questionText(p), iw)}
		for _, t := range p.Turns {
			lines = append(lines, m.turnOneLiner(t, iw))
		}
		if end := outcomeLines(p, iw); len(end) > 0 {
			lines = append(lines, end[0]) // just the closing rule
		}
		emit(lines, false, "")
	}
	if !s.started() {
		return out
	}
	if len(m.past) > 0 {
		out = append(out, "")
	}

	question := m.questionText(s)
	var block []string
	if compact {
		block = []string{fit(boldStyle.Render("you")+"   "+question, iw)}
	} else {
		block = append([]string{boldStyle.Render("you") + dimStyle.Render("  "+s.AskedAt)}, wrap(question, iw)...)
	}
	if route := m.routeLine(s, iw); route != "" && !m.solo() {
		block = append(block, route)
	}
	emit(block, false, "")

	prevOneLiner := compact
	for i, t := range s.Turns {
		selected := i == m.selected
		open := t.Open || t.Status == Live || t.Status == Waiting
		oneLiner := compact && !open && !selected
		if !(oneLiner && prevOneLiner) {
			out = append(out, "")
		}
		if oneLiner {
			emit([]string{m.turnOneLiner(t, iw)}, false, t.Agent)
		} else {
			emit(m.turnBlock(t, iw, open), selected, t.Agent)
		}
		prevOneLiner = oneLiner
	}

	if end := outcomeLines(s, iw); len(end) > 0 && !(m.solo() && s.Outcome == Finished) {
		out = append(out, "")
		emit(end, false, "")
	}
	if code := m.changesLines(iw); len(code) > 0 {
		emit(code, false, "")
	}
	return out
}

// routingHelp explains, on the start screen, who answers a question.
func (m Model) routingHelp() string {
	switch m.cfg.Routing {
	case "router":
		return "ask anything: a router picks who answers. @agent picks yourself, @todos asks everyone."
	case "all":
		return "ask anything: everyone answers, and @" + m.cfg.Lead + " closes. @agent asks just one."
	default:
		return "ask anything: it goes to @" + m.cfg.Lead + ", the lead. @agent picks someone else, @todos asks everyone."
	}
}

// questionText is the question as asked: with the agent's @ when the
// human named one.
func (m Model) questionText(s Session) string {
	if s.Target != "" {
		return m.colorMention("@" + s.Target + " " + s.Question)
	}
	return textStyle.Render(s.Question)
}

// routeLine says who routing picked and why:
//
//	→ router: @leo 0.95 · pipeline question
//	→ everyone: @iris @leo @nico
//	→ @iris · the lead
func (m Model) routeLine(s Session, w int) string {
	r := s.Route
	if r == nil || len(r.Agents) == 0 {
		return ""
	}
	var names []string
	for _, a := range r.Agents {
		name := m.agentStyle(a).Render("@" + a)
		if score, ok := r.Scores[a]; ok && r.By == "router" {
			name += dimStyle.Render(fmt.Sprintf(" %.2f", score))
		}
		names = append(names, name)
	}
	list := strings.Join(names, " ")
	switch r.By {
	case "router":
		return fit(dimStyle.Render("→ router: ")+list+dimStyle.Render(" · "+r.Reason), w)
	case "everyone":
		return fit(dimStyle.Render("→ everyone: ")+list, w)
	default:
		note := " · the lead"
		if strings.HasPrefix(r.Reason, "router failed") {
			note = " · " + r.Reason
		}
		return fit(dimStyle.Render("→ ")+list+dimStyle.Render(note), w)
	}
}

func (m Model) turnOneLiner(t Turn, w int) string {
	line := m.agentStyle(t.Agent).Bold(true).Render(fmt.Sprintf("%-6s", t.Agent))
	if t.Status == Passed {
		return fit(line+dimStyle.Render("passed · nothing to add"), w)
	}
	if t.Summary != "" {
		line += textStyle.Render(plainSummary(t.Summary))
	} else {
		line += badStyle.Render("stopped")
	}
	if len(t.Handoffs) > 0 {
		line += dimStyle.Render("  →")
		for _, h := range t.Handoffs {
			line += " " + m.agentStyle(h.To).Render("@"+h.To)
		}
	}
	return fit(line, w)
}

func (m Model) turnBlock(t Turn, w int, open bool) []string {
	role := ""
	if a := m.agentByName(t.Agent); a != nil {
		role = a.Role
	}
	head := m.agentStyle(t.Agent).Bold(true).Render(t.Agent) + dimStyle.Render("  "+role+" · ")
	if m.solo() {
		head = m.agentStyle(t.Agent).Bold(true).Render(t.Agent) + dimStyle.Render("  ")
		if t.Status == Done || t.Status == Passed {
			info := fmt.Sprintf("%ds", t.Seconds)
			if t.Model != "" {
				info = t.Model + " · " + info
			}
			return append([]string{fit(head+dimStyle.Render(info), w)}, m.turnBody(t, w, open)...)
		}
	}
	switch t.Status {
	case Passed:
		return []string{fit(head+dimStyle.Render("passed · nothing to add"), w)}
	case Live:
		elapsed := int(time.Since(t.StartedAt).Seconds())
		head += hiStyle.Render(fmt.Sprintf("%s working · %ds", spinner[m.frame%len(spinner)], elapsed))
	case Waiting:
		head += hiStyle.Render("waiting for you")
	case Stopped:
		head += badStyle.Render("stopped")
	default:
		cost := ""
		switch {
		case t.Plan:
			cost = " · plan"
		case t.Cost > 0:
			cost = " · " + fmtCost(t.Cost)
		}
		if otherModel(t.Model) {
			cost = " · " + t.Model + cost
		}
		calls := ""
		switch n := t.queries(); n {
		case 0:
		case 1:
			calls = "1 tool call · "
		default:
			calls = fmt.Sprintf("%d tool calls · ", n)
		}
		head += dimStyle.Render(fmt.Sprintf("%s%s tok%s · %ds", calls, fmtTokens(t.Tokens), cost, t.Seconds))
	}
	return append([]string{fit(head, w)}, m.turnBody(t, w, open)...)
}

// turnBody is what a turn did and said, under its head.
func (m Model) turnBody(t Turn, w int, open bool) []string {
	var lines []string
	if t.From == "" && t.Ask != "" { // the parallel answers came back to this agent
		lines = append(lines, fit(dimStyle.Render("↩ "+t.Ask), w))
	}

	if open {
		for _, st := range t.Steps {
			lines = append(lines, fit(stepLine(st), w))
		}
	}
	if len(t.Todos) > 0 && (open || t.Status == Live || t.Status == Waiting) {
		lines = append(lines, todoLines(t.Todos, w)...)
	}
	switch {
	case t.Summary != "":
		lines = append(lines, renderMarkdown(t.Summary, w)...)
	case t.Text != "":
		lines = append(lines, withCursor(renderMarkdown(t.Text, w-1))...) // still writing
	case t.Status == Live && t.Retry != "":
		// The provider is failing: say so, not "thinking" for minutes.
		lines = append(lines, fit(warnStyle.Render("⟳ "+t.Retry)+dimStyle.Render(waited(t)), w))
	case t.Status == Live && len(t.Steps) == 0:
		lines = append(lines, dimStyle.Render("  thinking…"+waited(t)))
	}
	if !open {
		// Decisions you made stay visible even when the turn is collapsed.
		for _, st := range t.Steps {
			if st.Note != "" {
				lines = append(lines, fit(stepLine(st), w))
			}
		}
	}
	if len(t.Handoffs) > 1 {
		lines = append(lines, dimStyle.Render("→ in parallel:"))
	}
	for _, h := range t.Handoffs {
		lines = append(lines, fit(dimStyle.Render("→ ")+m.agentStyle(h.To).Render("@"+h.To)+
			dimStyle.Render(" "+h.Note), w))
	}
	return lines
}

// todoLines draws the agent's checklist: done, doing, to do.
func todoLines(todos []tools.Todo, w int) []string {
	var lines []string
	for _, td := range todos {
		switch td.Status {
		case "completed":
			lines = append(lines, fit(dimStyle.Render("  ☑ "+td.Content), w))
		case "in_progress":
			lines = append(lines, fit(hiStyle.Render("  ◐ "+td.Content), w))
		default:
			lines = append(lines, fit(textStyle.Render("  ☐ "+td.Content), w))
		}
	}
	return lines
}

// waited is how long a live turn has been at it, once that is long
// enough to wonder: " · 12s".
func waited(t Turn) string {
	if t.StartedAt.IsZero() {
		return ""
	}
	if d := time.Since(t.StartedAt); d >= 5*time.Second {
		return fmt.Sprintf(" · %ds", int(d.Seconds()))
	}
	return ""
}

// withCursor adds the typing cursor after the last line.
func withCursor(lines []string) []string {
	cursor := barStyle.Render(" ")
	if len(lines) == 0 {
		return []string{cursor}
	}
	lines[len(lines)-1] = strings.TrimRight(lines[len(lines)-1], " ") + cursor
	return lines
}

func stepLine(st Step) string {
	var line string
	switch st.Kind {
	case ToolCall:
		name, args := splitCall(st.Text)
		return dimStyle.Render("  "+name+" ") + textStyle.Render(args)
	case Result:
		line = okStyle.Render("  ✓ ") + dimStyle.Render(st.Text)
	case Failure:
		line = badStyle.Render("  ✗ ") + dimStyle.Render(st.Text)
	case Senior:
		head, rest, _ := strings.Cut(st.Text, ":")
		line = warnStyle.Render("  ↑ "+head) + textStyle.Render(strings.Join(strings.Fields(rest), " "))
	}
	if st.Note != "" {
		line += dimStyle.Render("  · " + st.Note)
	}
	return line
}

// outcomeLines is the closing rule once a session ends.
func outcomeLines(s Session, w int) []string {
	switch s.Outcome {
	case Finished:
		return []string{ruleWith(okStyle.Render("done")+dimStyle.Render(fmt.Sprintf("  %s · %s · %ds", plural(len(s.Turns), "hop"),
			usageText(s.usage()), int(s.Elapsed.Seconds()))), w)}
	case HopLimit:
		return []string{
			ruleWith(badStyle.Render("stopped")+dimStyle.Render("  "+s.Detail), w),
			"",
			dimStyle.Render("what you can do"),
			hiStyle.Render("  ctrl+r") + dimStyle.Render("  run again with the same question"),
			hiStyle.Render("  @agent") + dimStyle.Render("  bring in someone else to break the tie"),
			hiStyle.Render("  agents/*.md") + dimStyle.Render("  change can_call"),
		}
	case Failed:
		// The whole reason, wrapped: it says what to do, so nothing is cut.
		return append([]string{ruleWith(badStyle.Render("failed"), w)}, wrap(textStyle.Render(s.Detail), w)...)
	case Cancelled:
		return []string{ruleWith(badStyle.Render("stopped")+dimStyle.Render("  by you · ctrl+r runs it again"), w)}
	}
	return nil
}

// ── bottom: popup, approval, input ──────────────────────────────────────

func (m Model) viewBottom(cw int) []string {
	b := m.viewPopup()
	if m.session.Pending != nil {
		b = append(b, m.viewBar(cw))
		if m.showDetails {
			b = append(b, m.viewDetails(cw)...)
		}
	}
	if m.paneFocused() {
		return append(b, "", faintStyle.Render(strings.Repeat("─", cw)), m.paneHint(cw))
	}
	return append(b, "", faintStyle.Render(strings.Repeat("─", cw)), m.viewInput(cw))
}

// viewBar is the approval in reverse video. Every piece is rendered with
// the bar style, so the light background never breaks in the middle.
func (m Model) viewBar(cw int) string {
	p := m.session.Pending
	left := barBold.Render(" "+p.Agent) + barStyle.Render(" wants to run ") + barBold.Render(p.Call)
	v := " details "
	if m.showDetails {
		v = " hide "
	}
	right := barBold.Render("y") + barStyle.Render(" approve   ") + barBold.Render("n") +
		barStyle.Render(" deny   ") + barBold.Render("v") + barStyle.Render(v)
	if lipgloss.Width(left)+lipgloss.Width(right) >= cw {
		right = barStyle.Render(" ") + barBold.Render("y n v") + barStyle.Render(" ")
		left = fit(left, cw-lipgloss.Width(right)-1)
	}
	gap := max(0, cw-lipgloss.Width(left)-lipgloss.Width(right))
	return left + barStyle.Render(strings.Repeat(" ", gap)) + right
}

func (m Model) viewDetails(cw int) []string {
	p := m.session.Pending
	name, args := splitCall(p.Call)
	role, why, from := "", "", "agents/"
	if a := m.agentByName(p.Agent); a != nil {
		role = a.Role
		var writes []string
		for tool, access := range a.Tools {
			if access == "write" {
				writes = append(writes, tool+": write")
			}
		}
		sort.Strings(writes)
		why = strings.Join(writes, ", ") + " · "
		from += filepath.Base(a.File)
	}
	kv := func(k, v string) string { return dimStyle.Render(fmt.Sprintf("%-12s", k)) + v }
	lines := []string{
		"",
		kv("tool", hiStyle.Render(name)),
		kv("arguments", textStyle.Render(args)),
		kv("agent", m.agentStyle(p.Agent).Render(p.Agent)+dimStyle.Render(" · "+role)),
		kv("why asked", dimStyle.Render(why+"tools that write always wait for you")),
		kv("from", dimStyle.Render(from)),
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colHi).
		PaddingLeft(2).
		Width(cw - 1). // Width counts padding, not the border: 1 + (cw-1) = cw
		Render(strings.Join(lines, "\n"))
	return strings.Split(box, "\n")
}

func (m Model) viewPopup() []string {
	if !m.popupOpen() {
		return nil
	}
	all := m.candidates()
	idx := min(m.popupIdx, len(all)-1)
	// Up to 8 rows, scrolling with the selection.
	const rows = 8
	start := 0
	if idx >= rows {
		start = idx - rows + 1
	}
	c := all[start:min(len(all), start+rows)]
	edge := faintStyle.Render("│ ")
	commands := strings.HasPrefix(string(m.input), "/")
	title := "mention an agent"
	nameW := 8
	if commands {
		title = fmt.Sprintf("commands · %d", len(all))
		nameW = 14
		for _, a := range c {
			nameW = max(nameW, min(26, len(a.Name)+2))
		}
	}
	cw := m.width - 2
	lines := []string{edge + dimStyle.Render(title)}
	for i, a := range c {
		name := a.Name
		if commands {
			name = "/" + a.Name
		}
		name = fmt.Sprintf("%-*s", nameW, fit(name, nameW-1))
		role := fit(a.Role, max(10, cw-nameW-12))
		if start+i == idx {
			lines = append(lines, edge+barBold.Render(" "+name)+barStyle.Render(" "+role+" ")+dimStyle.Render(" enter"))
		} else {
			style := m.agentStyle(a.Name)
			if commands {
				style = hiStyle
			}
			lines = append(lines, edge+" "+style.Render(name)+" "+dimStyle.Render(role))
		}
	}
	if more := len(all) - (start + len(c)); more > 0 {
		lines = append(lines, edge+faintStyle.Render(fmt.Sprintf(" ↓ %d more · keep typing to filter", more)))
	}
	return lines
}

func (m Model) viewInput(cw int) string {
	prompt, cursor := hiStyle.Render("› "), barStyle.Render(" ")
	var left string
	switch {
	case len(m.input) > 0:
		text := m.colorMention(string(m.input))
		if room := cw / 2; len(m.input) > room { // long text: show its end
			text = dimStyle.Render("…") + textStyle.Render(string(m.input[len(m.input)-room:]))
		}
		left = prompt + text + cursor
	case m.running && m.session.Pending == nil && m.solo():
		left = dimStyle.Render("› thinking…")
	case m.running && m.session.Pending == nil:
		left = dimStyle.Render("› agents are working…")
	case !m.session.started():
		left = prompt + cursor
	case m.session.Outcome != Ongoing && m.solo():
		left = prompt + cursor + dimStyle.Render(" ask a follow-up")
	case m.session.Outcome != Ongoing:
		left = prompt + cursor + dimStyle.Render(" ask a follow-up, or @agent something new")
	default:
		left = prompt + cursor + dimStyle.Render(" ask anything · @agent picks who answers")
	}
	// The hints stay on the right: they are buttons. A notice goes on the
	// left, in place of the grey text, while nothing is typed.
	right := dimStyle.Render(m.hint())
	if m.notice != "" {
		switch {
		case len(m.input) > 0:
			right = m.notice
		case m.running && m.session.Pending == nil:
			left = dimStyle.Render("› ") + m.notice
		default:
			left = prompt + cursor + " " + m.notice
		}
	}
	return spread(fit(left, cw-lipgloss.Width(right)-2), right, cw)
}

// hint shows the keys that matter right now.
func (m Model) hint() string {
	switch {
	case m.popupOpen():
		return "↑↓ choose · tab complete · esc close"
	case len(m.input) > 0:
		return "enter send · esc clear"
	case m.selected >= 0 && m.selected < len(m.session.Turns):
		if m.session.Turns[m.selected].Open {
			return "↑↓ move · enter collapse · esc back"
		}
		return "↑↓ move · enter expand · esc back"
	case m.grid && m.session.started():
		return "tab focus view · ? keys"
	case m.running && m.session.Pending == nil:
		return "esc stop · ? keys"
	case m.session.Outcome != Ongoing && m.solo():
		return "pgup scroll · / commands · ctrl+n new"
	case m.session.Outcome != Ongoing:
		return "ctrl+r run again · ? keys"
	default:
		return "enter send · / commands · ? keys"
	}
}

// ── grid ────────────────────────────────────────────────────────────────

// viewGrid shows every agent side by side, each with its own log,
// separated by thin rules.
func (m Model) viewGrid(cw, h int) []string {
	n := len(m.team)
	if n == 0 {
		return fitHeight(nil, h, false)
	}
	sep := faintStyle.Render(" │ ")
	pw := (cw - (n-1)*3) / n
	panes := make([][]string, n)
	for i, a := range m.team {
		panes[i] = m.gridPane(i, a.Name, a.Role, pw, h)
	}
	lines := make([]string, h)
	for r := range lines {
		parts := make([]string, n)
		for i := range panes {
			parts[i] = padRight(fit(panes[i][r], pw), pw)
		}
		lines[r] = strings.Join(parts, sep)
	}
	return lines
}

func (m Model) gridPane(i int, name, role string, w, h int) []string {
	s := m.session
	st := s.agentStatus(name)
	head := spread(m.agentStyle(name).Bold(true).Render(fmt.Sprintf("%d %s", i+1, name)), m.icon(st), w)
	var sub string
	switch st {
	case Waiting:
		sub = hiStyle.Render(role + " · waiting for you")
	case Live:
		sub = hiStyle.Render(role + " · working")
	case Stopped:
		sub = badStyle.Render(role + " · stopped")
	case Passed:
		sub = dimStyle.Render(role + " · passed")
	case Done:
		sub = dimStyle.Render(fmt.Sprintf("%s · %s · %ds", role, fmtTokens(s.agentTokens(name)), s.agentSeconds(name)))
	default:
		sub = dimStyle.Render(role + " · idle")
	}
	top := []string{head, sub}

	var body []string
	for _, t := range s.Turns {
		if t.Agent != name {
			continue
		}
		switch t.From {
		case "": // answers coming back from a parallel round
			body = append(body, "", dimStyle.Render("↩ "+t.Ask))
		case "you":
			body = append(body, "", dimStyle.Render("from ")+boldStyle.Render("you"))
			body = append(body, wrap(textStyle.Render(t.Ask), w)...)
		default:
			body = append(body, "", dimStyle.Render("from ")+m.agentStyle(t.From).Render(t.From))
			body = append(body, wrap(textStyle.Render(t.Ask), w)...)
		}
		body = append(body, "")
		for _, step := range t.Steps {
			switch step.Kind {
			case ToolCall:
				body = append(body, dimStyle.Render(step.Text))
			case Result:
				body = append(body, wrap(okStyle.Render("✓ ")+dimStyle.Render(step.Text), w)...)
			case Failure:
				body = append(body, wrap(badStyle.Render("✗ ")+dimStyle.Render(step.Text), w)...)
			}
		}
		if t.Text != "" && t.Summary == "" {
			body = append(body, withCursor(renderMarkdown(t.Text, w-1))...)
		}
		if t.Summary != "" {
			body = append(body, "")
			body = append(body, renderMarkdown(t.Summary, w)...)
		}
		for _, h := range t.Handoffs {
			body = append(body, dimStyle.Render("→ ")+m.agentStyle(h.To).Render("@"+h.To))
		}
	}
	if p := s.Pending; p != nil && p.Agent == name {
		body = append(body, "", hiStyle.Render("? "+p.Call))
	}
	return append(top, fitHeight(body, h-len(top), true)...)
}

// ── overlays ────────────────────────────────────────────────────────────

// withHelp draws the keys panel over a faded copy of the screen.
//
// A terminal has no layers, so "over" means: strip the colors from the
// lines behind, paint them faint, and splice the panel into the middle.
func (m Model) withHelp(base []string, cw int) []string {
	group := func(s string) string { return dimStyle.Render(fmt.Sprintf("%-10s", s)) }
	key := func(s string) string { return hiStyle.Render(fmt.Sprintf("%-16s", s)) }
	rows := [][3]string{
		{"talk", "text", "ask"},
		{"", "@agent", "pick who answers, anywhere in the text"},
		{"", "tab", "complete the @name"},
		{"", "esc", "stop the answer"},
		{},
		{"commands", "/", "every command, filtered as you type"},
		{"", "/new /retry", "new conversation · ask again"},
		{"", "/delete", "delete this conversation (ctrl+g, d: another)"},
		{"", "right click", "a conversation, tab or workspace: rename, delete"},
		{"", "/copy /export", "copy the answer · save as Markdown"},
		{"", "/model /usage", "change the model · what it cost"},
		{"", "/connect", "connect a provider: paste its key"},
		{"", "/mcp /skills", "tools · skills (/name runs a skill)"},
		{"", "/doctor", "check how agents reach a model"},
		{},
		{"decide", "y  n", "approve · deny"},
		{"", "v", "see the full call"},
		{},
		{"read", "↑  ↓", "select a turn"},
		{"", "enter", "expand · collapse it"},
		{"", "tab", "grid of all agents"},
		{},
		{"session", "ctrl+r", "run the last question again"},
		{"", "ctrl+c", "quit"},
		{},
		{"panes", "ctrl+b v  -", "a terminal to the right · below"},
		{"", "ctrl+b a", "an agent CLI (claude, codex…)"},
		{"", "ctrl+b hjkl", "move · z zoom · x close"},
		{"", "ctrl+b q", "detach: everything keeps running"},
		{},
		{"go to", "alt+1…7", "chat · inbox · agents · routines ·"},
		{"", " ", "connectors · files · settings"},
		{"", "esc", "back to the chat, from a section"},
	}
	pw := min(cw-4, 76)
	inner := pw - 6 // border (2) + padding (4)
	lines := []string{spread(boldStyle.Render("keys"), dimStyle.Render("esc close"), inner), ""}
	for _, r := range rows {
		if r[1] == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, fit(group(r[0])+key(r[1])+dimStyle.Render(r[2]), inner))
	}
	panel := strings.Split(lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(colFaint).
		Padding(0, 2).
		Width(pw-2).
		Render(strings.Join(lines, "\n")), "\n")
	pw = lipgloss.Width(panel[0])

	x, y := (cw-pw)/2, 3
	out := make([]string, len(base))
	for i, line := range base {
		plain := []rune(ansi.Strip(line))
		for len(plain) < cw {
			plain = append(plain, ' ')
		}
		if i < y || i >= y+len(panel) || x+pw > len(plain) {
			out[i] = faintStyle.Render(string(plain))
			continue
		}
		out[i] = faintStyle.Render(string(plain[:x])) + panel[i-y] + faintStyle.Render(string(plain[x+pw:]))
	}
	return out
}

func (m Model) viewSmall() string {
	msg := lipgloss.JoinVertical(lipgloss.Center,
		boldStyle.Render("tubas"),
		"",
		textStyle.Render("needs at least ")+hiStyle.Render(fmt.Sprintf("%d×%d", minWidth, minHeight)),
		dimStyle.Render(fmt.Sprintf("this window is %d×%d", m.width, m.height)),
		"",
		dimStyle.Render("make it bigger · agents keep working"),
	)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg)
}

// plural is "1 hop", "3 hops".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
