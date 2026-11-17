package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── the tubaleia ────────────────────────────────────────────────────────
//
// tubas's mascot: a shark (the dorsal fin, the crescent tail) crossed
// with a whale shark (the broad head, the wide mouth, the white spots).
// Each letter is a pixel; each terminal row holds two pixel rows, drawn
// with half blocks: ▀ in the top pixel's color over the bottom one's.

var tubaleiaPixels = []string{
	"B..................B..............",
	"BB................BB..............",
	".BB..............BBB..............",
	".BBB............BBBB..............",
	"..BBB.....BBBBBBBBBBBBBBBB........",
	"..BBBB.BBBBwBBBBwBBBBBwBBBBBB.....",
	"...BBBBBBwBBBBwBBBBwBBBBBBBwBBBB..",
	"...BBBBBBBBBBwBBBBBwBBBBBwBBBBEBBB",
	"..BBBBBBLLLLLLLLLLLLLLLLLLLLLLMMMB",
	"..BBB...LLLLLLLLLLLLLLLLLLLLLLLLL.",
	".BBB.......BBBB.........BBBB......",
	"BBB.........BB...........BB.......",
}

var tubaleiaColors = map[byte]lipgloss.Color{
	'B': "#346294", // body: deep sea blue
	'w': "#ecf4fa", // the whale shark's spots
	'L': "#c4deec", // belly
	'E': "#141820", // eye
	'M': "#182840", // the wide mouth
}

// tubaleiaWidth is how many columns the mascot takes.
var tubaleiaWidth = len(tubaleiaPixels[0])

// tubaleia draws the mascot, one string per terminal row.
func tubaleia() []string {
	var out []string
	for y := 0; y < len(tubaleiaPixels); y += 2 {
		top, bottom := tubaleiaPixels[y], tubaleiaPixels[y+1]
		var b strings.Builder
		for x := 0; x < len(top); x++ {
			ct, okT := tubaleiaColors[top[x]]
			cb, okB := tubaleiaColors[bottom[x]]
			switch {
			case okT && okB:
				b.WriteString(lipgloss.NewStyle().Foreground(ct).Background(cb).Render("▀"))
			case okT:
				b.WriteString(lipgloss.NewStyle().Foreground(ct).Render("▀"))
			case okB:
				b.WriteString(lipgloss.NewStyle().Foreground(cb).Render("▄"))
			default:
				b.WriteByte(' ')
			}
		}
		out = append(out, b.String())
	}
	return out
}

// Tubaleia is the mascot for the command line (tubas detached).
func Tubaleia() string { return strings.Join(tubaleia(), "\n") }
