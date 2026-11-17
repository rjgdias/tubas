package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A conversation is every session of one tubas run. It is saved as JSON
// after each session, so it can be reopened with -resume.

type savedConversation struct {
	Team     string    `json:"team"`
	Saved    time.Time `json:"saved"`
	Sessions []Session `json:"sessions"`
}

// noticeMsg shows a short message next to the input.
type noticeMsg string

// save returns a command that writes the conversation to disk. File
// writing is a side effect, so it runs as a command, outside Update.
func (m Model) save() tea.Cmd {
	if m.cfg.HistoryFile == "" {
		return nil
	}
	sessions := slices.Clone(m.past)
	if m.session.started() {
		sessions = append(sessions, m.session)
	}
	conv := savedConversation{Team: m.cfg.Name, Saved: time.Now(), Sessions: sessions}
	path := m.cfg.HistoryFile
	return func() tea.Msg {
		if err := writeJSON(path, conv); err != nil {
			return noticeMsg(badStyle.Render("could not save history: " + err.Error()))
		}
		return nil
	}
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadHistory reads a saved conversation.
func LoadHistory(path string) ([]Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var conv savedConversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return conv.Sessions, nil
}

// LatestHistory finds the most recent conversation saved in dir.
func LatestHistory(dir string) (string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no saved conversations in %s", dir)
	}
	slices.Sort(files) // names start with the date, so the last one is the newest
	return files[len(files)-1], nil
}

// NewHistoryFile names a file for a new conversation.
func NewHistoryFile(dir string) string {
	return filepath.Join(dir, time.Now().Format("2006-01-02-150405")+".json")
}

// conversationContext tells the agents what was asked and concluded
// earlier, so a follow-up does not start from zero.
func conversationContext(past []Session) string {
	if len(past) == 0 {
		return ""
	}
	if len(past) > 5 {
		past = past[len(past)-5:]
	}
	var b strings.Builder
	b.WriteString("Earlier in this conversation (for context):\n")
	for _, s := range past {
		who, answer := s.finalAnswer()
		if answer == "" {
			who, answer = "nobody", "(no answer: the session was stopped)"
		}
		if r := []rune(answer); len(r) > 1200 {
			answer = string(r[:1200]) + "…"
		}
		fmt.Fprintf(&b, "- The human asked %s: %s\n  Answer from @%s: %s\n", s.addressee(), s.Question, who, answer)
	}
	return b.String()
}
