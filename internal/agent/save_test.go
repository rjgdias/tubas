package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := Agent{Name: "ana", Role: "data analyst", Color: "green", Model: "haiku", Effort: "low",
		Tools: map[string]string{"data": "read", "reports": "write", "slack": "none"}, CanCall: []string{"leo"},
		Skills: []string{"check-job"}, Escalate: &Escalation{To: "opus", On: []string{"tool_fails"}, PerDay: 3},
		Instructions: "Você é a ana.\n\n- Mostre a consulta."}
	saved, err := Save(dir, a)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(saved.File)
	text := string(data)
	if strings.Contains(text, "slack") || strings.Contains(text, "harness") || strings.Contains(text, "token_limit") {
		t.Errorf("defaults and none should stay out of the file:\n%s", text)
	}
	back, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if back.Model != "haiku" || back.Tools["reports"] != "write" || back.Escalate == nil || back.Escalate.To != "opus" ||
		len(back.Skills) != 1 || back.Instructions != a.Instructions || back.CanCall[0] != "leo" {
		t.Errorf("round trip lost something: %+v", back)
	}
}

func TestSaveRenameRemovesTheOldFile(t *testing.T) {
	dir := t.TempDir()
	a, err := Save(dir, Agent{Name: "ana", Instructions: "x"})
	if err != nil {
		t.Fatal(err)
	}
	a.Name = "bia"
	if _, err := Save(dir, a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ana.md")); !os.IsNotExist(err) {
		t.Error("the old file should be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "bia.md")); err != nil {
		t.Error("the new file should exist")
	}
}

func TestSaveRefusesABadAgent(t *testing.T) {
	if _, err := Save(t.TempDir(), Agent{Name: "Ana Maria", Instructions: "x"}); err == nil {
		t.Error("a name with spaces and capitals should be refused")
	}
	if _, err := Save(t.TempDir(), Agent{Name: "ana", Instructions: "x", Escalate: &Escalation{To: "opus", On: []string{"always"}}}); err == nil {
		t.Error("an unknown trigger should be refused")
	}
}

func TestEscalationDefaults(t *testing.T) {
	e := Escalation{To: "opus"}
	if !e.Has("ask") || !e.Has("before_write") || e.Limit() != 5 {
		t.Errorf("empty on should mean every trigger, 5 a day: %+v", e)
	}
}
