package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"tubas/internal/agent"
)

// Lip Gloss is like CSS for the terminal: you build a Style (color, bold,
// width, border) and call Render on a string.
//
// The "quiet log" look: only agent names are colored. Everything else is
// light, grey or faint. What needs you is bright white or shown in
// reverse (dark text on a light bar). Red is only for things that stopped.

// palette maps the color names used in agent files to real colors.
var palette = map[string]lipgloss.Color{
	"orange": "#F0A04B",
	"blue":   "#6FA8DC",
	"purple": "#C79BF2",
	"green":  "#7FC98F",
	"pink":   "#F28DB2",
	"cyan":   "#7CD0D6",
}

var (
	colBg    = lipgloss.Color("#101318")
	colText  = lipgloss.Color("#D5DAE1")
	colDim   = lipgloss.Color("#7D8590")
	colFaint = lipgloss.Color("#3A414B")
	colHi    = lipgloss.Color("#E6EAF0")
	colOK    = lipgloss.Color("#7FC98F")
	colBad   = lipgloss.Color("#EE8A7A")
	colWarn  = lipgloss.Color("#F2B880")

	textStyle  = lipgloss.NewStyle().Foreground(colText)
	dimStyle   = lipgloss.NewStyle().Foreground(colDim)
	faintStyle = lipgloss.NewStyle().Foreground(colFaint)
	hiStyle    = lipgloss.NewStyle().Foreground(colHi)
	boldStyle  = lipgloss.NewStyle().Foreground(colHi).Bold(true)
	okStyle    = lipgloss.NewStyle().Foreground(colOK)
	badStyle   = lipgloss.NewStyle().Foreground(colBad)
	warnStyle  = lipgloss.NewStyle().Foreground(colWarn)
	linkStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#9DB4CC"))

	// Reverse video: the one loud thing on screen, for approvals.
	barStyle = lipgloss.NewStyle().Background(colHi).Foreground(colBg)
	barBold  = barStyle.Bold(true)
)

// agentStyle is the text style in the agent's own color.
func (m Model) agentStyle(name string) lipgloss.Style {
	c, ok := m.colors[name]
	if !ok {
		c = colHi
	}
	return lipgloss.NewStyle().Foreground(c)
}

// spinner frames for agents that are working; the tick advances m.frame.
var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m Model) icon(s Status) string {
	switch s {
	case Done:
		return okStyle.Render("✓")
	case Live:
		return hiStyle.Render(spinner[m.frame%len(spinner)])
	case Waiting:
		return hiStyle.Render("◐")
	case Stopped:
		return badStyle.Render("■")
	case Passed:
		return dimStyle.Render("–")
	default:
		return dimStyle.Render("○")
	}
}

// spread puts left at the start and right at the end of a w-wide line.
// If both don't fit, only left is kept (cut to w).
func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// fit cuts a styled string to w cells. ansi.Truncate understands color
// codes, so it never cuts one in half.
func fit(s string, w int) string {
	return ansi.Truncate(s, max(0, w), "…")
}

// wrap breaks text into lines of at most w cells.
func wrap(s string, w int) []string {
	return strings.Split(lipgloss.NewStyle().Width(max(1, w)).Render(s), "\n")
}

// fitHeight returns exactly h lines: cut (from the top if keepTail, so
// the newest stays visible) or padded with empty lines.
func fitHeight(lines []string, h int, keepTail bool) []string {
	h = max(0, h)
	if len(lines) > h {
		if keepTail {
			return lines[len(lines)-h:]
		}
		return lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines
}

// padRight pads a styled line with spaces to w cells.
func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

// ruleWith draws "── label ─────" across w cells.
func ruleWith(label string, w int) string {
	head := faintStyle.Render("── ") + label + " "
	return head + faintStyle.Render(strings.Repeat("─", max(0, w-lipgloss.Width(head))))
}

// splitCall turns "run_sql(SELECT 1)" into "run_sql" and "SELECT 1".
func splitCall(call string) (name, args string) {
	name, args, ok := strings.Cut(call, "(")
	if !ok {
		return call, ""
	}
	return name, strings.TrimSuffix(args, ")")
}

// toolsText shows an agent's tools as "data: read · reports: write".
func toolsText(a agent.Agent) string {
	if len(a.Tools) == 0 {
		return "no tools"
	}
	names := make([]string, 0, len(a.Tools))
	for n := range a.Tools {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + ": " + a.Tools[n]
	}
	return strings.Join(parts, " · ")
}

// usageText is "57.8k tok · 67% cached · $0.007": tokens alone overstate
// the cost, since cached tokens cost about a tenth.
func usageText(tokens, cached int, cost float64, plan ...bool) string {
	out := fmtTokens(tokens) + " tok"
	if tokens > 0 && cached > 0 {
		out += fmt.Sprintf(" · %d%% cached", cached*100/tokens)
	}
	if cost > 0 {
		out += " · " + fmtCost(cost)
	}
	if len(plan) > 0 && plan[0] {
		out += " · plan" // a subscription: no price per turn
	}
	return out
}

// otherModel is a model worth naming on screen: one that is not Claude's.
func otherModel(m string) bool {
	return m != "" && m != "haiku" && m != "sonnet" && m != "opus" && !strings.HasPrefix(m, "claude")
}

// fmtCost shows US$ with the precision small numbers need.
func fmtCost(c float64) string {
	if c < 0.1 {
		return fmt.Sprintf("$%.3f", c)
	}
	return fmt.Sprintf("$%.2f", c)
}

// fmtTokens shows 4200 as "4.2k".
func fmtTokens(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
