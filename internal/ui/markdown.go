package ui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Agents answer in markdown. A terminal cannot show markdown, so this is
// a small renderer for what answers use: headings, **bold**, *italic*,
// `code`, bullets, numbered lists, > quotes, --- rules, ``` blocks and
// tables. Anything else is shown as plain text.

var (
	italicStyle = textStyle.Italic(true)
	codeStyle   = hiStyle
	tableSep    = regexp.MustCompile(`^:?-{2,}:?$`)
	numbered    = regexp.MustCompile(`^(\d+)[.)] `)
)

// renderMarkdown turns markdown into styled lines of at most w cells.
func renderMarkdown(text string, w int) []string {
	var out []string
	inFence := false
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \r")
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			inFence = !inFence
		case inFence: // code: a bar on the left sets it apart from the text
			for _, l := range wrap(codeStyle.Render(line), max(10, w-2)) {
				out = append(out, faintStyle.Render("▏ ")+l) // the bar on wrapped lines too
			}
		case trimmed == "":
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "") // collapse runs of blank lines
			}
		case strings.HasPrefix(trimmed, "|"):
			j := i
			for j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "|") {
				j++
			}
			out = append(out, renderTable(lines[i:j], w)...)
			i = j - 1
		case strings.HasPrefix(trimmed, "#"):
			title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			out = append(out, wrap(boldStyle.Render(stripInline(title)), w)...)
		case trimmed == "---" || trimmed == "***" || trimmed == "___":
			out = append(out, faintStyle.Render(strings.Repeat("─", min(w, 40))))
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ "):
			indent := strings.Repeat(" ", min(8, len(line)-len(strings.TrimLeft(line, " "))))
			out = append(out, hang(indent+dimStyle.Render("• "), renderInline(trimmed[2:]), w)...)
		case numbered.MatchString(trimmed):
			n := numbered.FindString(trimmed)
			out = append(out, hang(dimStyle.Render(n), renderInline(trimmed[len(n):]), w)...)
		case strings.HasPrefix(trimmed, ">"):
			out = append(out, hang(faintStyle.Render("│ "), italicStyle.Render(stripInline(strings.TrimSpace(strings.TrimPrefix(trimmed, ">")))), w)...)
		default:
			out = append(out, wrap(renderInline(line), w)...)
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// hang wraps text after a prefix (a bullet, a number), with the next
// lines indented under the text.
func hang(prefix, text string, w int) []string {
	pw := ansi.StringWidth(prefix)
	lines := wrap(text, max(10, w-pw))
	for i := range lines {
		if i == 0 {
			lines[i] = prefix + lines[i]
		} else {
			lines[i] = strings.Repeat(" ", pw) + lines[i]
		}
	}
	return lines
}

// renderInline styles **bold**, *italic* (or _italic_) and `code`.
func renderInline(s string) string {
	var b strings.Builder
	r := []rune(s)
	plain := func(t string) {
		if t != "" {
			b.WriteString(textStyle.Render(t))
		}
	}
	start := 0
	for i := 0; i < len(r); i++ {
		closing := func(marker string, from int) int {
			rest := string(r[from:])
			if k := strings.Index(rest, marker); k > 0 {
				return from + len([]rune(rest[:k]))
			}
			return -1
		}
		switch {
		case r[i] == '`':
			if end := closing("`", i+1); end > 0 {
				plain(string(r[start:i]))
				b.WriteString(codeStyle.Render(string(r[i+1 : end])))
				i, start = end, end+1
			}
		case r[i] == '*' && i+1 < len(r) && r[i+1] == '*':
			if end := closing("**", i+2); end > 0 {
				plain(string(r[start:i]))
				b.WriteString(boldStyle.Render(string(r[i+2 : end])))
				i, start = end+1, end+2
			}
		case (r[i] == '*' || r[i] == '_') && i+1 < len(r) && r[i+1] != ' ' && (i == 0 || r[i-1] == ' ' || r[i-1] == '('):
			if end := closing(string(r[i]), i+1); end > 0 && r[end-1] != ' ' {
				plain(string(r[start:i]))
				b.WriteString(italicStyle.Render(string(r[i+1 : end])))
				i, start = end, end+1
			}
		}
	}
	plain(string(r[min(start, len(r)):]))
	return b.String()
}

