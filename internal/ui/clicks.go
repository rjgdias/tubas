package ui

import (
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// ── clicks on what the screen says ──────────────────────────────────────
//
// Every hint on screen says a key and what it does: "enter send",
// "/ commands", "? keys", "esc stop", "ctrl+s save", "y deletes it for
// good", "d removes", "/model changes the model". A click on one does
// what the key does. clickHint reads the line as drawn, so a new hint
// is a button without anything else to keep in step.

// hintKeys are the keys a hint may name, and how to press them.
var hintKeys = map[string]tea.KeyMsg{
	"enter": {Type: tea.KeyEnter}, "esc": {Type: tea.KeyEsc}, "tab": {Type: tea.KeyTab},
	"space": {Type: tea.KeySpace}, "pgup": {Type: tea.KeyPgUp}, "pgdown": {Type: tea.KeyPgDown},
	"ctrl+n": {Type: tea.KeyCtrlN}, "ctrl+g": {Type: tea.KeyCtrlG}, "ctrl+o": {Type: tea.KeyCtrlO},
	"ctrl+r": {Type: tea.KeyCtrlR}, "ctrl+s": {Type: tea.KeyCtrlS}, "ctrl+u": {Type: tea.KeyCtrlU},
	"ctrl+b": {Type: tea.KeyCtrlB},
}

// letterKeys are the one-letter keys hints name ("y approve", "d
// removes"); a letter counts only as the first word of a piece, so the
// word "a" in a sentence is never a key.
const letterKeys = "ynvdreapstxcqnbw?/1234567"

// screenLine is a line of the screen as plain text.
func (m Model) screenLine(y int) string {
	lines := strings.Split(m.View(), "\n")
	if y < 0 || y >= len(lines) {
		return ""
	}
	return ansi.Strip(lines[y])
}

// pieceAt is the piece of a hint line under column x: pieces are split
// by " · ", by two spaces or more, and after a question ("delete it? y
// deletes it").
func pieceAt(line string, x int) string {
	r := []rune(line)
	if x < 0 || x >= len(r) {
		return ""
	}
	isCut := func(i int) bool { // is r[i] inside a cut between pieces?
		switch {
		case r[i] == '·' && i > 0 && i+1 < len(r) && r[i-1] == ' ' && r[i+1] == ' ':
			return true
		case r[i] == ' ' && ((i > 0 && r[i-1] == ' ') || (i+1 < len(r) && r[i+1] == ' ')):
			return true
		case r[i] == ' ' && i > 0 && r[i-1] == '?' && i > 1 && !unicode.IsSpace(r[i-2]):
			return true // "it? y deletes": a question ends a piece
		case r[i] == '·' && i > 0 && i+1 < len(r) && (r[i-1] == ' ' || r[i+1] == ' '):
			return true
		}
		return false
	}
	if isCut(x) { // between two pieces: nothing there
		return ""
	}
	start, end := x, x
	for start > 0 && !isCut(start-1) {
		start--
	}
	for end < len(r)-1 && !isCut(end+1) {
		end++
	}
	return strings.TrimSpace(string(r[start : end+1]))
}

// keysFor reads a piece as key presses: "esc stop" is esc, "ctrl+b b
// hide" is ctrl+b then b, "/model changes the model" is that command.
func keysFor(piece string) (keys []tea.KeyMsg, command string) {
	words := strings.Fields(piece)
	if len(words) == 0 {
		return nil, ""
	}
	first := words[0]
	if strings.HasPrefix(first, "/") && len(first) > 1 {
		return nil, strings.TrimPrefix(first, "/")
	}
	if k, ok := hintKeys[first]; ok {
		keys = append(keys, k)
		if first == "ctrl+b" && len(words) > 1 && len([]rune(words[1])) == 1 {
			keys = append(keys, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(words[1])})
		}
		return keys, ""
	}
	if r := []rune(first); len(r) == 1 && strings.ContainsRune(letterKeys, r[0]) && len(words) > 1 {
		return []tea.KeyMsg{{Type: tea.KeyRunes, Runes: r}}, ""
	}
	if first == "?" || first == "/" {
		return []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune(first)}}, ""
	}
	return nil, ""
}

