package team

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveSettingsKeepsComments(t *testing.T) {
	dir := t.TempDir()
	orig := "name: dados\nlead: iris     # answers when nobody else is picked\nrouting: lead\nmax_hops: 10\nconnectors:\n  data:\n    builtin: sqlite # the example db\n    db: data/tubas.db\n"
	if err := os.WriteFile(filepath.Join(dir, "team.yaml"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(dir, Settings{Routing: "router", Lead: "leo", MaxHops: 6}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "team.yaml"))
	got := string(data)
	for _, want := range []string{"routing: router", "lead: leo", "max_hops: 6", "# answers when nobody else is picked", "# the example db", "db: data/tubas.db"} {
		if !strings.Contains(got, want) {
			t.Errorf("team.yaml should contain %q:\n%s", want, got)
		}
	}
	if err := SaveSettings(dir, Settings{Routing: "magic", Lead: "leo", MaxHops: 6}); err == nil {
		t.Error("an unknown routing should be refused")
	}
}
