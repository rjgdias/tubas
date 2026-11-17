package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const sample = "---\nname: job-report\ndescription: \"Report on a job: runs, tasks and failures\"\norigin: written\nversion: 3\n---\n\n# Job report\n\n1. Get the runs.\n\n---\n\nMore after a rule.\n"

func TestParseAndMarkdownRoundTrip(t *testing.T) {
	s, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "job-report" || s.Description != "Report on a job: runs, tasks and failures" ||
		s.Origin != OriginWritten || s.Version != 3 || s.Locked {
		t.Fatalf("parsed %+v", s)
	}
	if !strings.HasPrefix(s.Body, "# Job report") || !strings.Contains(s.Body, "More after a rule.") {
		t.Fatalf("body %q", s.Body)
	}

	s.Locked = true
	s.Source = "https://example.com/skills/job-report"
	again, err := Parse(s.Markdown())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, s) {
		t.Fatalf("round trip changed the skill:\n%+v\n%+v", s, again)
	}
}

func TestParseToleratesWindowsFiles(t *testing.T) {
	content := string(rune(0xFEFF)) + strings.ReplaceAll(sample, "\n", "\r\n")
	s, err := Parse(content)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "job-report" || strings.Contains(s.Body, "\r") {
		t.Fatalf("parsed %+v", s)
	}
}

func TestParseDefaultsVersionToOne(t *testing.T) {
	s, err := Parse("---\nname: a\ndescription: b\n---\nbody")
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 1 {
		t.Fatalf("version %d", s.Version)
	}
}

