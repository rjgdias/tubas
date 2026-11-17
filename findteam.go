package main

import (
	"fmt"
	"os"
	"path/filepath"

	"tubas/internal/team"
)

// findTeam says which team to open, so typing `tubas` anywhere works:
//
//   - a folder you named (-team DIR) is used as it is;
//   - otherwise the nearest folder with a team.yaml, from here up, the
//     way git finds its repository;
//   - outside any team, your personal team in ~/.tubas/personal, made the
//     first time (tubas init DIR makes one for a project).
func findTeam(dir string) string {
	if dir != "." {
		return dir
	}
	here, err := os.Getwd()
	if err != nil {
		return dir
	}
	for d := here; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "team.yaml")); err == nil {
			return d
		}
		if parent := filepath.Dir(d); parent == d {
			break
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return dir
	}
	personal := filepath.Join(home, ".tubas", "personal")
	if _, err := os.Stat(filepath.Join(personal, "team.yaml")); err != nil {
		if err := team.Init(personal); err != nil {
			fail("making your personal team in %s: %v", personal, err)
		}
		fmt.Fprintf(os.Stderr, "tubas: no team here, so this is your personal team (%s) · tubas init makes one for a project\n", personal)
	}
	return personal
}
