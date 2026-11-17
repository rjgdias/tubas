package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tubas/internal/repo"
)

// ── code: reviewing what a session changed ──────────────────────────────
//
// When a session that wrote code ends, the chat shows what changed in
// each repository; v opens the review: the files, the diff, and what to
// do with it: merge into your branch, push for a pull request, or
// discard. Your checkout was never touched until you choose.

// wtChanges is one worktree and what it changed.
type wtChanges struct {
	wt      repo.Worktree
	changes repo.Changes
	patch   string
	err     string
}

type changesMsg []wtChanges

// sessionWorktrees are the worktrees of the session on screen.
func (m Model) sessionWorktrees() []repo.Worktree {
	if m.cfg.Remote == nil {
		if m.code == nil {
			return nil
		}
		return m.code.Worktrees()
	}
	if st, err := m.cfg.Remote.Status(); err == nil {
		for _, s := range st.Sessions {
			if s.ID == m.remoteID {
				return s.Worktrees
			}
		}
	}
	return nil
}

// loadChanges reads the diffs of the session's worktrees.
func (m Model) loadChanges() Model {
	var out []wtChanges
	for _, wt := range m.sessionWorktrees() {
		ch, patch, err := repo.Diff(wt)
		c := wtChanges{wt: wt, changes: ch, patch: patch}
		if err != nil {
			c.err = err.Error()
		}
		if len(ch.Files) > 0 || c.err != "" {
			out = append(out, c)
		}
	}
	m.changes = out
	return m
}

// changesLines go under a finished session in the chat.
func (m Model) changesLines(w int) []string {
	if len(m.changes) == 0 || m.running {
		return nil
	}
	var lines []string
	for _, c := range m.changes {
		if c.err != "" {
			lines = append(lines, badStyle.Render("  ✎ "+c.wt.Repo+": "+c.err))
			continue
		}
		lines = append(lines, fit(hiStyle.Render("  ✎ "+c.wt.Repo)+dimStyle.Render(fmt.Sprintf(" · %d files · ", len(c.changes.Files)))+
			okStyle.Render(fmt.Sprintf("+%d", c.changes.Added))+dimStyle.Render(" ")+badStyle.Render(fmt.Sprintf("−%d", c.changes.Removed))+
			dimStyle.Render(" · "+c.wt.Branch+" · ")+hiStyle.Render("v")+dimStyle.Render(" review"), w))
	}
	return lines
}

func (m Model) reviewKey(key string) (Model, tea.Cmd) {
	if m.reviewWT >= len(m.changes) {
		m.review = false
		return m, nil
	}
	c := m.changes[m.reviewWT]
	switch key {
	case "esc", "v":
		m.review = false
	case "up":
		m.fileIdx = max(0, m.fileIdx-1)
	case "down":
		m.fileIdx = max(0, min(len(c.changes.Files)-1, m.fileIdx+1))
	case "tab":
		m.reviewWT = (m.reviewWT + 1) % len(m.changes)
		m.fileIdx = 0
	case "m":
		return m, codeAction(c.wt, "merge", m.session.Question)
	case "p":
		return m, codeAction(c.wt, "push", m.session.Question)
	case "d":
		if m.confirm != "discard "+c.wt.Branch {
			m.confirm = "discard " + c.wt.Branch
			m.notice = warnStyle.Render("press d again to throw away " + c.wt.Branch)
			return m, nil
		}
		m.confirm = ""
		return m, codeAction(c.wt, "discard", "")
	}
	return m, nil
}

// codeActionMsg is the result of merge, push or discard.
type codeActionMsg struct {
	branch, action, result string
	err                    error
}

func codeAction(wt repo.Worktree, action, question string) tea.Cmd {
	return func() tea.Msg {
		title := "tubas: " + strings.TrimSpace(question)
		if r := []rune(title); len(r) > 72 {
			title = string(r[:72])
		}
		var err error
		result := ""
		switch action {
		case "merge":
			err = repo.Merge(wt, title)
			result = "merged " + wt.Branch + " into " + wt.Base
		case "push":
			result, err = repo.Push(wt, title)
		case "discard":
			err = repo.Discard(wt)
			result = "discarded " + wt.Branch
		}
		return codeActionMsg{wt.Branch, action, result, err}
	}
}