// renderTable lays a markdown table out in aligned columns that fit w.
func renderTable(rows []string, w int) []string {
	var cells [][]string
	header := -1
	for _, row := range rows {
		row = strings.TrimSpace(row)
		row = strings.TrimSuffix(strings.TrimPrefix(row, "|"), "|")
		parts := strings.Split(row, "|")
		sep := true
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
			sep = sep && tableSep.MatchString(parts[i])
		}
		if sep {
			header = len(cells) - 1
			continue
		}
		cells = append(cells, parts)
	}
	cols := 0
	for _, c := range cells {
		cols = max(cols, len(c))
	}
	if cols == 0 {
		return nil
	}
	widths := make([]int, cols)
	for _, c := range cells {
		for j, cell := range c {
			widths[j] = max(widths[j], lipgloss.Width(stripInline(cell)))
		}
	}
	// Too wide: the widest columns give room until it fits.
	gap := 2
	for total := sum(widths) + gap*(cols-1); total > w; total = sum(widths) + gap*(cols-1) {
		widest := 0
		for j := range widths {
			if widths[j] > widths[widest] {
				widest = j
			}
		}
		if widths[widest] <= 8 {
			break
		}
		widths[widest]--
	}
	// A cell longer than its column wraps inside it: nothing is cut. When
	// any row takes more than one line, a blank line keeps rows apart.
	grid := make([][][]string, len(cells))
	loose := false
	for i, c := range cells {
		grid[i] = make([][]string, cols)
		for j := 0; j < cols; j++ {
			cell := ""
			if j < len(c) {
				cell = stripInline(c[j])
			}
			grid[i][j] = strings.Split(ansi.Wrap(cell, widths[j], ""), "\n")
			loose = loose || len(grid[i][j]) > 1
		}
	}
	var out []string
	for i, row := range grid {
		height := 1
		for _, cell := range row {
			height = max(height, len(cell))
		}
		for k := 0; k < height; k++ {
			var b strings.Builder
			for j, cell := range row {
				text := ""
				if k < len(cell) {
					text = strings.TrimRight(cell[k], " ")
				}
				text = padRight(text, widths[j])
				if i == header {
					b.WriteString(boldStyle.Render(text))
				} else {
					b.WriteString(textStyle.Render(text))
				}
				if j < cols-1 {
					b.WriteString(strings.Repeat(" ", gap))
				}
			}
			out = append(out, strings.TrimRight(b.String(), " "))
		}
		switch {
		case i == header:
			out = append(out, faintStyle.Render(strings.Repeat("─", min(w, sum(widths)+gap*(cols-1)))))
		case loose && i < len(grid)-1:
			out = append(out, "")
		}
	}
	return out
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

func stripInline(s string) string {
	return strings.NewReplacer("**", "", "`", "", "__", "").Replace(s)
}

// plainSummary squeezes an answer into one plain line for collapsed
// turns: "## Conclusão" + "A queda..." becomes "Conclusão: A queda...".
func plainSummary(text string) string {
	var lines []string
	inFence := false
	for _, raw := range strings.Split(text, "\n") {
		t := strings.TrimSpace(raw)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence || t == "" {
			continue
		}
		heading := strings.HasPrefix(t, "#")
		t = strings.TrimSpace(strings.TrimLeft(t, "#"))
		t = strings.TrimPrefix(strings.TrimPrefix(t, "- "), "* ")
		t = stripInline(t)
		// A heading, or a short intro ending in ":", says little alone:
		// join it with the line that follows.
		intro := heading || (strings.HasSuffix(t, ":") && len([]rune(t)) < 60)
		if heading {
			t = strings.TrimSuffix(t, ":") + ":"
		}
		lines = append(lines, t)
		if !intro || len(lines) == 2 {
			break
		}
	}
	return strings.Join(lines, " ")
}
