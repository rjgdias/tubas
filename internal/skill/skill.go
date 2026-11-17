// Package skill keeps a team's skills: folders with a SKILL.md (a YAML
// header between --- lines, then Markdown) and maybe a few supporting
// files, in the Agent Skills layout that Claude Code also reads.
//
// A team's skills live in <teamDir>/.tubas/skills/<name>/. Archived ones
// move to <teamDir>/.tubas/skills/.archive/<name>/, so nothing is lost
// when you put a skill away. Which agents use a skill is written in the
// agent files, not here.
package skill

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Where tubas learned or got a skill.
const (
	OriginLearned  = "learned"  // suggested from an agent's repeated work
	OriginWritten  = "written"  // written by a person (or an agent, on request)
	OriginImported = "imported" // copied from a folder on this computer
)

const (
	fileName       = "SKILL.md"
	maxNameLen     = 64
	maxDescription = 1024
)

// Skill is one skill, as read from (or about to be written to) its folder.
type Skill struct {
	Name        string
	Description string
	Body        string // the Markdown after the header

	Origin  string // learned, written, imported
	Source  string // where it came from: a path or URL ("" when unknown)
	Locked  bool   // a person said: leave this one as it is
	Version int    // starts at 1, +1 on each Save of an existing skill

	Dir      string   // absolute folder
	Files    []string // other files in the folder, relative, sorted (not SKILL.md)
	Archived bool
}