func (m Model) codeActionDone(msg codeActionMsg) Model {
	if msg.err != nil {
		m.notice = badStyle.Render(msg.action + ": " + msg.err.Error())
		return m
	}
	m.notice = okStyle.Render("✓ " + msg.result)
	if msg.action == "discard" || msg.action == "merge" {
		var keep []wtChanges
		for _, c := range m.changes {
			if c.wt.Branch != msg.branch {
				keep = append(keep, c)
			}
		}
		m.changes = keep
		if msg.action == "discard" || len(keep) == 0 {
			m.review = len(keep) > 0
		}
		m.reviewWT, m.fileIdx = 0, 0
	}
	return m
}

func (m Model) viewReview(cw, h int) []string {
	if m.reviewWT >= len(m.changes) {
		return fitHeight(nil, h, false)
	}
	c := m.changes[m.reviewWT]
	head := boldStyle.Render("review · "+c.wt.Repo) + dimStyle.Render(" · "+c.wt.Branch+" from "+c.wt.Base+" · ") +
		okStyle.Render(fmt.Sprintf("+%d", c.changes.Added)) + " " + badStyle.Render(fmt.Sprintf("−%d", c.changes.Removed))
	lines := []string{fit(head, cw)}
	if c.wt.SetupErr != "" {
		lines = append(lines, fit(warnStyle.Render("setup failed: "+c.wt.SetupErr), cw))
	}
	lines = append(lines, "")
	lw := listWidth(cw) + 6
	left := []string{dimStyle.Render(fmt.Sprintf("%d files", len(c.changes.Files))), ""}
	for i, f := range c.changes.Files {
		nums := okStyle.Render(fmt.Sprintf("+%d", f.Added)) + " " + badStyle.Render(fmt.Sprintf("−%d", f.Removed))
		left = append(left, listRow(f.Path, nums, i == m.fileIdx, lw))
	}
	left = append(left, "", dimStyle.Render("your checkout"), faintStyle.Render(fit(c.wt.Checkout, lw)),
		faintStyle.Render("untouched until you choose"))
	var right []string
	if m.fileIdx < len(c.changes.Files) {
		right = colorDiff(filePatch(c.patch, c.changes.Files[m.fileIdx].Path), cw-lw-3)
	}
	body := panes(left, right, lw, cw, max(0, h-len(lines)-2))
	lines = append(lines, body...)
	bar := barStyle.Render(" what now with ") + barBold.Render(c.wt.Branch) + barStyle.Render("?   ") +
		barBold.Render("m") + barStyle.Render(" merge into "+c.wt.Base+"   ") + barBold.Render("p") + barStyle.Render(" push for a PR   ") +
		barBold.Render("d") + barStyle.Render(" discard   ") + barBold.Render("esc") + barStyle.Render(" back ")
	lines = append(lines, "", spread(fit(bar, cw), "", cw))
	return fitHeight(lines, h, false)
}

// filePatch is the part of a patch about one file.
func filePatch(patch, path string) string {
	var out []string
	in := false
	for _, l := range strings.Split(patch, "\n") {
		if strings.HasPrefix(l, "diff --git ") {
			in = strings.HasSuffix(l, " b/"+path)
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// colorDiff paints a patch: green added lines, red removed, blue hunks.
func colorDiff(patch string, w int) []string {
	var out []string
	for _, l := range strings.Split(patch, "\n") {
		l = strings.ReplaceAll(strings.TrimRight(l, "\r"), "\t", "    ")
		switch {
		case strings.HasPrefix(l, "diff --git"), strings.HasPrefix(l, "index "), strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			continue
		case strings.HasPrefix(l, "@@"):
			out = append(out, linkStyle.Render(fit(l, w)))
		case strings.HasPrefix(l, "+"):
			out = append(out, okStyle.Render(fit(l, w)))
		case strings.HasPrefix(l, "-"):
			out = append(out, badStyle.Render(fit(l, w)))
		default:
			out = append(out, textStyle.Render(fit(l, w)))
		}
	}
	return out
}
