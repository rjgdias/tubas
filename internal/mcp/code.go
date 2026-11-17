package mcp

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ── code tools, for the api harness ─────────────────────────────────────
//
// claude, codex and ACP agents bring their own file tools. An agent on the
// api harness (tubas's own loop over a provider's API) has none, so the
// gateway gives it these, confined to its folders: it reads and searches
// the repositories it may read, and writes only in its worktree. Writing
// there needs no approval (you review the whole diff at the end); shell
// commands go through run_command and wait for you.

const maxFileRead = 60000

func codeTools(readDirs, writeDirs []string) []tool {
	resolve := func(path string, dirs []string) (string, error) {
		if path == "" {
			return "", errors.New("give a path")
		}
		if !filepath.IsAbs(path) && len(dirs) > 0 {
			path = filepath.Join(dirs[0], path)
		}
		if !insideAny(path, dirs) {
			return "", fmt.Errorf("%s is outside the folders you may use: %s", path, strings.Join(dirs, ", "))
		}
		return path, nil
	}
	all := append(append([]string{}, writeDirs...), readDirs...)
	tools := []tool{
		textTool("read_file", "Read a text file of the repositories you work on. Relative paths start at "+first(all)+".",
			object(map[string]any{"path": str("the file")}, "path"), true, func(args map[string]any) (string, error) {
				path, err := resolve(fmt.Sprint(args["path"]), all)
				if err != nil {
					return "", err
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return "", err
				}
				text := string(data)
				if len(text) > maxFileRead {
					text = text[:maxFileRead] + "\n… (cut: the file is longer)"
				}
				return text, nil
			}),
		textTool("list_files", "List the files under a folder (recursively, skipping .git and node_modules).",
			object(map[string]any{"path": str("the folder (default: the first repository)")}), true, func(args map[string]any) (string, error) {
				p, _ := args["path"].(string)
				if p == "" {
					p = first(all)
				}
				path, err := resolve(p, all)
				if err != nil {
					return "", err
				}
				var files []string
				filepath.WalkDir(path, func(f string, d fs.DirEntry, err error) error {
					if err != nil {
						return nil
					}
					if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".venv") {
						return filepath.SkipDir
					}
					if !d.IsDir() {
						rel, _ := filepath.Rel(path, f)
						files = append(files, filepath.ToSlash(rel))
					}
					if len(files) >= 500 {
						return filepath.SkipAll
					}
					return nil
				})
				sort.Strings(files)
				return strings.Join(files, "\n"), nil
			}),
		textTool("search", "Search the repositories for a regular expression; returns file:line: text.",
			object(map[string]any{"pattern": str("a regular expression"), "path": str("where to search (default: every repository)")}, "pattern"), true,
			func(args map[string]any) (string, error) {
				re, err := regexp.Compile(fmt.Sprint(args["pattern"]))
				if err != nil {
					return "", err
				}
				roots := all
				if p, _ := args["path"].(string); p != "" {
					path, err := resolve(p, all)
					if err != nil {
						return "", err
					}
					roots = []string{path}
				}
				var hits []string
				for _, root := range roots {
					filepath.WalkDir(root, func(f string, d fs.DirEntry, err error) error {
						if err != nil || len(hits) >= 100 {
							return nil
						}
						if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".venv") {
							return filepath.SkipDir
						}
						if d.IsDir() {
							return nil
						}
						fh, err := os.Open(f)
						if err != nil {
							return nil
						}
						defer fh.Close()
						sc := bufio.NewScanner(fh)
						for n := 1; sc.Scan(); n++ {
							if re.MatchString(sc.Text()) {
								rel, _ := filepath.Rel(root, f)
								hits = append(hits, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), n, strings.TrimSpace(sc.Text())))
							}
						}
						return nil
					})
				}
				if len(hits) == 0 {
					return "no matches", nil
				}
				return strings.Join(hits, "\n"), nil
			}),
	}
	if len(writeDirs) > 0 {
		write := textTool("write_file", "Write a whole file in your worktree (create or replace it). A human reviews the diff at the end.",
			object(map[string]any{"path": str("the file, in your worktree"), "content": str("the whole new content")}, "path", "content"), false,
			func(args map[string]any) (string, error) {
				path, err := resolve(fmt.Sprint(args["path"]), writeDirs)
				if err != nil {
					return "", err
				}
				content, _ := args["content"].(string)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return "", err
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					return "", err
				}
				return fmt.Sprintf("wrote %s (%d bytes)", path, len(content)), nil
			})
		write.noApproval = true // its own worktree: you review the whole diff
		tools = append(tools, write)
	}
	return tools
}

func first(dirs []string) string {
	if len(dirs) == 0 {
		return "."
	}
	return dirs[0]
}
