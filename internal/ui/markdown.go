package ui

import "strings"

// Real agents answer in markdown. A terminal cannot show markdown, so this
// is a tiny renderer for the parts agents use most: headings, **bold**,
// `code`, bullets and ``` blocks. Anything else is shown as plain text.

// renderMarkdown turns markdown into styled lines of at most w cells.
func renderMarkdown(text string, w int) []string {
	var out []string
	inFence := false
	for _, raw := range strings.Split(strings.TrimSpace(text), "\n") {
		line := strings.TrimRight(raw, " \r")
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			inFence = !inFence
		case inFence:
			out = append(out, wrap(dimStyle.Render("  "+line), w)...)
		case trimmed == "":
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "") // collapse runs of blank lines
			}
		case strings.HasPrefix(trimmed, "#"):
			title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			out = append(out, wrap(boldStyle.Render(stripInline(title)), w)...)
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			out = append(out, wrap(dimStyle.Render("• ")+renderInline(trimmed[2:]), w)...)
		default:
			out = append(out, wrap(renderInline(line), w)...)
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// renderInline styles **bold** and drops the backticks around `code`.
func renderInline(s string) string {
	parts := strings.Split(strings.ReplaceAll(s, "`", ""), "**")
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 { // between a pair of **
			b.WriteString(boldStyle.Render(p))
		} else {
			b.WriteString(textStyle.Render(p))
		}
	}
	return b.String()
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
