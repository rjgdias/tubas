package ui

import (
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tubas/internal/pane"
)

// ── scrollback and copy mode ────────────────────────────────────────────
//
// ctrl+b [ (or the mouse wheel) opens what a pane printed, 5000 lines
// back. Move with h j k l, w b, 0 $, g G, pgup pgdown; / searches back
// (n again, N forward); v starts a selection, y (or enter) copies it, or
// the line under the cursor, to the clipboard; q goes back to the live
// terminal. Dragging the mouse over a pane copies too (mouse.go).

type copyState struct {
	id           string
	lines, plain []string
	top          int // the first line on screen
	cy, cx       int // the cursor: line, column
	anchor       *[2]int
	searching    bool
	query        string
	scroll       int // wheel ticks that came before the history did
}

type copyLoadedMsg struct {
	id           string
	lines, plain []string
	err          error
	scroll       int
}

func (m Model) enterCopy(id string, scroll int) (Model, tea.Cmd) {
	host := m.panes.host
	return m, func() tea.Msg {
		lines, plain, err := host.History(id)
		return copyLoadedMsg{id, lines, plain, err, scroll}
	}
}

func (m Model) copyLoaded(msg copyLoadedMsg) Model {
	ps := m.panes
	if msg.err != nil {
		m.notice = badStyle.Render("history: " + msg.err.Error())
		return m
	}
	if ps.focus() != msg.id || len(msg.plain) == 0 {
		return m
	}
	c := &copyState{id: msg.id, lines: msg.lines, plain: msg.plain}
	c.cy = len(c.plain) - 1
	h := m.copyRows()
	c.top = max(0, len(c.plain)-h)
	ps.copy = c
	if msg.scroll != 0 {
		m.copyScroll(msg.scroll)
	}
	return m
}

// copyRows is how many lines of history fit in the pane.
func (m Model) copyRows() int {
	if c := m.panes.copy; c != nil {
		if r, ok := m.layoutRects()[c.id]; ok {
			return max(1, r.h-1)
		}
	}
	if r, ok := m.layoutRects()[m.panes.focus()]; ok {
		return max(1, r.h-1)
	}
	return 10
}

// copyScroll moves the view by n lines (negative: back in time); the
// cursor stays on screen. Scrolling past the end goes back to live.
func (m Model) copyScroll(n int) Model {
	c := m.panes.copy
	if c == nil {
		return m
	}
	h := m.copyRows()
	bottom := max(0, len(c.plain)-h)
	if n > 0 && c.top >= bottom {
		m.panes.copy = nil // the wheel went past the end: live again
		return m
	}
	c.top = max(0, min(bottom, c.top+n))
	c.cy = max(c.top, min(c.top+h-1, c.cy))
	c.cy = min(c.cy, len(c.plain)-1)
	return m
}

func (m Model) copyKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	c := m.panes.copy
	key := msg.String()
	if c.searching {
		switch key {
		case "esc":
			c.searching, c.query = false, ""
		case "enter":
			c.searching = false
			m = m.copyFind(-1)
		case "backspace":
			if r := []rune(c.query); len(r) > 0 {
				c.query = string(r[:len(r)-1])
			}
		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				c.query += string(msg.Runes)
				if msg.Type == tea.KeySpace {
					c.query += " "
				}
			}
		}
		return m, nil
	}
	h := m.copyRows()
	line := func() []rune { return []rune(c.plain[c.cy]) }
	switch key {
	case "q", "ctrl+c":
		m.panes.copy = nil
		return m, nil
	case "esc":
		if c.anchor != nil {
			c.anchor = nil
		} else {
			m.panes.copy = nil
		}
		return m, nil
	case "h", "left":
		c.cx--
	case "l", "right":
		c.cx++
	case "k", "up":
		c.cy--
	case "j", "down":
		c.cy++
	case "0", "home":
		c.cx = 0
	case "$", "end":
		c.cx = len(line())
	case "w":
		c.cx = wordStart(line(), c.cx, +1)
	case "b":
		c.cx = wordStart(line(), c.cx, -1)
	case "e":
		c.cx = wordEnd(line(), c.cx)
	case "g":
		c.cy, c.cx = 0, 0
	case "G":
		c.cy = len(c.plain) - 1
	case "pgup", "ctrl+u":
		step := h
		if key == "ctrl+u" {
			step = h / 2
		}
		c.cy -= step
		c.top -= step
	case "pgdown", "ctrl+d":
		step := h
		if key == "ctrl+d" {
			step = h / 2
		}
		c.cy += step
		c.top += step
	case "v", " ":
		if c.anchor == nil {
			c.anchor = &[2]int{c.cy, c.cx}
		} else {
			c.anchor = nil
		}
	case "/", "?":
		c.searching, c.query = true, ""
		return m, nil
	case "n":
		return m.copyFind(-1), nil
	case "N":
		return m.copyFind(+1), nil
	case "y", "enter":
		text := c.text()
		m.panes.copy = nil
		return m, copyText(text)
	}
	c.clamp(h)
	return m, nil
}

// clamp keeps the cursor in the text and on screen.
func (c *copyState) clamp(h int) {
	c.cy = max(0, min(len(c.plain)-1, c.cy))
	c.cx = max(0, min(len([]rune(c.plain[c.cy])), c.cx))
	if c.cy < c.top {
		c.top = c.cy
	}
	if c.cy >= c.top+h {
		c.top = c.cy - h + 1
	}
	c.top = max(0, min(max(0, len(c.plain)-h), c.top))
}