// clickHint does what the hint under the mouse says.
func (m Model) clickHint(msg tea.MouseMsg) (Model, tea.Cmd) {
	keys, command := keysFor(pieceAt(m.screenLine(msg.Y), msg.X))
	if command != "" {
		if _, ok := m.findCommand(command); ok && m.mcp == nil && m.modelPick == nil {
			return m.runCommand(command, "")
		}
		return m, nil
	}
	var cmds []tea.Cmd
	for _, k := range keys {
		next, cmd := m.handleKey(k)
		m = next.(Model)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// clickHeader: the model opens the model list; an agent's name opens it
// among the agents.
func (m Model) clickHeader(x int) (Model, tea.Cmd) {
	line := m.screenLine(0)
	word := wordAt(line, x)
	if word == "" {
		return m, nil
	}
	if m.solo() {
		if strings.Contains(m.soloModel(), word) || word == "sonnet" || word == "opus" || word == "haiku" {
			return m.openModelPicker(), nil
		}
		return m, nil
	}
	for i, a := range m.team {
		if a.Name == word {
			m.section, m.secIdx[SecAgents] = SecAgents, i
			return m, nil
		}
	}
	return m, nil
}

// wordAt is the word under column x.
func wordAt(line string, x int) string {
	r := []rune(line)
	if x < 0 || x >= len(r) || unicode.IsSpace(r[x]) {
		return ""
	}
	start, end := x, x
	for start > 0 && !unicode.IsSpace(r[start-1]) {
		start--
	}
	for end < len(r)-1 && !unicode.IsSpace(r[end+1]) {
		end++
	}
	return string(r[start : end+1])
}

// clickPopup picks a row of the / or @ menu; row 0 is its title.
func (m Model) clickPopup(row int) (Model, tea.Cmd, bool) {
	if !m.popupOpen() || row < 1 || row > len(m.viewPopup())-1 {
		return m, nil, false
	}
	all := m.candidates()
	start := 0
	if idx := min(m.popupIdx, len(all)-1); idx >= 8 {
		start = idx - 8 + 1
	}
	i := start + row - 1
	if i >= len(all) || i >= start+8 {
		return m, nil, false // the "↓ more" line
	}
	if m.commandCandidates() != nil {
		mm, cmd := m.pickCommand(i)
		return mm, cmd, true
	}
	m.popupIdx = i
	return m.complete(), nil, true
}

// clickModelPicker picks a model from the list.
func (m Model) clickModelPicker(msg tea.MouseMsg, by, h int) (Model, tea.Cmd) {
	p := *m.modelPick
	list := p.shown()
	start := 0
	if room := h - 8; room > 0 && p.idx >= room {
		start = p.idx - room + 1
	}
	if i := start + by - 4; by >= 4 && i < len(list) && i >= start {
		p.idx = i
		m.modelPick = &p
		return m.modelPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	}
	return m.clickHint(msg) // "esc closes"
}

// clickMCP selects a server; a click on the selected one adds or tests
// it, as enter does.
func (m Model) clickMCP(msg tea.MouseMsg, by int) (Model, tea.Cmd) {
	v := m.mcp
	line := 2
	if v.err != "" {
		line++
	}
	rowAt := map[int]int{}
	line++ // "in tubas"
	inTeam := 0
	for _, r := range v.rows {
		if r.inTeam {
			inTeam++
		}
	}
	if inTeam == 0 {
		line++
	}
	for i, r := range v.rows {
		if r.inTeam {
			rowAt[line] = i
			line++
		}
	}
	line += 2 // a blank line and "from your Claude Code"
	if len(v.rows) == inTeam {
		line++
	}
	for i, r := range v.rows {
		if !r.inTeam {
			rowAt[line] = i
			line++
		}
	}
	i, ok := rowAt[by]
	if !ok {
		return m.clickHint(msg) // "enter adds or tests it · d removes · esc closes"
	}
	if v.idx != i {
		copied := *v
		copied.idx = i
		m.mcp = &copied
		return m, nil
	}
	next, cmd := m.mcpKey("enter")
	return next.(Model), cmd
}

// clickSection selects a row of a section's list; a click on the
// selected row opens it, as enter does. Elsewhere, hints are buttons.
func (m Model) clickSection(msg tea.MouseMsg, bx, by, cw int) (Model, tea.Cmd) {
	if !(m.section == SecAgents && (m.permMode || m.skillsMode)) &&
		bx < listWidth(cw) && by >= 2 && by-2 < m.sectionLen(m.section) {
		if m.secIdx[m.section] == by-2 {
			next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
			return next.(Model), cmd
		}
		m.secIdx[m.section] = by - 2
		return m, nil
	}
	return m.clickHint(msg)
}

// clickChat: on the start screen its suggestions are buttons
// ("/model changes the model"); in the conversation, "↓ N more lines"
// goes back to the bottom.
func (m Model) clickChat(msg tea.MouseMsg) (Model, tea.Cmd) {
	line := m.screenLine(msg.Y)
	if strings.Contains(line, "more lines · pgdown") {
		m.scroll = 0
		return m, nil
	}
	if !m.session.started() && len(m.past) == 0 {
		return m.clickHint(msg)
	}
	return m, nil
}

// clickForm focuses the field clicked; on a choice, the option clicked
// is chosen (as ←→ would); on a long text, it opens your editor.
func (m Model) clickForm(msg tea.MouseMsg, bx, by int) (Model, tea.Cmd) {
	f := m.form
	h := m.height - 4 - len(m.formBottom(m.width-2))
	start := 0
	if row := f.idx + 2; row >= h-1 {
		start = row - h + 2
	}
	i := by - 2 + start
	if by < 2 || i < 0 || i >= len(f.fields) {
		return m.clickHint(msg) // "ctrl+s save · esc cancel"
	}
	fl := f.fields[i]
	copied := *f
	copied.idx = i
	m.form = &copied
	switch fl.kind {
	case fChoice:
		// The options start after the label (2 + 16 cells), each " text ".
		x := 18
		target := -1
		for j, o := range fl.options {
			text := o
			if j < len(fl.show) && fl.show[j] != "" {
				text = fl.show[j]
			}
			w := ansi.StringWidth(text) + 2
			if bx >= x && bx < x+w {
				target = j
			}
			x += w
		}
		if target < 0 {
			return m, nil
		}
		cur := 0
		for j, o := range fl.options {
			if o == fl.value {
				cur = j
			}
		}
		step := tea.KeyMsg{Type: tea.KeyRight}
		if target < cur {
			step = tea.KeyMsg{Type: tea.KeyLeft}
		}
		var cmds []tea.Cmd
		for n := abs(target - cur); n > 0; n-- {
			var cmd tea.Cmd
			m, cmd = m.formKey(step)
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	case fLong:
		return m.formKey(tea.KeyMsg{Type: tea.KeyEnter})
	}
	return m, nil
}

// noticeFor is how long a notice stays where the hints are (0: until
// the next key; tests use that).
var noticeFor = 6 * time.Second

type noticeExpiredMsg string

// asking: a notice that waits for an answer (y deletes it…) stays.
func (m Model) asking() bool {
	return m.deleting != "" || (m.panes != nil && (m.panes.closing != "" || m.panes.closingWS != nil))
}
