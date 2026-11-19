package ui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// A long text (an agent's instructions, a routine's prompt, a skill's
// steps) is edited right here, in a box inside the form: arrows move,
// letters type, enter breaks the line, esc goes back to the fields and
// ctrl+s saves. Your own editor is one key away (ctrl+e), never a
// surprise: a window opening somewhere else, with tubas waiting for it,
// looks like tubas froze.

type textEdit struct {
	key  string
	text []rune
	cur  int // the cursor: an index into text
	top  int // the first row shown
	goal int // the column ↑↓ keep to (-1: none yet)

	undo, redo []editState // ctrl+z, ctrl+y
	last       string      // the kind of the last change: a run of typing is one undo
}

// editState is the text and the cursor, for undo.
type editState struct {
	text []rune
	cur  int
}

// maxUndo is how many steps back ctrl+z goes.
const maxUndo = 300

// remember saves the text as it was before a change, for ctrl+z. A run of
// typing is one step, cut where a new word starts; a run of deletes is
// one step; anything else (a paste, your editor's text) is a step of its
// own.
func (e *textEdit) remember(before editState, kind string) {
	newWord := kind == "type" && before.cur > 0 && unicode.IsSpace(before.text[before.cur-1]) &&
		e.cur > before.cur && !unicode.IsSpace(e.text[before.cur])
	if kind != e.last || kind == "other" || newWord {
		e.undo = append(slices.Clip(e.undo), before)
		if len(e.undo) > maxUndo {
			e.undo = e.undo[len(e.undo)-maxUndo:]
		}
	}
	e.redo, e.last = nil, kind
}

// step moves the text one state back (from undo) or forward (from redo).
func (e *textEdit) step(from, to *[]editState) {
	if len(*from) == 0 {
		return
	}
	*to = append(slices.Clip(*to), editState{e.text, e.cur})
	s := (*from)[len(*from)-1]
	*from = slices.Clip((*from)[:len(*from)-1])
	e.text, e.cur, e.last = slices.Clone(s.text), min(s.cur, len(s.text)), ""
}

// editRow is one row on screen: text[start:end]. last: the row ends its
// line (a newline or the end of the text comes next), so the cursor can
// sit at end.
type editRow struct {
	start, end int
	last       bool
}

// editRows wraps the text at w cells, between words when it can.
func editRows(text []rune, w int) []editRow {
	w = max(1, w)
	var rows []editRow
	lineStart := 0
	for i := 0; i <= len(text); i++ {
		if i < len(text) && text[i] != '\n' {
			continue
		}
		s := lineStart
		for {
			e, width, space := s, 0, -1
			for e < i {
				cw := ansi.StringWidth(string(text[e]))
				if width+cw > w {
					break
				}
				width += cw
				if text[e] == ' ' {
					space = e
				}
				e++
			}
			if e >= i {
				rows = append(rows, editRow{s, i, true})
				break
			}
			switch {
			case space >= s:
				e = space + 1 // the space stays at the end of the row
			case e == s:
				e = s + 1 // a character wider than the box
			}
			rows = append(rows, editRow{s, e, false})
			s = e
		}
		lineStart = i + 1
	}
	return rows
}

// rowOf is the row the cursor is on.
func rowOf(rows []editRow, cur int) int {
	for i, r := range rows {
		if cur >= r.start && (cur < r.end || (cur == r.end && r.last)) {
			return i
		}
	}
	return len(rows) - 1
}

// colOf is how many cells come before the cursor on its row.
func colOf(text []rune, r editRow, cur int) int {
	return ansi.StringWidth(string(text[r.start:min(cur, r.end)]))
}

// indexAt is where column col of row r is.
func indexAt(text []rune, r editRow, col int) int {
	last := r.end
	if !r.last {
		last = r.end - 1 // the next row starts at end
	}
	i, width := r.start, 0
	for i < last {
		cw := ansi.StringWidth(string(text[i]))
		if width+cw > col {
			break
		}
		width += cw
		i++
	}
	return i
}

