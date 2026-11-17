package team

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ── MCP servers from Claude Code ────────────────────────────────────────
//
// Claude Code keeps MCP servers in ~/.claude.json: your own ones under
// mcpServers, a project's under projects[path].mcpServers, and a
// project's shared ones in its .mcp.json. tubas lists them, so /mcp can
// add one in a key; the connector then says `claude: NAME` and tubas
// reads the server from Claude Code each time it starts it, so tokens in
// its env or headers never get copied into team.yaml.

// ClaudeServer is one MCP server Claude Code knows.
type ClaudeServer struct {
	Name    string
	Type    string // stdio, http or sse
	Command string
	Args    []string
	Env     map[string]string
	URL     string
	Headers map[string]string
	From    string // "your settings", "project <name>" or ".mcp.json"
	Dir     string // the project folder, for a project's or a .mcp.json server
}

type claudeServerJSON struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func (s claudeServerJSON) server(name, from, dir string) ClaudeServer {
	kind := s.Type
	if kind == "" {
		kind = "stdio"
		if s.URL != "" {
			kind = "http"
		}
	}
	return ClaudeServer{Name: name, Type: kind, Command: s.Command, Args: s.Args, Env: s.Env,
		URL: s.URL, Headers: s.Headers, From: from, Dir: dir}
}

// claudeJSON is where Claude Code keeps its state, MCP servers included.
func claudeJSON() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

// ClaudeServers are the MCP servers Claude Code has: yours, then the
// projects' in dirs (the folder tubas runs in, the team's), then those
// projects' .mcp.json. The first one of each name wins.
func ClaudeServers(dirs ...string) []ClaudeServer {
	var out []ClaudeServer
	seen := map[string]bool{}
	add := func(name string, s claudeServerJSON, from, dir string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, s.server(name, from, dir))
	}
	sortedAdd := func(servers map[string]claudeServerJSON, from, dir string) {
		names := make([]string, 0, len(servers))
		for n := range servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			add(n, servers[n], from, dir)
		}
	}
	var state struct {
		MCPServers map[string]claudeServerJSON `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]claudeServerJSON `json:"mcpServers"`
		} `json:"projects"`
	}
	if data, err := os.ReadFile(claudeJSON()); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	sortedAdd(state.MCPServers, "your settings", "")
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		abs, _ := filepath.Abs(dir)
		for path, p := range state.Projects {
			if samePath(path, abs) {
				sortedAdd(p.MCPServers, "project "+filepath.Base(abs), abs)
			}
		}
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		var shared struct {
			MCPServers map[string]claudeServerJSON `json:"mcpServers"`
		}
		if data, err := os.ReadFile(filepath.Join(dir, ".mcp.json")); err == nil && json.Unmarshal(data, &shared) == nil {
			abs, _ := filepath.Abs(dir)
			sortedAdd(shared.MCPServers, ".mcp.json", abs)
		}
	}
	// Last, every other project's, so a name added from one folder is
	// still found when tubas starts it from another.
	paths := make([]string, 0, len(state.Projects))
	for path := range state.Projects {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		sortedAdd(state.Projects[path].MCPServers, "project "+filepath.Base(path), path)
	}
	return out
}

// ClaudeServerNamed finds one of Claude Code's MCP servers by name.
func ClaudeServerNamed(name string, dirs ...string) (ClaudeServer, bool) {
	for _, s := range ClaudeServers(dirs...) {
		if s.Name == name {
			return s, true
		}
	}
	return ClaudeServer{}, false
}

// Resolved is the connector ready to start: for `claude: NAME`, the
// server as Claude Code has it now.
func (c Connector) Resolved(dirs ...string) (Connector, error) {
	if c.Claude == "" {
		return c, nil
	}
	if c.ClaudeFrom != "" {
		dirs = append([]string{c.ClaudeFrom}, dirs...)
	}
	s, ok := ClaudeServerNamed(c.Claude, dirs...)
	if !ok {
		return c, fmt.Errorf("%s: no MCP server named %s in your Claude Code settings anymore", c.Name, c.Claude)
	}
	if s.Type == "sse" {
		return c, fmt.Errorf("%s: %s uses the old SSE transport, which tubas doesn't speak", c.Name, c.Claude)
	}
	r := c
	r.Command, r.Args, r.Env, r.URL, r.Headers = s.Command, s.Args, s.Env, s.URL, s.Headers
	return r, nil
}

func samePath(a, b string) bool {
	x, _ := filepath.Abs(a)
	y, _ := filepath.Abs(b)
	return strings.EqualFold(filepath.Clean(x), filepath.Clean(y))
}

// ── adding and removing connectors in team.yaml ─────────────────────────

// AddConnector writes a connector under connectors: in team.yaml, keeping
// the file's comments.
func AddConnector(dir string, c Connector) error {
	if !validName.MatchString(c.Name) {
		return fmt.Errorf("%q: use lowercase letters, digits, - or _ in the name", c.Name)
	}
	if err := checkConnector(c); err != nil {
		return err
	}
	return editTeamYAML(dir, func(root *yaml.Node) error {
		conns := mappingAt(root, "connectors")
		for i := 0; i+1 < len(conns.Content); i += 2 {
			if conns.Content[i].Value == c.Name {
				return fmt.Errorf("the team already has a connector named %s", c.Name)
			}
		}
		var value yaml.Node
		if err := value.Encode(c); err != nil {
			return err
		}
		dropEmpty(&value)
		conns.Content = append(conns.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: c.Name, Tag: "!!str"}, &value)
		return nil
	})
}

// RemoveConnector deletes a connector from team.yaml.
func RemoveConnector(dir, name string) error {
	return editTeamYAML(dir, func(root *yaml.Node) error {
		conns := mappingAt(root, "connectors")
		for i := 0; i+1 < len(conns.Content); i += 2 {
			if conns.Content[i].Value == name {
				conns.Content = append(conns.Content[:i], conns.Content[i+2:]...)
				return nil
			}
		}
		return fmt.Errorf("no connector named %s in team.yaml", name)
	})
}

// mappingAt is root[key] as a mapping, made when missing or empty (a
// `connectors:` with only comments under it is null).
func mappingAt(root *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			v := root.Content[i+1]
			if v.Kind != yaml.MappingNode {
				v.Kind, v.Tag, v.Value, v.Content = yaml.MappingNode, "!!map", "", nil
			}
			return v
		}
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}, v)
	return v
}

// dropEmpty removes keys with empty values from an encoded connector.
func dropEmpty(m *yaml.Node) {
	if m.Kind != yaml.MappingNode {
		return
	}
	var kept []*yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		v := m.Content[i+1]
		empty := (v.Kind == yaml.ScalarNode && (v.Value == "" || v.Tag == "!!null")) ||
			((v.Kind == yaml.MappingNode || v.Kind == yaml.SequenceNode) && len(v.Content) == 0)
		if !empty {
			kept = append(kept, m.Content[i], v)
		}
	}
	m.Content = kept
}

func editTeamYAML(dir string, change func(root *yaml.Node) error) error {
	path := filepath.Join(dir, "team.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("team.yaml: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("team.yaml: expected a mapping at the top")
	}
	if err := change(doc.Content[0]); err != nil {
		return err
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	enc.Close()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
