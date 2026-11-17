package ui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