// editorText is what a key or a paste typed: a pasted text keeps its
// line breaks, a tab becomes spaces, other control characters go.
func editorText(msg tea.KeyMsg) string {
	s := strings.ReplaceAll(string(msg.Runes), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "  ")
	return strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// editSize is the box's width and height for this window.
func (m Model) editSize() (w, h int) {
	cw := m.width - 2
	return max(1, cw-4), max(1, m.height-4-len(m.formBottom(cw))-3)
}

// startEdit opens the box on field i.
func (f *form) startEdit(i int) {
	fl := f.fields[i]
	f.idx = i
	f.edit = &textEdit{key: fl.key, text: []rune(fl.value), goal: -1}
}

// editKey handles a key while the box is open.
func (m Model) editKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	f := *m.form
	f.fields = slices.Clone(f.fields)
	e := *f.edit
	before := editState{f.edit.text, f.edit.cur} // e.text is a copy: this one stays as it was
	e.text = slices.Clone(e.text)
	w, h := m.editSize()
	kind := "other" // what the change was, for undo
	rows := editRows(e.text, w)
	row := rowOf(rows, e.cur)
	moved := false // ↑↓ keep their column; anything else forgets it
	insert := func(s string) {
		r := []rune(s)
		e.text = slices.Insert(e.text, e.cur, r...)
		e.cur += len(r)
	}
	switch key := msg.String(); key {
	case "esc":
		f.edit = nil
		m.form = &f
		return m, nil
	case "ctrl+s":
		f.edit = nil
		m.form = &f
		return m.saveForm()
	case "ctrl+e":
		m.form = &f
		return m, openEditor(e.key, string(e.text))
	case "ctrl+z":
		e.step(&e.undo, &e.redo)
		kind = ""
	case "ctrl+y", "ctrl+shift+z":
		e.step(&e.redo, &e.undo)
		kind = ""
	case "enter":
		insert("\n")
		kind = "type"
	case "tab":
		insert("  ")
		kind = "type"
	case "backspace":
		if e.cur > 0 {
			e.text = slices.Delete(e.text, e.cur-1, e.cur)
			e.cur--
		}
		kind = "delete"
	case "delete":
		if e.cur < len(e.text) {
			e.text = slices.Delete(e.text, e.cur, e.cur+1)
		}
		kind = "delete"
	case "left":
		e.cur = max(0, e.cur-1)
	case "right":
		e.cur = min(len(e.text), e.cur+1)
	case "ctrl+left", "alt+left":
		e.cur = prevWord(e.text, e.cur)
	case "ctrl+right", "alt+right":
		e.cur = nextWord(e.text, e.cur)
	case "home":
		e.cur = rows[row].start
	case "end":
		e.cur = indexAt(e.text, rows[row], 1<<30)
	case "ctrl+home":
		e.cur = 0
	case "ctrl+end":
		e.cur = len(e.text)
	case "up", "down", "pgup", "pgdown":
		if e.goal < 0 {
			e.goal = colOf(e.text, rows[row], e.cur)
		}
		step := map[string]int{"up": -1, "down": 1, "pgup": -(h - 1), "pgdown": h - 1}[key]
		switch to := row + step; {
		case to < 0:
			e.cur = 0
		case to >= len(rows):
			e.cur = len(e.text)
		default:
			e.cur = indexAt(e.text, rows[to], e.goal)
		}
		moved = true
	default:
		if msg.Type == tea.KeySpace {
			insert(" ")
			kind = "type"
		} else if msg.Type == tea.KeyRunes {
			insert(editorText(msg))
			if !msg.Paste {
				kind = "type"
			}
		}
	}
	switch {
	case kind == "": // ctrl+z, ctrl+y
	case !slices.Equal(before.text, e.text):
		e.remember(before, kind)
	case e.cur != before.cur:
		e.last = "" // moving the cursor ends a run of typing
	}
	if !moved {
		e.goal = -1
	}
	e.scroll(w, h)
	f.edit = &e
	f.set(e.key, string(e.text))
	m.form = &f
	return m, nil
}

// scroll keeps the cursor's row in the box.
func (e *textEdit) scroll(w, h int) {
	rows := editRows(e.text, w)
	row := rowOf(rows, e.cur)
	e.top = min(e.top, max(0, len(rows)-h))
	if row < e.top {
		e.top = row
	}
	if row >= e.top+h {
		e.top = row - h + 1
	}
	e.top = max(0, e.top)
}

func prevWord(text []rune, i int) int {
	for i > 0 && !isWord(text[i-1]) {
		i--
	}
	for i > 0 && isWord(text[i-1]) {
		i--
	}
	return i
}