func TestParseValidates(t *testing.T) {
	long := strings.Repeat("x", 1025)
	for name, content := range map[string]string{
		"no header":        "just text",
		"unclosed header":  "---\nname: a\ndescription: b\n",
		"bad yaml":         "---\nname: [a\n---\n",
		"no name":          "---\ndescription: b\n---\n",
		"no description":   "---\nname: a\n---\n",
		"upper case":       "---\nname: Job\ndescription: b\n---\n",
		"spaces":           "---\nname: job report\ndescription: b\n---\n",
		"long description": "---\nname: a\ndescription: " + long + "\n---\n",
		"unknown origin":   "---\nname: a\ndescription: b\norigin: stolen\n---\n",
	} {
		if _, err := Parse(content); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"a", "job-report", "get-job-runs2", strings.Repeat("a", 64)} {
		if !ValidName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "a--b", "A", "a_b", "a b", "../x", "a/b", strings.Repeat("a", 65)} {
		if ValidName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillFile(name string) string {
	return "---\nname: " + name + "\ndescription: does " + name + "\n---\n\nSteps for " + name + ".\n"
}

func TestLoadAllKeepsGoodSkillsAndListsBrokenOnes(t *testing.T) {
	team := t.TempDir()
	skills := filepath.Join(team, ".tubas", "skills")
	writeFile(t, filepath.Join(skills, "zeta", "SKILL.md"), skillFile("zeta"))
	writeFile(t, filepath.Join(skills, "alpha", "SKILL.md"), skillFile("alpha"))
	writeFile(t, filepath.Join(skills, "alpha", "scripts", "run.sh"), "echo hi")
	writeFile(t, filepath.Join(skills, "alpha", "notes.md"), "notes")
	writeFile(t, filepath.Join(skills, "broken", "SKILL.md"), "no header here")
	writeFile(t, filepath.Join(skills, "renamed", "SKILL.md"), skillFile("other-name"))
	writeFile(t, filepath.Join(skills, ".archive", "old", "SKILL.md"), skillFile("old"))

	got, err := LoadAll(team)
	if err == nil {
		t.Fatal("expected an error for the broken skills")
	}
	for _, want := range []string{filepath.Join("broken", "SKILL.md"), filepath.Join("renamed", "SKILL.md")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not list %s: %v", want, err)
		}
	}

	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "alpha,zeta,old" {
		t.Fatalf("names %v", names)
	}
	alpha := got[0]
	if !reflect.DeepEqual(alpha.Files, []string{"notes.md", "scripts/run.sh"}) {
		t.Errorf("files %v", alpha.Files)
	}
	if !filepath.IsAbs(alpha.Dir) || alpha.Archived {
		t.Errorf("alpha %+v", alpha)
	}
	if !got[2].Archived {
		t.Errorf("old should be archived")
	}
}

func TestLoadAllWithoutSkills(t *testing.T) {
	got, err := LoadAll(t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSaveBumpsVersion(t *testing.T) {
	team := t.TempDir()
	s := Skill{Name: "job-report", Description: "Report on a job", Body: "# Job report\n\nDo it."}
	saved, err := Save(team, s)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || saved.Origin != OriginWritten || saved.Dir == "" {
		t.Fatalf("first save %+v", saved)
	}
	writeFile(t, filepath.Join(saved.Dir, "ref.md"), "reference")

	saved.Body = "# Job report\n\nDo it better."
	again, err := Save(team, saved)
	if err != nil {
		t.Fatal(err)
	}
	if again.Version != 2 || !reflect.DeepEqual(again.Files, []string{"ref.md"}) {
		t.Fatalf("second save %+v", again)
	}
	all, err := LoadAll(team)
	if err != nil || len(all) != 1 || all[0].Body != "# Job report\n\nDo it better." || all[0].Version != 2 {
		t.Fatalf("loaded %+v, %v", all, err)
	}
	// No temporary files left behind.
	entries, _ := os.ReadDir(saved.Dir)
	if len(entries) != 2 {
		t.Fatalf("folder has %d entries", len(entries))
	}
}

func TestSaveRejectsInvalidSkills(t *testing.T) {
	if _, err := Save(t.TempDir(), Skill{Name: "../escape", Description: "x"}); err == nil {
		t.Fatal("saved a skill with a bad name")
	}
	if _, err := Save(t.TempDir(), Skill{Name: "ok"}); err == nil {
		t.Fatal("saved a skill without a description")
	}
}

func TestArchiveAndRestore(t *testing.T) {
	team := t.TempDir()
	if _, err := Save(team, Skill{Name: "job-report", Description: "Report"}); err != nil {
		t.Fatal(err)
	}
	if err := Archive(team, "job-report"); err != nil {
		t.Fatal(err)
	}
	all, _ := LoadAll(team)
	if len(all) != 1 || !all[0].Archived {
		t.Fatalf("after archive %+v", all)
	}
	if err := Archive(team, "job-report"); err == nil {
		t.Fatal("archived a skill that is not there")
	}

	// A new skill with the same name blocks the restore.
	if _, err := Save(team, Skill{Name: "job-report", Description: "Report v2"}); err != nil {
		t.Fatal(err)
	}
	if err := Restore(team, "job-report"); err == nil {
		t.Fatal("restore overwrote a skill")
	}
	if err := os.RemoveAll(filepath.Join(team, ".tubas", "skills", "job-report")); err != nil {
		t.Fatal(err)
	}

	if err := Restore(team, "job-report"); err != nil {
		t.Fatal(err)
	}
	all, _ = LoadAll(team)
	if len(all) != 1 || all[0].Archived || all[0].Description != "Report" {
		t.Fatalf("after restore %+v", all)
	}
	if err := Restore(team, "../x"); err == nil {
		t.Fatal("restored a bad name")
	}
}

func TestExternalFindsSkillsOutsideTubas(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	team := t.TempDir()

	writeFile(t, filepath.Join(config, "skills", "pdf", "SKILL.md"), skillFile("pdf"))
	writeFile(t, filepath.Join(config, "skills", "taken", "SKILL.md"), skillFile("taken"))
	writeFile(t, filepath.Join(config, "skills", "broken", "SKILL.md"), "nope")
	writeFile(t, filepath.Join(team, ".claude", "skills", "deploy", "SKILL.md"), skillFile("deploy"))
	writeFile(t, filepath.Join(team, ".claude", "skills", "pdf", "SKILL.md"), skillFile("pdf"))
	if _, err := Save(team, Skill{Name: "taken", Description: "already in the team"}); err != nil {
		t.Fatal(err)
	}

	got := External(team)
	if len(got) != 2 || got[0].Name != "deploy" || got[1].Name != "pdf" {
		t.Fatalf("external %+v", got)
	}
	if got[1].Source != filepath.Join(config, "skills", "pdf") || got[1].Source != got[1].Dir {
		t.Errorf("pdf source %q dir %q", got[1].Source, got[1].Dir)
	}
}

func TestImportCopiesAFolder(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "SKILL.md"), "---\nname: deploy\ndescription: Deploy the app\norigin: written\nversion: 7\n---\nSteps.\n")
	writeFile(t, filepath.Join(src, "scripts", "deploy.sh"), "#!/bin/sh\necho deploy\n")
	writeFile(t, filepath.Join(src, ".git", "objects", "blob"), "\x00binary")

	team := t.TempDir()
	s, err := Import(team, src)
	if err != nil {
		t.Fatal(err)
	}
	if s.Origin != OriginImported || s.Source != src || s.Version != 1 || s.Body != "Steps." {
		t.Fatalf("imported %+v", s)
	}
	if !reflect.DeepEqual(s.Files, []string{"scripts/deploy.sh"}) {
		t.Fatalf("files %v", s.Files)
	}
	if _, err := Import(team, src); err == nil {
		t.Fatal("imported the same skill twice")
	}
	// Nothing half-written is left in the skills folder.
	entries, _ := os.ReadDir(filepath.Join(team, ".tubas", "skills"))
	if len(entries) != 1 {
		t.Fatalf("skills folder has %d entries", len(entries))
	}
}

func TestImportLimits(t *testing.T) {
	newSource := func(t *testing.T) string {
		src := t.TempDir()
		writeFile(t, filepath.Join(src, "SKILL.md"), skillFile("big"))
		return src
	}
	check := func(t *testing.T, src, want string) {
		t.Helper()
		team := t.TempDir()
		_, err := Import(team, src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("got %v, want an error about %q", err, want)
		}
		if _, err := os.Stat(filepath.Join(team, ".tubas", "skills", "big")); err == nil {
			t.Fatal("a refused skill was imported")
		}
	}

	t.Run("too big", func(t *testing.T) {
		src := newSource(t)
		writeFile(t, filepath.Join(src, "huge.txt"), strings.Repeat("a", maxImportBytes))
		check(t, src, "bigger")
	})
	t.Run("too many files", func(t *testing.T) {
		src := newSource(t)
		for i := range maxImportFiles {
			writeFile(t, filepath.Join(src, "refs", fmt.Sprintf("f%03d.md", i)), "x")
		}
		check(t, src, "more than")
	})
	t.Run("binary", func(t *testing.T) {
		src := newSource(t)
		writeFile(t, filepath.Join(src, "image.png"), "\x89PNG\x00\x00")
		check(t, src, "not a text file")
	})
	t.Run("symlink", func(t *testing.T) {
		src := newSource(t)
		secret := filepath.Join(t.TempDir(), "secret.txt")
		writeFile(t, secret, "secret")
		if err := os.Symlink(secret, filepath.Join(src, "link.txt")); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("cannot create symlinks here: %v", err)
			}
			t.Fatal(err)
		}
		check(t, src, "symlink")
	})
	t.Run("not a skill", func(t *testing.T) {
		if _, err := Import(t.TempDir(), t.TempDir()); err == nil {
			t.Fatal("imported a folder without SKILL.md")
		}
	})
}
