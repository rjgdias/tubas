package tools

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// InsideAny says whether path is inside one of dirs, symbolic links
// included: a link in the worktree that points elsewhere doesn't count.
func InsideAny(path string, dirs []string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, d := range dirs {
		base, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(base, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		if StaysInside(abs, base) == nil {
			return true
		}
	}
	return false
}

// StaysInside checks that full, with its links followed, is still under
// base. A path that doesn't exist yet is checked through its deepest
// existing folder.
func StaysInside(full, base string) error {
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil // the folder does not exist yet: nothing to escape through
	}
	probe := full
	for {
		if real, err := filepath.EvalSymlinks(probe); err == nil {
			rel, err := filepath.Rel(realBase, real)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return errors.New("the path leads outside the folder")
			}
			return nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil
		}
		probe = parent
	}
}

// resolve turns a path the model gave into an absolute one inside dirs.
func (b *Box) resolve(path string, dirs []string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("give a path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.home(), path)
	}
	path = filepath.Clean(path)
	if !InsideAny(path, dirs) {
		if len(b.Write) > 0 && InsideAny(path, b.all()) {
			return "", fmt.Errorf("%s is read only: you change files only in your worktree, %s", path, strings.Join(b.Write, ", "))
		}
		return "", fmt.Errorf("%s is outside the folders you may use: %s", path, strings.Join(dirs, ", "))
	}
	return path, nil
}

// show is a path as the screen shows it: relative to the worktree when
// it's in there, else whole.
func (b *Box) show(path string) string {
	if rel, err := filepath.Rel(b.home(), path); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return filepath.ToSlash(rel)
	}
	return path
}
