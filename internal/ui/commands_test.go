package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"tubas/internal/engine"
)

func TestSlashAgentsOpensTheForm(t *testing.T) {
	m, _, _ := remoteModel(t, nil)
	m = feed(m, tea.WindowSizeMsg{Width: 110, Height: 34})
	t.Cleanup(m.Stop)
	// Some terminals also send ctrl+g as the text "\a"; an invisible
	// character must not turn the command into a question.
	m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\a'}})
	m = m.PressKeys("/agents", "enter")
	if m.section != SecAgents || m.form == nil || m.running {
		t.Fatalf("/agents opens the new agent form: section %v, form %v, running %v", m.section, m.form != nil, m.running)
	}
}

func TestGatewayListsOnlyMappedShortNames(t *testing.T) {
	m, _, _ := remoteModel(t, nil)
	t.Cleanup(m.Stop)
	m.cfg.Providers = map[string][]string{"anthropic": {"acme-claude-sonnet", "acme-llama-70b"}}
	m.onGateway, m.aliases = true, map[string]string{"sonnet": "acme-claude-sonnet"}
	got := m.modelsFor("claude", "anthropic", "")
	want := []string{"sonnet", "acme-claude-sonnet", "acme-llama-70b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("on a gateway, only the short names your settings map: got %v, want %v", got, want)
	}
	m.onGateway = false
	if got := m.modelsFor("claude", "anthropic", ""); !slices.Contains(got, "opus") {
		t.Errorf("on Anthropic itself, every short name: %v", got)
	}
}

func TestAFailingProviderShowsOnScreen(t *testing.T) {
	s := Session{Question: "oi", StartedAt: time.Now()}
	s = s.apply(engine.TurnStarted{Agent: "leo", From: "you"})
	s = s.apply(engine.Retrying{Agent: "leo", Attempt: 6, Max: 10, Status: 500})
	if got := s.Turns[0].Retry; got != "the provider failed (500) · trying again, 6 of 10" {
		t.Errorf("a retry shows on the turn, got %q", got)
	}
	long := "leo: claude: API Error: 500 upstream failed. This is a server-side issue, usually temporary, try again in a moment. If it persists, check your inference gateway."
	s = s.apply(engine.Failed{Agent: "leo", Err: errors.New(long[5:])})
	if tr := s.Turns[0]; tr.Status != Stopped || tr.Retry != "" {
		t.Errorf("a failed session's turn is no longer working: %+v", tr)
	}
	text := ansi.Strip(strings.Join(outcomeLines(s, 60), " "))
	for _, want := range []string{"failed", "check your inference gateway"} {
		if !strings.Contains(text, want) {
			t.Errorf("the whole reason shows, wrapped: %q missing in %q", want, text)
		}
	}
}