func nextWord(text []rune, i int) int {
	for i < len(text) && !isWord(text[i]) {
		i++
	}
	for i < len(text) && isWord(text[i]) {
		i++
	}
	return i
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// mouseEdit: a click puts the cursor where you clicked, the wheel
// scrolls.
func (m Model) mouseEdit(msg tea.MouseMsg, bx, by int) (Model, tea.Cmd) {
	f := *m.form
	e := *f.edit
	w, h := m.editSize()
	rows := editRows(e.text, w)
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		e.top = max(0, e.top-3)
	case tea.MouseButtonWheelDown:
		e.top = max(0, min(len(rows)-h, e.top+3))
	default:
		row := by - 3 + e.top
		if by < 3 || row < 0 {
			return m.clickHint(msg) // "esc done · ctrl+s save"
		}
		if row >= len(rows) {
			e.cur = len(e.text)
		} else {
			e.cur = indexAt(e.text, rows[row], max(0, bx-2))
		}
		e.goal = -1
		e.scroll(w, h)
	}
	f.edit = &e
	m.form = &f
	return m, nil
}

// viewEdit draws the box: the title, what is being edited, the text with
// its cursor.
func (m Model) viewEdit(cw, h int) []string {
	f := m.form
	e := f.edit
	label := e.key
	for _, fl := range f.fields {
		if fl.key == e.key {
			label = fl.label
		}
	}
	w, boxH := m.editSize()
	rows := editRows(e.text, w)
	row := rowOf(rows, e.cur)
	where := fmt.Sprintf("line %d of %d", strings.Count(string(e.text[:e.cur]), "\n")+1, strings.Count(string(e.text), "\n")+1)
	under := ""
	if f.err != "" {
		under = badStyle.Render("  " + fit(f.err, cw-4))
	}
	lines := []string{boldStyle.Render(f.title), spread(hiStyle.Render("  "+label), dimStyle.Render(where), cw), under}
	for i := e.top; i < len(rows) && i < e.top+boxH; i++ {
		r := rows[i]
		text := string(e.text[r.start:r.end])
		if i != row {
			lines = append(lines, "  "+textStyle.Render(text))
			continue
		}
		c := e.cur - r.start
		before, after := string(e.text[r.start:r.start+c]), []rune(text)[c:]
		under, rest := " ", ""
		if len(after) > 0 {
			under, rest = string(after[0]), string(after[1:])
		}
		lines = append(lines, "  "+textStyle.Render(before)+barStyle.Render(under)+textStyle.Render(rest))
	}
	if len(e.text) == 0 && e.key == "instructions" {
		lines = append(lines, "", faintStyle.Render("  type here: what this agent does, how, and what it never does"))
	}
	return fitHeight(lines, h, false)
}

// openEditor writes the text to a temporary file and opens your editor
// on it ($VISUAL, $EDITOR, else vi, or Notepad on Windows). Bubble Tea
// gives the terminal to the editor until it closes; the terminal says
// so, since a window editor opens elsewhere.
func openEditor(key, text string) tea.Cmd {
	tmp, err := os.CreateTemp("", "tubas-*.md")
	if err != nil {
		return func() tea.Msg { return editorDoneMsg{key: key, err: err} }
	}
	tmp.WriteString(text)
	tmp.Close()
	parts := strings.Fields(editorCommand())
	cmd := exec.Command(parts[0], append(parts[1:], tmp.Name())...)
	run := &editorRun{cmd: cmd, note: fmt.Sprintf("\n  tubas is waiting for %s: save, close it, and you are back.\n", filepath.Base(parts[0]))}
	return tea.Exec(run, func(err error) tea.Msg { return editorDoneMsg{key, tmp.Name(), err} })
}

func editorCommand() string {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if s := strings.TrimSpace(os.Getenv(v)); s != "" {
			return s
		}
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "vi"
}

// editorRun runs the editor after a line saying tubas waits for it.
type editorRun struct {
	cmd  *exec.Cmd
	note string
}

func (r *editorRun) Run() error {
	if r.cmd.Stdout != nil {
		io.WriteString(r.cmd.Stdout, r.note)
	}
	return r.cmd.Run()
}
func (r *editorRun) SetStdin(in io.Reader)   { r.cmd.Stdin = in }
func (r *editorRun) SetStdout(out io.Writer) { r.cmd.Stdout = out }
func (r *editorRun) SetStderr(out io.Writer) { r.cmd.Stderr = out }
