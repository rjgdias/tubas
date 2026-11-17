package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The workspace: every agent has a folder of its own that survives
// between sessions, and the team has a shared one.
//
//	notes.md          the agent's own folder: read and write, no approval
//	shared/notes.md   the team's folder: everyone reads and writes
//	@leo/notes.md     another agent's folder: read only
//
// Paths can never leave these folders: no "..", no absolute paths, and
// symbolic links that point outside are refused.

const maxFileBytes = 200_000

var agentHandle = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

type workspace struct {
	root   string // one folder per agent inside
	shared string
	agent  string
}

func workspaceTools(root, shared, agentName string) []tool {
	w := workspace{root: root, shared: shared, agent: agentName}
	paths := "Paths: 'notes.md' is in your own folder; 'shared/notes.md' is in the team's shared folder; '@name/notes.md' is in another agent's folder (read only)."
	return []tool{
		textTool("list_files", "List files in your workspace, the shared folder or another agent's folder. "+paths,
			object(map[string]any{"path": str("Folder to list; empty for your own folder")}), true,
			func(args map[string]any) (string, error) { return w.list(text(args, "path")) }),
		textTool("read_file", "Read a text file. "+paths,
			object(map[string]any{"path": str("File to read")}, "path"), true,
			func(args map[string]any) (string, error) { return w.read(text(args, "path")) }),
		{
			name:        "write_file",
			description: "Write a text file in your own folder or in the shared folder (creates or replaces it). Use it to keep notes, queries and drafts between sessions. " + paths,
			schema:      object(map[string]any{"path": str("File to write"), "content": str("The whole new content")}, "path", "content"),
			readOnly:    false,
			noApproval:  true, // writing in your own space needs nobody's permission
			call: textTool("", "", nil, false, func(args map[string]any) (string, error) {
				return w.write(text(args, "path"), rawText(args, "content"))
			}).call,
		},
	}
}

// resolve turns a workspace path into a real path, and says whether this
// agent may write there.
func (w workspace) resolve(p string, forWrite bool) (string, bool, error) {
	p = strings.TrimSpace(filepath.ToSlash(p))
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || filepath.VolumeName(p) != "" {
		return "", false, errors.New("use a path inside the workspace, not an absolute one")
	}
	base, writable := filepath.Join(w.root, w.agent), true
	switch {
	case p == "shared" || strings.HasPrefix(p, "shared/"):
		base, p = w.shared, strings.TrimPrefix(strings.TrimPrefix(p, "shared"), "/")
	case strings.HasPrefix(p, "@"):
		name, rest, _ := strings.Cut(p[1:], "/")
		if !agentHandle.MatchString(name) {
			return "", false, fmt.Errorf("unknown agent folder @%s", name)
		}
		base, writable, p = filepath.Join(w.root, name), name == w.agent, rest
	}
	clean := filepath.Clean(filepath.FromSlash("./" + p))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false, errors.New("the path leaves the workspace")
	}
	if forWrite && !writable {
		return "", false, errors.New("you can only write in your own folder or in shared/")
	}
	if writable {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", false, err
		}
	}
	full := filepath.Join(base, clean)
	if err := staysInside(full, base); err != nil {
		return "", false, err
	}
	return full, writable, nil
}

// staysInside refuses symbolic links that lead out of base.
func staysInside(full, base string) error {
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil // the folder does not exist yet: nothing to escape through
	}
	// Check the deepest part of the path that exists.
	probe := full
	for {
		if real, err := filepath.EvalSymlinks(probe); err == nil {
			rel, err := filepath.Rel(realBase, real)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errors.New("the path leads outside the workspace")
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

func (w workspace) list(p string) (string, error) {
	dir, _, err := w.resolve(p, false)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "empty folder", nil
	}
	if err != nil {
		return "", err
	}
	var lines []string
	for _, e := range entries {
		if e.IsDir() {
			lines = append(lines, e.Name()+"/")
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s (%d bytes)", e.Name(), info.Size()))
	}
	if len(lines) == 0 {
		return "empty folder", nil
	}
	sort.Strings(lines)
	return fmt.Sprintf("%d entries\n%s", len(lines), strings.Join(lines, "\n")), nil
}

func (w workspace) read(p string) (string, error) {
	path, _, err := w.resolve(p, false)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("no file %s", p)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a folder: use list_files", p)
	}
	if info.Size() > maxFileBytes {
		return "", fmt.Errorf("%s is too big to read (%d bytes)", p, info.Size())
	}
	data, err := os.ReadFile(path)
	return string(data), err
}

func (w workspace) write(p, content string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if len(content) > maxFileBytes {
		return "", fmt.Errorf("content too big (%d bytes, max %d)", len(content), maxFileBytes)
	}
	path, _, err := w.resolve(p, true)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d bytes)", p, len(content)), nil
}

// rawText reads a string argument without trimming it.
func rawText(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}
