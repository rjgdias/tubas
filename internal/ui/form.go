package ui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// A form is how you change anything on screen: an agent, a routine, a
// skill. It is a list of fields; ↑↓ moves, ←→ picks an option, letters
// type, enter on a long text opens your editor, ctrl+s saves, esc
// cancels. Nothing is written until ctrl+s.

type fieldKind int

const (
	fText   fieldKind = iota // one line you type
	fChoice                  // one of a few options, ←→
	fLong                    // long text, edited in your editor
)

type field struct {
	key, label, hint string
	kind             fieldKind
	value            string
	options          []string // fChoice
	show             []string // fChoice: what to show for each option ("" shows the option)
}

type form struct {
	kind, title, file string // kind says what ctrl+s saves; file is where it goes
	target            string // the original name, when editing
	fields            []field
	idx               int
	err               string
	extra             any // whatever the kind needs to carry (a skill suggestion, …)
}

func (f *form) get(key string) string {
	for _, fl := range f.fields {
		if fl.key == key {
			return strings.TrimSpace(fl.value)
		}
	}
	return ""
}

func (f *form) set(key, value string) {
	for i := range f.fields {
		if f.fields[i].key == key {
			f.fields[i].value = value
		}
	}
}

func (f *form) has(key string) bool {
	for _, fl := range f.fields {
		if fl.key == key {
			return true
		}
	}
	return false
}

// editorDoneMsg arrives when your editor closes.
type editorDoneMsg struct {
	key, path string
	err       error
}

// formKey handles a key while a form is open.
func (m Model) formKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	f := *m.form
	f.fields = slices.Clone(f.fields)
	fl := &f.fields[f.idx]
	f.err = ""
	switch key := msg.String(); key {
	case "esc":
		m.form = nil
		m.notice = dimStyle.Render("nothing saved")
		return m, nil
	case "ctrl+s":
		m.form = &f
		return m.saveForm()
	case "up", "shift+tab":
		f.idx = max(0, f.idx-1)
	case "down", "tab":
		f.idx = min(len(f.fields)-1, f.idx+1)
	case "left", "right":
		if fl.kind == fChoice && len(fl.options) > 0 {
			i := max(0, slices.Index(fl.options, fl.value))
			if key == "left" {
				i = (i + len(fl.options) - 1) % len(fl.options)
			} else {
				i = (i + 1) % len(fl.options)
			}
			fl.value = fl.options[i]
			m.form = &f
			return m.formChanged(fl.key), nil
		}
	case "enter", "ctrl+e":
		if f.kind == "pane" { // a menu: enter opens it
			m.form = &f
			return m.saveForm()
		}
		if fl.kind == fLong {
			m.form = &f
			return m, openEditor(fl.key, fl.value)
		}
		f.idx = min(len(f.fields)-1, f.idx+1)
	case "backspace":
		if fl.kind == fText && fl.value != "" {
			r := []rune(fl.value)
			fl.value = string(r[:len(r)-1])
		}
	case "ctrl+u":
		if fl.kind == fText {
			fl.value = ""
		}
	default:
		if fl.kind == fText && (msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace) {
			if msg.Type == tea.KeySpace {
				fl.value += " "
			} else {
				fl.value += typedText(msg)
			}
		}
	}
	m.form = &f
	return m, nil
}

// openEditor writes the text to a temporary file and opens your editor
// on it. Bubble Tea gives the terminal to the editor until it closes.
func openEditor(key, text string) tea.Cmd {
	tmp, err := os.CreateTemp("", "tubas-*.md")
	if err != nil {
		return func() tea.Msg { return editorDoneMsg{key: key, err: err} }
	}
	tmp.WriteString(text)
	tmp.Close()
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
		if runtime.GOOS == "windows" {
			editor = "notepad"
		}
	}
	parts := strings.Fields(editor)
	cmd := exec.Command(parts[0], append(parts[1:], tmp.Name())...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editorDoneMsg{key, tmp.Name(), err} })
}

func (m Model) editorDone(msg editorDoneMsg) Model {
	if msg.path != "" {
		defer os.Remove(msg.path)
	}
	if m.form == nil {
		return m
	}
	f := *m.form
	f.fields = slices.Clone(f.fields)
	if msg.err != nil {
		f.err = "editor: " + msg.err.Error()
	} else if data, err := os.ReadFile(msg.path); err == nil {
		f.set(msg.key, strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")))
	}
	m.form = &f
	return m
}

// viewForm draws the open form in h lines.
func (m Model) viewForm(cw, h int) []string {
	f := m.form
	lines := []string{boldStyle.Render(f.title), ""}
	for i, fl := range f.fields {
		sel := i == f.idx
		label := dimStyle.Render(fmt.Sprintf("  %-16s", fl.label))
		if sel {
			label = hiStyle.Render("› ") + boldStyle.Render(fmt.Sprintf("%-16s", fl.label))
		}
		var value string
		switch fl.kind {
		case fText:
			value = textStyle.Render(fl.value)
			if sel {
				value += barStyle.Render(" ")
			}
		case fChoice:
			parts := make([]string, len(fl.options))
			for j, o := range fl.options {
				text := o
				if j < len(fl.show) && fl.show[j] != "" {
					text = fl.show[j]
				}
				if o == fl.value {
					parts[j] = barStyle.Render(" " + text + " ")
				} else {
					parts[j] = dimStyle.Render(" " + text + " ")
				}
			}
			value = strings.Join(parts, "")
		case fLong:
			first, _, _ := strings.Cut(fl.value, "\n")
			n := strings.Count(fl.value, "\n") + 1
			value = textStyle.Render(first) + dimStyle.Render(fmt.Sprintf("  · %d lines · enter edits", n))
		}
		hint := ""
		if sel && fl.hint != "" {
			hint = dimStyle.Render(fl.hint)
		}
		lines = append(lines, spread(fit(label+value, cw-lipgloss.Width(hint)-2), hint, cw))
	}
	if f.err != "" {
		lines = append(lines, "", badStyle.Render(f.err))
	}
	if f.kind == "agent" || f.kind == "agent-new" {
		lines = append(lines, "", dimStyle.Render("  instructions"))
		for _, l := range strings.Split(f.get("instructions"), "\n") {
			for _, w := range wrap(l, cw-4) {
				lines = append(lines, "  "+faintStyle.Render(w))
			}
		}
	}
	start := 0
	if row := f.idx + 2; row >= h-1 { // keep the selected field in view
		start = row - h + 2
	}
	if start > 0 && start < len(lines) {
		lines = append(lines[:2:2], lines[2+start:]...)
	}
	return fitHeight(lines, h, false)
}

func (m Model) formBottom(cw int) []string {
	keys := "↑↓ field · ←→ choose · type to edit · enter on a long text opens your editor · ctrl+s save · esc cancel"
	return []string{"", faintStyle.Render(strings.Repeat("─", cw)), spread(dimStyle.Render(fit(keys, cw-lipgloss.Width(m.form.file)-2)), faintStyle.Render(m.form.file), cw)}
}

// splitList reads "a, b c" as [a b c].
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}
