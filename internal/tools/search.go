package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	maxGlobResults = 200
	maxGrepLines   = 250
	maxWalkFiles   = 50000
)

var globTool = Tool{
	Name: "glob",
	Description: "Find files by name with a glob pattern: \"**/*.go\" (any depth), \"src/*.ts\", \"**/test_*.py\", \"*.{js,ts}\". " +
		"Newest first. Ignored files (.gitignore) are left out.",
	Schema:   schema(map[string]any{"pattern": str("the glob pattern"), "path": str("the folder to look in (default: every folder you may read)")}, "pattern"),
	ReadOnly: true,
	run: func(ctx context.Context, b *Box, input json.RawMessage) (string, error) {
		var in struct{ Pattern, Path string }
		if err := decode(input, &in); err != nil {
			return "", err
		}
		re, err := globRegexp(strings.TrimPrefix(strings.TrimSpace(in.Pattern), "./"))
		if err != nil {
			return "", err
		}
		roots, err := b.roots(in.Path)
		if err != nil {
			return "", err
		}
		type hit struct {
			path string
			mod  time.Time
		}
		var hits []hit
		for _, root := range roots {
			for _, rel := range listFiles(ctx, root) {
				if re.MatchString(rel) {
					full := filepath.Join(root, filepath.FromSlash(rel))
					var mod time.Time
					if info, err := os.Stat(full); err == nil {
						mod = info.ModTime()
					}
					hits = append(hits, hit{full, mod})
				}
			}
		}
		if len(hits) == 0 {
			return "no files match " + in.Pattern, nil
		}
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].mod.After(hits[j].mod) })
		var out []string
		for i, h := range hits {
			if i == maxGlobResults {
				out = append(out, fmt.Sprintf("… and %d more: narrow the pattern", len(hits)-i))
				break
			}
			out = append(out, h.path)
		}
		return strings.Join(out, "\n"), nil
	},
}

var grepTool = Tool{
	Name: "grep",
	Description: "Search file contents for a regular expression. mode \"content\" (default) gives file:line: text, \"files\" only the files, " +
		"\"count\" how many lines match in each. glob narrows the files (\"*.go\", \"**/*.{ts,tsx}\").",
	Schema: schema(map[string]any{
		"pattern":     str("a regular expression"),
		"path":        str("a file or folder (default: every folder you may read)"),
		"glob":        str("only files whose name matches this glob"),
		"mode":        map[string]any{"type": "string", "enum": []string{"content", "files", "count"}},
		"ignore_case": flag("match upper and lower case alike"),
		"context":     num("lines to show before and after each match (content mode)"),
	}, "pattern"),
	ReadOnly: true,
	run: func(ctx context.Context, b *Box, input json.RawMessage) (string, error) {
		var in grepInput
		if err := decode(input, &in); err != nil {
			return "", err
		}
		if in.Pattern == "" {
			return "", errors.New("give a pattern")
		}
		if in.Mode == "" {
			in.Mode = "content"
		}
		expr := in.Pattern
		if in.IgnoreCase {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return "", fmt.Errorf("the pattern isn't a valid regular expression: %v", err)
		}
		roots, err := b.roots(in.Path)
		if err != nil {
			return "", err
		}
		var lines []string
		if rg := ripgrep(); rg != "" {
			lines, err = in.withRipgrep(ctx, rg, roots)
		} else {
			lines, err = in.walk(ctx, re, roots)
		}
		if err != nil {
			return "", err
		}
		if len(lines) == 0 {
			return "no matches", nil
		}
		if len(lines) > maxGrepLines {
			more := len(lines) - maxGrepLines
			lines = append(lines[:maxGrepLines], fmt.Sprintf("… and %d more lines: narrow the search", more))
		}
		return strings.Join(lines, "\n"), nil
	},
}

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	Mode       string `json:"mode"`
	IgnoreCase bool   `json:"ignore_case"`
	Context    int    `json:"context"`
}

// useRipgrep lets tests try the walker on a machine that has rg.
var useRipgrep = true

func ripgrep() string {
	if !useRipgrep {
		return ""
	}
	rg, _ := exec.LookPath("rg")
	return rg
}