// header is the YAML part of SKILL.md. The order of the fields here is
// the order Markdown writes them in.
type header struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Origin      string `yaml:"origin,omitempty"`
	Source      string `yaml:"source,omitempty"`
	Locked      bool   `yaml:"locked,omitempty"`
	Version     int    `yaml:"version,omitempty"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidName says whether name is kebab-case (lowercase letters, digits
// and single hyphens) and at most 64 characters, as Agent Skills asks.
// It also keeps names safe to use as folder names.
func ValidName(name string) bool {
	return len(name) <= maxNameLen && namePattern.MatchString(name)
}

// Parse reads the content of a SKILL.md. Dir, Files and Archived stay
// empty: they depend on where the file is, not on what it says.
func Parse(content string) (Skill, error) {
	// Files saved on Windows end lines with \r\n, and some editors add a
	// BOM. Normalizing keeps the "---" lines recognizable.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimPrefix(content, string(rune(0xFEFF)))

	if !strings.HasPrefix(content, "---\n") {
		return Skill{}, errors.New("SKILL.md must start with a --- line")
	}
	rest := content[len("---\n"):]

	// The header ends at the first line that is exactly "---". The body
	// may have more of them (Markdown rules), which is fine.
	var head, body string
	switch {
	case strings.HasPrefix(rest, "---\n") || rest == "---":
		body = strings.TrimPrefix(rest, "---")
	default:
		end := strings.Index(rest, "\n---\n")
		if end < 0 {
			if !strings.HasSuffix(rest, "\n---") {
				return Skill{}, errors.New("missing the --- line that closes the header")
			}
			end = len(rest) - len("\n---")
		}
		head, body = rest[:end], rest[end+len("\n---"):]
	}

	var h header
	if err := yaml.Unmarshal([]byte(head), &h); err != nil {
		return Skill{}, fmt.Errorf("invalid YAML header: %w", err)
	}
	s := Skill{
		Name:        strings.TrimSpace(h.Name),
		Description: strings.TrimSpace(h.Description),
		Body:        strings.TrimSpace(body),
		Origin:      h.Origin,
		Source:      h.Source,
		Locked:      h.Locked,
		Version:     h.Version,
	}
	if s.Version < 1 {
		s.Version = 1 // skills from elsewhere have no version: they are a first one
	}
	return s, validate(s)
}

func validate(s Skill) error {
	switch {
	case s.Name == "":
		return errors.New("missing the name field")
	case !ValidName(s.Name):
		return fmt.Errorf("invalid name %q: use lowercase letters, digits and hyphens, at most %d characters", s.Name, maxNameLen)
	case s.Description == "":
		return fmt.Errorf("skill %s: missing the description field", s.Name)
	case len([]rune(s.Description)) > maxDescription:
		return fmt.Errorf("skill %s: the description is longer than %d characters", s.Name, maxDescription)
	}
	switch s.Origin {
	case "", OriginLearned, OriginWritten, OriginImported:
	default:
		return fmt.Errorf("skill %s: unknown origin %q (use learned, written or imported)", s.Name, s.Origin)
	}
	return nil
}

// Markdown is the content of the skill's SKILL.md: what Save writes.
func (s Skill) Markdown() string {
	h := header{
		Name:        s.Name,
		Description: s.Description,
		Origin:      s.Origin,
		Source:      s.Source,
		Locked:      s.Locked,
		Version:     s.Version,
	}
	// Marshaling a struct of strings, bools and ints cannot fail.
	head, _ := yaml.Marshal(h)
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(head)
	b.WriteString("---\n")
	if body := strings.TrimSpace(s.Body); body != "" {
		b.WriteString("\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.String()
}

// ── where skills live ───────────────────────────────────────────────────

func skillsDir(teamDir string) string  { return filepath.Join(teamDir, ".tubas", "skills") }
func archiveDir(teamDir string) string { return filepath.Join(skillsDir(teamDir), ".archive") }

func folderFor(teamDir, name string, archived bool) string {
	if archived {
		return filepath.Join(archiveDir(teamDir), name)
	}
	return filepath.Join(skillsDir(teamDir), name)
}

// load reads one skill folder.
func load(dir string, archived bool) (Skill, error) {
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		return Skill{}, err
	}
	s, err := Parse(string(data))
	if err != nil {
		return Skill{}, err
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	s.Dir = dir
	s.Archived = archived
	s.Files, err = otherFiles(dir)
	return s, err
}

// otherFiles lists the supporting files of a skill folder, with forward
// slashes so the list looks the same on every OS.
func otherFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel = filepath.ToSlash(rel); rel != fileName {
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// subdirs lists the folder names in dir, sorted, without hidden ones
// (that is how .archive stays out of the team's own skills).
func subdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// LoadAll reads the team's skills, then its archived ones, each group
// sorted by name. A broken skill does not hide the others: they are all
// returned, together with an error that lists every broken path.
func LoadAll(teamDir string) ([]Skill, error) {
	var skills []Skill
	var broken []string
	for _, archived := range []bool{false, true} {
		root := skillsDir(teamDir)
		if archived {
			root = archiveDir(teamDir)
		}
		names, err := subdirs(root)
		if err != nil {
			return skills, err
		}
		for _, name := range names {
			dir := filepath.Join(root, name)
			s, err := load(dir, archived)
			if err == nil && s.Name != name {
				// Archive and Restore find a skill by its folder.
				err = fmt.Errorf("its name %q does not match its folder", s.Name)
			}
			if err != nil {
				broken = append(broken, fmt.Sprintf("%s: %v", filepath.Join(dir, fileName), err))
				continue
			}
			skills = append(skills, s)
		}
	}
	if len(broken) > 0 {
		return skills, fmt.Errorf("some skills could not be read:\n  %s", strings.Join(broken, "\n  "))
	}
	return skills, nil
}

// Save writes the skill's SKILL.md (in the archive when s.Archived),
// creating its folder if needed. Saving over an existing skill bumps its
// version; a new skill starts at 1. It returns the skill as saved.
func Save(teamDir string, s Skill) (Skill, error) {
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	if s.Origin == "" {
		s.Origin = OriginWritten
	}
	if err := validate(s); err != nil {
		return Skill{}, err
	}
	dir := folderFor(teamDir, s.Name, s.Archived)
	path := filepath.Join(dir, fileName)

	s.Version = 1
	if data, err := os.ReadFile(path); err == nil {
		prev := 0
		if old, err := Parse(string(data)); err == nil {
			prev = old.Version
		}
		s.Version = max(prev, 1) + 1
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Skill{}, err
	}
	if err := writeAtomic(path, []byte(s.Markdown())); err != nil {
		return Skill{}, err
	}
	return load(dir, s.Archived)
}

// writeAtomic writes a file through a temporary one next to it, so a
// crash halfway leaves the old file, never half of the new one.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

// Archive puts a skill away: its folder moves to .archive.
func Archive(teamDir, name string) error {
	return move(teamDir, name, false)
}

// Restore brings an archived skill back.
func Restore(teamDir, name string) error {
	return move(teamDir, name, true)
}

func move(teamDir, name string, fromArchive bool) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid skill name %q", name)
	}
	from := folderFor(teamDir, name, fromArchive)
	to := folderFor(teamDir, name, !fromArchive)
	if _, err := os.Stat(filepath.Join(from, fileName)); err != nil {
		if fromArchive {
			return fmt.Errorf("no archived skill named %s", name)
		}
		return fmt.Errorf("no skill named %s", name)
	}
	if _, err := os.Stat(to); err == nil {
		if fromArchive {
			return fmt.Errorf("the team already has a skill named %s", name)
		}
		return fmt.Errorf("an archived skill named %s already exists", name)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.Rename(from, to)
}
