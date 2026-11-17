package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinkedSkillFoldersAreFound(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	real := filepath.Join(t.TempDir(), "brainstorming")
	os.MkdirAll(real, 0o755)
	os.WriteFile(filepath.Join(real, "SKILL.md"), []byte("---\nname: brainstorming\ndescription: ideas first\n---\nThink of ideas."), 0o644)
	os.MkdirAll(filepath.Join(cfg, "skills"), 0o755)
	if err := os.Symlink(real, filepath.Join(cfg, "skills", "brainstorming")); err != nil {
		t.Skip("symlinks need permission here:", err)
	}
	found := External(t.TempDir())
	if len(found) != 1 || found[0].Name != "brainstorming" {
		t.Fatalf("a linked skill folder is found: %+v", found)
	}
	team := t.TempDir()
	if s, err := Import(team, found[0].Source); err != nil || s.Name != "brainstorming" {
		t.Errorf("and imported from where it points: %+v %v", s, err)
	}
}