func (in grepInput) withRipgrep(ctx context.Context, rg string, roots []string) ([]string, error) {
	args := []string{"--line-number", "--no-heading", "--color=never", "--max-columns=500", "--max-columns-preview"}
	if in.IgnoreCase {
		args = append(args, "-i")
	}
	if in.Glob != "" {
		args = append(args, "--glob", in.Glob)
	}
	switch in.Mode {
	case "files":
		args = append(args, "--files-with-matches")
	case "count":
		args = append(args, "--count")
	default:
		if in.Context > 0 {
			args = append(args, "-C", fmt.Sprint(min(in.Context, 20)))
		}
	}
	args = append(args, "-e", in.Pattern, "--")
	args = append(args, roots...)
	cmd := exec.CommandContext(ctx, rg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return nil, nil // no matches
	}
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("rg: %v %s", err, firstLine(stderr.String()))
	}
	text := strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// walk searches without rg: the same files, Go's regular expressions.
func (in grepInput) walk(ctx context.Context, re *regexp.Regexp, roots []string) ([]string, error) {
	var only *regexp.Regexp
	if in.Glob != "" {
		g := in.Glob
		if !strings.Contains(g, "/") {
			g = "**/" + g // a name pattern matches at any depth, as rg does
		}
		var err error
		if only, err = globRegexp(g); err != nil {
			return nil, err
		}
	}
	var out []string
	for _, root := range roots {
		files := []string{""}
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			files = listFiles(ctx, root)
		}
		for _, rel := range files {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if only != nil && rel != "" && !only.MatchString(rel) {
				continue
			}
			full := root
			if rel != "" {
				full = filepath.Join(root, filepath.FromSlash(rel))
			}
			out = append(out, in.searchFile(re, full)...)
			if len(out) > maxGrepLines*2 {
				return out, nil
			}
		}
	}
	return out, nil
}

func (in grepInput) searchFile(re *regexp.Regexp, path string) []string {
	data, err := os.ReadFile(path)
	if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil // unreadable or binary
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var out []string
	matches := 0
	lastShown := -1
	for i, line := range lines {
		if !re.MatchString(line) {
			continue
		}
		matches++
		if in.Mode != "content" {
			continue
		}
		from, to := max(i-in.Context, lastShown+1), min(i+in.Context, len(lines)-1)
		for j := from; j <= to; j++ {
			sep := "-"
			if j == i || re.MatchString(lines[j]) {
				sep = ":"
			}
			text := lines[j]
			if len(text) > 500 {
				text = text[:500] + "…"
			}
			out = append(out, fmt.Sprintf("%s%s%d%s%s", path, ":", j+1, sep, text))
		}
		lastShown = to
	}
	switch {
	case matches == 0:
		return nil
	case in.Mode == "files":
		return []string{path}
	case in.Mode == "count":
		return []string{fmt.Sprintf("%s:%d", path, matches)}
	}
	return out
}

// roots is where a search looks: the path asked for, or every folder.
func (b *Box) roots(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return b.all(), nil
	}
	p, err := b.resolve(path, b.all())
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(p); err != nil {
		return nil, noFile(err, p)
	}
	return []string{p}, nil
}

// listFiles lists a folder's files, relative and with forward slashes:
// git's list in a repository (tracked and new, not ignored), else a walk
// that skips the usual heavy folders.
func listFiles(ctx context.Context, root string) []string {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if out, err := cmd.Output(); err == nil {
		var files []string
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f))); err == nil { // deleted but still tracked: gone
					files = append(files, f)
				}
			}
		}
		return files
	}
	var files []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".venv", "venv", "__pycache__", "target", "dist", ".tubas":
				if p != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= maxWalkFiles {
			return filepath.SkipAll
		}
		return nil
	})
	return files
}

// globRegexp turns a glob into a regular expression over a relative path
// with forward slashes: ** crosses folders, * and ? don't, {a,b} is
// either one.
func globRegexp(glob string) (*regexp.Regexp, error) {
	if glob == "" {
		return nil, errors.New("give a pattern")
	}
	var re strings.Builder
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		re.WriteString("(?i)") // names are case-insensitive there
	}
	re.WriteString("^")
	depth := 0 // inside {…}
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			re.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			re.WriteString(".*")
			i++
		case c == '*':
			re.WriteString("[^/]*")
		case c == '?':
			re.WriteString("[^/]")
		case c == '{':
			re.WriteString("(?:")
			depth++
		case c == '}' && depth > 0:
			re.WriteString(")")
			depth--
		case c == ',' && depth > 0:
			re.WriteString("|")
		case c == '[':
			j := strings.IndexByte(glob[i:], ']')
			if j < 0 {
				re.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			re.WriteString("[" + class + "]")
			i += j
		case c == '\\':
			re.WriteString("/")
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	re.WriteString("$")
	r, err := regexp.Compile(re.String())
	if err != nil {
		return nil, fmt.Errorf("the pattern %q isn't a glob tubas understands: %v", glob, err)
	}
	return r, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
