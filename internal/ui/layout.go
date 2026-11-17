package ui

import (
	"encoding/json"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// ── layout: how panes share the screen ──────────────────────────────────
//
// The chat area is a tree, like tmux: a leaf is one pane (the tubas chat
// or a terminal), a split shares its space between two children, side by
// side ("right") or one above the other ("down"). Splitting a pane turns
// its leaf into a split holding the old pane and the new one.

const chatPane = "chat"

type layoutNode struct {
	Pane  string      `json:"pane,omitempty"`
	Split string      `json:"split,omitempty"` // "right": A | B, "down": A over B
	Ratio float64     `json:"ratio,omitempty"` // A's share, 0 for half (dragging the border changes it)
	A     *layoutNode `json:"a,omitempty"`
	B     *layoutNode `json:"b,omitempty"`
}

// separator is the border between a split's two children, where the
// mouse can drag it.
type separator struct {
	node   *layoutNode
	parent rect // the split's whole area
	line   rect // the border's cells
}

type rect struct{ x, y, w, h int }

// leaves are the panes, left to right and top to bottom.
func (n *layoutNode) leaves() []string {
	if n == nil {
		return nil
	}
	if n.Pane != "" {
		return []string{n.Pane}
	}
	return append(n.A.leaves(), n.B.leaves()...)
}

func (n *layoutNode) has(id string) bool {
	for _, l := range n.leaves() {
		if l == id {
			return true
		}
	}
	return false
}

// splitPane puts id next to target. With no layout yet, the chat is the
// only pane, so the split is made next to it.
func splitPane(n *layoutNode, target, dir, id string) *layoutNode {
	if n == nil {
		return &layoutNode{Split: dir, A: &layoutNode{Pane: chatPane}, B: &layoutNode{Pane: id}}
	}
	if n.Pane != "" {
		if n.Pane == target {
			return &layoutNode{Split: dir, A: &layoutNode{Pane: n.Pane}, B: &layoutNode{Pane: id}}
		}
		return n
	}
	return &layoutNode{Split: n.Split, Ratio: n.Ratio, A: splitPane(n.A, target, dir, id), B: splitPane(n.B, target, dir, id)}
}

// withoutPane removes a pane; its sibling takes its place.
func withoutPane(n *layoutNode, id string) *layoutNode {
	if n == nil {
		return nil
	}
	if n.Pane != "" {
		if n.Pane == id {
			return nil
		}
		return n
	}
	a, b := withoutPane(n.A, id), withoutPane(n.B, id)
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return &layoutNode{Split: n.Split, Ratio: n.Ratio, A: a, B: b}
}

// splitSizes is how a split shares n cells: the first child, the
// separator (one cell) and the second.
func splitSizes(n int) (int, int) { return splitAt(n, 0) }

// splitAt shares n cells with ratio for the first child (0: half).
func splitAt(n int, ratio float64) (int, int) {
	if ratio <= 0 || ratio >= 1 {
		ratio = 0.5
	}
	a := int(float64(n-1) * ratio)
	if n >= 3 {
		a = max(1, min(n-2, a))
	}
	return max(0, a), max(0, n-1-a)
}

// place computes where each pane goes inside r.
func (n *layoutNode) place(r rect, out map[string]rect) { n.placeAll(r, out, nil) }

// placeAll also records the borders, for the mouse.
func (n *layoutNode) placeAll(r rect, out map[string]rect, seps *[]separator) {
	if n == nil {
		return
	}
	if n.Pane != "" {
		out[n.Pane] = r
		return
	}
	if n.Split == "right" {
		a, b := splitAt(r.w, n.Ratio)
		if seps != nil {
			*seps = append(*seps, separator{n, r, rect{r.x + a, r.y, 1, r.h}})
		}
		n.A.placeAll(rect{r.x, r.y, a, r.h}, out, seps)
		n.B.placeAll(rect{r.x + a + 1, r.y, b, r.h}, out, seps)
		return
	}
	a, b := splitAt(r.h, n.Ratio)
	if seps != nil {
		*seps = append(*seps, separator{n, r, rect{r.x, r.y + a, r.w, 1}})
	}
	n.A.placeAll(rect{r.x, r.y, r.w, a}, out, seps)
	n.B.placeAll(rect{r.x, r.y + a + 1, r.w, b}, out, seps)
}

func (r rect) contains(x, y int) bool { return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h }

// neighbor is the pane next to from in a direction (left, right, up,
// down), or "" at the edge.
func neighbor(rects map[string]rect, from, dir string) string {
	f, ok := rects[from]
	if !ok {
		return ""
	}
	best, bestDist, bestOff := "", 1<<30, 1<<30
	overlap := func(a1, a2, b1, b2 int) bool { return a1 < b2 && b1 < a2 }
	center := func(r rect) (int, int) { return r.x + r.w/2, r.y + r.h/2 }
	fx, fy := center(f)
	for id, r := range rects {
		if id == from {
			continue
		}
		var dist, off int
		switch dir {
		case "left":
			if r.x+r.w > f.x || !overlap(r.y, r.y+r.h, f.y, f.y+f.h) {
				continue
			}
			dist = f.x - (r.x + r.w)
			_, cy := center(r)
			off = abs(cy - fy)
		case "right":
			if r.x < f.x+f.w || !overlap(r.y, r.y+r.h, f.y, f.y+f.h) {
				continue
			}
			dist = r.x - (f.x + f.w)
			_, cy := center(r)
			off = abs(cy - fy)
		case "up":
			if r.y+r.h > f.y || !overlap(r.x, r.x+r.w, f.x, f.x+f.w) {
				continue
			}
			dist = f.y - (r.y + r.h)
			cx, _ := center(r)
			off = abs(cx - fx)
		case "down":
			if r.y < f.y+f.h || !overlap(r.x, r.x+r.w, f.x, f.x+f.w) {
				continue
			}
			dist = r.y - (f.y + f.h)
			cx, _ := center(r)
			off = abs(cx - fx)
		default:
			return ""
		}
		better := dist < bestDist || (dist == bestDist && off < bestOff)
		if dist == bestDist && off == bestOff && best != "" { // a tie: the top, then the left one
			b := rects[best]
			better = r.y < b.y || (r.y == b.y && r.x < b.x)
		}
		if better {
			best, bestDist, bestOff = id, dist, off
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (n *layoutNode) encode() string {
	if n == nil {
		return ""
	}
	data, _ := json.Marshal(n)
	return string(data)
}

func decodeLayout(s string) *layoutNode {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var n layoutNode
	if json.Unmarshal([]byte(s), &n) != nil || (n.Pane == "" && (n.A == nil || n.B == nil)) {
		return nil
	}
	return &n
}

// cell makes a styled line exactly w cells wide.
func cell(s string, w int) string {
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	return padRight(s, w)
}