// copyFind moves to the next line holding the query, back in time
// (dir -1) or forward (+1).
func (m Model) copyFind(dir int) Model {
	c := m.panes.copy
	if c.query == "" {
		return m
	}
	q := strings.ToLower(c.query)
	for i, y := 0, c.cy+dir; i < len(c.plain); i, y = i+1, y+dir {
		if y < 0 || y >= len(c.plain) {
			break
		}
		if x := strings.Index(strings.ToLower(c.plain[y]), q); x >= 0 {
			c.cy, c.cx = y, len([]rune(c.plain[y][:x]))
			c.clamp(m.copyRows())
			return m
		}
	}
	m.notice = dimStyle.Render("not found: " + c.query)
	return m
}

func wordStart(r []rune, x, dir int) int {
	isWord := func(i int) bool { return i >= 0 && i < len(r) && !unicode.IsSpace(r[i]) }
	if dir > 0 {
		for x < len(r) && isWord(x) {
			x++
		}
		for x < len(r) && !isWord(x) {
			x++
		}
		return x
	}
	x--
	for x > 0 && !isWord(x) {
		x--
	}
	for x > 0 && isWord(x-1) {
		x--
	}
	return max(0, x)
}

func wordEnd(r []rune, x int) int {
	isWord := func(i int) bool { return i >= 0 && i < len(r) && !unicode.IsSpace(r[i]) }
	x++
	for x < len(r) && !isWord(x) {
		x++
	}
	for x < len(r)-1 && isWord(x+1) {
		x++
	}
	return min(x, len(r))
}

// span is the selection from the anchor to the cursor, in reading order.
func (c *copyState) span() (y0, x0, y1, x1 int, ok bool) {
	if c.anchor == nil {
		return 0, 0, 0, 0, false
	}
	y0, x0, y1, x1 = c.anchor[0], c.anchor[1], c.cy, c.cx
	if y1 < y0 || (y1 == y0 && x1 < x0) {
		y0, x0, y1, x1 = y1, x1, y0, x0
	}
	return y0, x0, y1, x1, true
}

// text is what y copies: the selection, or the cursor's line.
func (c *copyState) text() string {
	y0, x0, y1, x1, ok := c.span()
	if !ok {
		return c.plain[c.cy]
	}
	return cutText(c.plain, y0, x0, y1, x1+1)
}

// cutText takes the text between two positions (the end column is
// excluded) out of plain lines.
func cutText(plain []string, y0, x0, y1, x1 int) string {
	var parts []string
	for y := y0; y <= y1 && y < len(plain); y++ {
		r := []rune(plain[y])
		from, to := 0, len(r)
		if y == y0 {
			from = min(x0, len(r))
		}
		if y == y1 {
			to = min(x1, len(r))
		}
		if from < to {
			parts = append(parts, string(r[from:to]))
		} else {
			parts = append(parts, "")
		}
	}
	return strings.Join(parts, "\n")
}

// copyView draws the history with the selection and the cursor.
func (m Model) copyView(w, h int) []string {
	c := m.panes.copy
	y0, x0, y1, x1, sel := c.span()
	out := make([]string, 0, h)
	for y := c.top; y < c.top+h && y < len(c.plain); y++ {
		inSel := sel && y >= y0 && y <= y1
		if !inSel && y != c.cy {
			out = append(out, c.lines[y])
			continue
		}
		r := []rune(c.plain[y])
		for len(r) <= max(c.cx, 0) || len(r) < w {
			r = append(r, ' ')
			if len(r) >= w && len(r) > c.cx {
				break
			}
		}
		var b strings.Builder
		for x, ch := range r {
			s := string(ch)
			selected := inSel && (y > y0 || x >= x0) && (y < y1 || x <= x1)
			switch {
			case y == c.cy && x == c.cx:
				b.WriteString(ansi.SGR(ansi.ReverseAttr) + s + ansi.SGR(ansi.ResetAttr))
			case selected:
				b.WriteString(barStyle.Render(s))
			default:
				b.WriteString(textStyle.Render(s))
			}
		}
		out = append(out, b.String())
	}
	return out
}

// ── selecting with the mouse ────────────────────────────────────────────

// selection is text being dragged over on a live pane, in its cells.
type selection struct {
	id             string
	x0, y0, x1, y1 int
	moved          bool
}

func (s *selection) ordered() (y0, x0, y1, x1 int) {
	y0, x0, y1, x1 = s.y0, s.x0, s.y1, s.x1
	if y1 < y0 || (y1 == y0 && x1 < x0) {
		y0, x0, y1, x1 = y1, x1, y0, x0
	}
	return
}

// draw paints the selected cells over a pane's screen.
func (s *selection) draw(lines []string) []string {
	y0, x0, y1, x1 := s.ordered()
	out := append([]string(nil), lines...)
	for y := y0; y <= y1 && y < len(out); y++ {
		r := []rune(ansi.Strip(out[y]))
		var b strings.Builder
		for x, ch := range r {
			if (y > y0 || x >= x0) && (y < y1 || x <= x1) {
				b.WriteString(barStyle.Render(string(ch)))
			} else {
				b.WriteString(textStyle.Render(string(ch)))
			}
		}
		out[y] = b.String()
	}
	return out
}

// text is the selected text, from the screen's plain text.
func (s *selection) text(lines []string) string {
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	y0, x0, y1, x1 := s.ordered()
	return cutText(plain, y0, x0, y1, x1+1)
}

func copiedNotice(text string) string {
	return okStyle.Render(fmt.Sprintf("✓ copied %d characters", len([]rune(text))))
}

var _ = pane.Done
