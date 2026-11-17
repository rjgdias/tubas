package skill

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Limits for Import. A skill is instructions and a few small scripts or
// references; anything bigger is probably not a skill folder at all.
const (
	maxImportBytes = 2 << 20 // 2 MB in total
	maxImportFiles = 200
)

// External lists the skills already on this computer, outside tubas:
// your own (~/.claude/skills, or $CLAUDE_CONFIG_DIR/skills) and the
// project's (<teamDir>/.claude/skills). Source and Dir are their
// folders. Skills whose name the team already has (archived included)
// are left out, and so are folders that are not valid skills.
func External(teamDir string) []Skill {
	taken := map[string]bool{}
	if team, _ := LoadAll(teamDir); team != nil {
		for _, s := range team {
			taken[s.Name] = true
		}
	}

	var roots []string
	if dir := claudeConfigDir(); dir != "" {
		roots = append(roots, filepath.Join(dir, "skills"))
	}
	if teamDir != "" {
		roots = append(roots, filepath.Join(teamDir, ".claude", "skills"))
	}

	var found []Skill
	for _, root := range roots {
		names, _ := linkedSubdirs(root)
		for _, name := range names {
			s, err := load(filepath.Join(root, name), false)
			if err != nil || taken[s.Name] {
				continue
			}
			taken[s.Name] = true // the same skill in both places: show it once
			s.Source = s.Dir
			found = append(found, s)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found
}

// claudeConfigDir is where Claude Code keeps your settings and skills.
func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".claude")
	}
	return ""
}

// Import copies a skill folder into the team, with origin "imported" and
// the folder as its source. Only text files come along, and only within
// the limits above; symlinks are refused (they could point anywhere).
// Hidden files and folders (.git, .DS_Store) are skipped.
func Import(teamDir, folder string) (Skill, error) {
	if abs, err := filepath.Abs(folder); err == nil {
		folder = abs
	}
	// The skill folder itself may be a link (a skill manager's install):
	// read the folder it points to. Links inside it are still refused.
	folder = linkTarget(folder)
	data, err := os.ReadFile(filepath.Join(folder, fileName))
	if err != nil {
		return Skill{}, fmt.Errorf("%s is not a skill folder: %w", folder, err)
	}
	s, err := Parse(string(data))
	if err != nil {
		return Skill{}, fmt.Errorf("%s: %w", filepath.Join(folder, fileName), err)
	}
	for _, archived := range []bool{false, true} {
		if _, err := os.Stat(folderFor(teamDir, s.Name, archived)); err == nil {
			return Skill{}, fmt.Errorf("the team already has a skill named %s", s.Name)
		}
	}

	files, err := readImport(folder)
	if err != nil {
		return Skill{}, err
	}

	// Write everything into a temporary folder first and rename it at the
	// end, so a failure never leaves half a skill in the team.
	root := skillsDir(teamDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Skill{}, err
	}
	tmp, err := os.MkdirTemp(root, ".import-")
	if err != nil {
		return Skill{}, err
	}
	defer os.RemoveAll(tmp) // a no-op once renamed

	s.Origin = OriginImported
	s.Source = folder
	s.Version = 1
	s.Locked = false
	files[fileName] = []byte(s.Markdown())
	for rel, content := range files {
		path := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Skill{}, err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return Skill{}, err
		}
	}
	dir := folderFor(teamDir, s.Name, false)
	if err := os.Rename(tmp, dir); err != nil {
		return Skill{}, err
	}
	return load(dir, false)
}

// readImport reads the files of a folder to import, keyed by their path
// relative to it (with forward slashes), checking every limit first.
func readImport(folder string) (map[string][]byte, error) {
	files := map[string][]byte{}
	total := 0
	err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == folder {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(folder, path)
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("%s is outside the skill folder", path)
		}
		// WalkDir does not follow symlinks, so d.Type() is the link itself.
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink: skills can only bring plain files", rel)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a plain file", rel)
		}
		if len(files)+1 > maxImportFiles {
			return fmt.Errorf("the skill has more than %d files", maxImportFiles)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if total+int(info.Size()) > maxImportBytes {
			return fmt.Errorf("the skill is bigger than %d MB", maxImportBytes>>20)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += len(content)
		if total > maxImportBytes { // it grew since we looked
			return fmt.Errorf("the skill is bigger than %d MB", maxImportBytes>>20)
		}
		if !isText(content) {
			return fmt.Errorf("%s is not a text file", rel)
		}
		files[filepath.ToSlash(rel)] = content
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := files[fileName]; !ok {
		return nil, errors.New("the folder has no SKILL.md")
	}
	return files, nil
}

// isText is a cheap check: valid UTF-8 with no NUL bytes.
func isText(b []byte) bool {
	return utf8.Valid(b) && !bytes.ContainsRune(b, 0)
}

// linkedSubdirs are a folder's subfolders, links to folders included:
// skill managers install a skill in one place and link it into
// ~/.claude/skills, and Claude Code follows those links.
func linkedSubdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		isDir := e.IsDir()
		// A link (on Windows also a junction, which Go reports as
		// irregular): follow it and see whether it is a folder.
		if !isDir && e.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// linkTarget is the folder a link (or a Windows junction) points to, so
// walking it walks the skill; any other path comes back as it is.
func linkTarget(folder string) string {
	for range 8 { // a link to a link, a few deep at most
		st, err := os.Lstat(folder)
		if err != nil || st.IsDir() {
			return folder
		}
		target, err := os.Readlink(folder)
		if err != nil {
			if real, err := filepath.EvalSymlinks(folder); err == nil {
				return real
			}
			return folder
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(folder), target)
		}
		folder = target
	}
	return folder
}
