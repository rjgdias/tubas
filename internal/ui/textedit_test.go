package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTheBoxWrapsBetweenWords(t *testing.T) {
	text := []rune("one two three\nfour")
	rows := editRows(text, 8)
	var got []string
	for _, r := range rows {
		got = append(got, string(text[r.start:r.end]))
	}
	if strings.Join(got, "|") != "one two |three|four" {
		t.Fatalf("rows: %q", got)
	}
	// The cursor at the end of a wrapped row is at the start of the next.
	if rowOf(rows, 8) != 1 || rowOf(rows, 13) != 1 || rowOf(rows, 14) != 2 {
		t.Errorf("rowOf: %d %d %d", rowOf(rows, 8), rowOf(rows, 13), rowOf(rows, 14))
	}
	if r := editRows([]rune(""), 10); len(r) != 1 {
		t.Errorf("an empty text has one row: %v", r)
	}
}

// The bug: a click on "instructions" opened Notepad and tubas waited for
// it, looking frozen. Now the click opens a box right in the form.
func TestAUserEditsInstructionsInPlace(t *testing.T) {
	m, _ := personalModel(t)
	m = click(t, m, "3 agents")
	m = click(t, m, "tubas  assistant")
	if m.form == nil {
		t.Fatal("the agent's form opens")
	}
	m = click(t, m, "instructions")
	if m.form.edit == nil || m.form.edit.key != "instructions" {
		t.Fatal("a click on instructions opens the box, no outside editor")
	}
	onScreen(t, m, "esc done", "ctrl+s save", "line 1 of")

	// Typing goes in at the cursor (the start); enter breaks the line.
	m = m.PressKeys("ctrl+home", "Be", "space", "brief.", "enter")
	if got := m.form.get("instructions"); !strings.HasPrefix(got, "Be brief.\n") {
		t.Fatalf("typed at the start: %q", got)
	}
	// Arrows and backspace.
	m = m.PressKeys("up", "end", "backspace", "!")
	if got := m.form.get("instructions"); !strings.HasPrefix(got, "Be brief!\n") {
		t.Fatalf("end and backspace: %q", got)
	}
	// A paste keeps its lines.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\r\nb"), Paste: true})
	m = next.(Model)
	if got := m.form.get("instructions"); !strings.HasPrefix(got, "Be brief!a\nb\n") {
		t.Fatalf("a paste: %q", got)
	}
	// A click puts the cursor where it lands.
	m = click(t, m, "Be brief")
	if m.form.edit.cur != 4 {
		t.Errorf("the click puts the cursor on its letter: %d", m.form.edit.cur)
	}

	// esc goes back to the fields, keeping the text; ctrl+s saves.
	m = m.PressKeys("esc")
	if m.form == nil || m.form.edit != nil {
		t.Fatal("esc leaves the box, not the form")
	}
	onScreen(t, m, "Be brief!a")
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = settle(next.(Model), cmd)
	if m.form != nil {
		t.Fatalf("ctrl+s saves and closes: %s", m.form.err)
	}
	data, err := os.ReadFile(filepath.Join(m.cfg.TeamDir, "agents", "tubas.md"))
	if err != nil || !strings.Contains(string(data), "Be brief!a\nb\n") {
		t.Fatalf("the file has the new instructions: %v\n%s", err, data)
	}
}

// ctrl+e: what your editor wrote shows in the box, and stays after the
// next key.
func TestTheBoxShowsWhatYourEditorWrote(t *testing.T) {
	m, _ := personalModel(t)
	m = click(t, m, "3 agents")
	m = click(t, m, "tubas  assistant")
	m = click(t, m, "instructions")
	path := filepath.Join(t.TempDir(), "edit.md")
	os.WriteFile(path, []byte("from the editor\r\n"), 0o644)
	m = m.editorDone(editorDoneMsg{key: "instructions", path: path})
	m = m.PressKeys("right")
	if got := string(m.form.edit.text); got != "from the editor" || m.form.get("instructions") != got {
		t.Fatalf("the box has the editor's text: %q / %q", got, m.form.get("instructions"))
	}
}

func TestTheBoxScrollsToTheCursor(t *testing.T) {
	m, _ := personalModel(t)
	m = click(t, m, "3 agents")
	m = click(t, m, "tubas  assistant")
	m = click(t, m, "instructions")
	long := strings.Repeat("line\n", 80) + "the end"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(long), Paste: true})
	m = next.(Model)
	onScreen(t, m, "the end")
	m = m.PressKeys("ctrl+home")
	if m.form.edit.top != 0 {
		t.Errorf("ctrl+home scrolls to the top: %d", m.form.edit.top)
	}
	next, _ = m.Update(tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if next.(Model).form.edit.top != 3 {
		t.Errorf("the wheel scrolls: %d", next.(Model).form.edit.top)
	}
}
