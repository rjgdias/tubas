// Package team reads a team: a folder with a team.yaml and an agents/
// folder.
//
//	my-team/
//	  team.yaml        name, example question, limits, connectors
//	  agents/*.md      one file per agent
//
// Connectors are where tools come from. Each agent's tools field names
// connectors and an access level: none, read or write.
package team

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"tubas/internal/agent"
	"tubas/internal/data"
)

// Connector is one source of tools. It is either built into tubas
// (builtin: sqlite or reports) or any MCP server tubas starts (command).
type Connector struct {
	Name string `yaml:"-"`

	// Built-in connectors.
	Builtin string `yaml:"builtin"` // "sqlite", "reports" or "workspace"
	DB      string `yaml:"db"`      // sqlite: the database file
	Seed    string `yaml:"seed"`    // sqlite: "delivery" creates the example data if the file is missing
	Dir     string `yaml:"dir"`     // reports: where reports go (default "reports")
	Channel string `yaml:"channel"` // reports: the channel to use when the agent names none

	// A git repository agents work on: each task gets its own worktree.
	Repo  string `yaml:"repo"`  // the repository folder
	Base  string `yaml:"base"`  // the branch tasks start from (default: the one checked out)
	Setup string `yaml:"setup"` // runs in every new worktree, like "uv sync"

	// Any MCP server that speaks stdio.
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"` // ${VAR} is replaced from your environment

	// Or one over HTTP (MCP's streamable HTTP).
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"` // ${VAR} is replaced from your environment

	// Or one from your Claude Code settings, by name: tubas reads it from
	// there each time, so its tokens stay in Claude Code's file.
	Claude     string `yaml:"claude"`
	ClaudeFrom string `yaml:"claude_from"` // the project folder it came from, when not your own settings

	// Override how tools are classified. By default a tool is read-only
	// when the server marks it so (readOnlyHint); every other tool writes
	// and waits for your approval.
	Read  []string `yaml:"read"`
	Write []string `yaml:"write"`
}

// Kind is "sqlite", "reports" or "mcp".
func (c Connector) Kind() string {
	if c.Builtin != "" {
		return c.Builtin
	}
	if c.Repo != "" {
		return "repo"
	}
	return "mcp"
}

// Runs says what runs an MCP connector, for screens: the command, the
// URL, or "claude: playwright".
func (c Connector) Runs() string {
	switch {
	case c.Claude != "":
		return "from Claude Code: " + c.Claude
	case c.URL != "":
		return c.URL
	case c.Command != "":
		return strings.TrimSpace(c.Command + " " + strings.Join(c.Args, " "))
	}
	return c.Builtin
}

// ProviderConfig is a model provider from team.yaml.
type ProviderConfig struct {
	Anthropic string   `yaml:"anthropic"` // an Anthropic-compatible base URL
	OpenAI    string   `yaml:"openai"`    // an OpenAI-compatible base URL
	KeyEnv    string   `yaml:"key_env"`   // the environment variable holding the key
	Models    []string `yaml:"models"`
	// Prices, per model, in US$ per million tokens: [input, cached input,
	// output]. A turn's cost is shown with them.
	Prices map[string][3]float64 `yaml:"prices"`
}

// HarnessConfig is an agent CLI an older team.yaml listed: read, ignored.
type HarnessConfig struct {
	ACP string `yaml:"acp"` // the command that starts it in ACP mode, like "dsh --profile acp"
}

// Team is everything in a team folder.
type Team struct {
	Dir        string               `yaml:"-"`
	Name       string               `yaml:"name"`
	Example    string               `yaml:"example"`
	Lead       string               `yaml:"lead"`    // who answers when nobody else is picked
	Routing    string               `yaml:"routing"` // questions that name no agent: lead (default), router or all
	MaxHops    int                  `yaml:"max_hops"`
	Notify     string               `yaml:"notify"` // all (default), window or off: see Notifications in the README
	Connectors map[string]Connector `yaml:"connectors"`
	// AllowCommands are shell commands agents run without asking you:
	// prefixes like "go test" or "npm run lint". Anything chained after
	// them (; && | $( ) still asks.
	AllowCommands []string `yaml:"allow_commands"`

	// Provider is where agents' models are served when their file says
	// nothing: anthropic (your Claude setup, the default), deepseek, glm…
	Provider string `yaml:"provider"`
	// Providers adds model providers to the built-in ones. Keys are named
	// by their environment variable, never written here.
	Providers map[string]ProviderConfig `yaml:"providers"`
	// Harnesses is read and ignored: tubas runs every agent itself.
	Harnesses map[string]HarnessConfig `yaml:"harnesses"`
	Agents    []agent.Agent            `yaml:"-"`
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

// Load reads and checks a team folder.
func Load(dir string) (Team, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Team{}, err
	}
	raw, err := os.ReadFile(filepath.Join(abs, "team.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return Team{}, fmt.Errorf("no team.yaml in %s (try: tubas -team examples/dados)", abs)
	}
	if err != nil {
		return Team{}, err
	}

	var t Team
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return Team{}, fmt.Errorf("team.yaml: %w", err)
	}
	t.Dir = abs
	if t.Name == "" {
		t.Name = filepath.Base(abs)
	}
	if t.MaxHops == 0 {
		t.MaxHops = 6
	}
	switch t.Routing {
	case "":
		t.Routing = "lead"
	case "lead", "router", "all":
	default:
		return Team{}, fmt.Errorf("team.yaml: routing must be lead, router or all")
	}
	if t.MaxHops < 1 || t.MaxHops > 50 {
		return Team{}, fmt.Errorf("team.yaml: max_hops must be between 1 and 50")
	}
	// Every team has a workspace connector: each agent's own folder and a
	// shared one. Agents get it with write access unless their file says
	// otherwise (tools: { workspace: read } or none).
	if t.Connectors == nil {
		t.Connectors = map[string]Connector{}
	}
	if _, ok := t.Connectors["workspace"]; !ok {
		t.Connectors["workspace"] = Connector{Builtin: "workspace"}
	}
	// Two more come with every team: "team", through which an agent (the
	// lead, by default) proposes new agents and routines, each waiting for
	// you; and "skills", which loads the skills an agent was given.
	for _, name := range []string{"team", "skills"} {
		if _, ok := t.Connectors[name]; !ok {
			t.Connectors[name] = Connector{Builtin: name}
		}
	}
	for name, c := range t.Connectors {
		c.Name = name
		if err := checkConnector(c); err != nil {
			return Team{}, fmt.Errorf("team.yaml: connector %s: %w", name, err)
		}
		t.Connectors[name] = c
	}

	if t.Agents, err = agent.LoadTeam(filepath.Join(abs, "agents")); err != nil {
		return Team{}, err
	}
	if t.Lead == "" {
		t.Lead = t.Agents[0].Name // LoadTeam fails on an empty folder, so there is one
	} else if _, ok := t.Agent(t.Lead); !ok {
		return Team{}, fmt.Errorf("team.yaml: lead %q is not an agent of the team", t.Lead)
	}
	for i := range t.Agents {
		a := &t.Agents[i]
		if _, ok := a.Tools["workspace"]; !ok {
			a.Tools["workspace"] = "write"
		}
		// The lead of a team may propose agents; you still approve each
		// one. A lone assistant is a plain chat: it doesn't build teams
		// unless its file asks for it (tools: { team: write }).
		if _, ok := a.Tools["team"]; !ok && a.Name == t.Lead && len(t.Agents) > 1 {
			a.Tools["team"] = "write"
		}
		if _, ok := a.Tools["skills"]; !ok && len(a.Skills) > 0 {
			a.Tools["skills"] = "read"
		}
	}
	for _, a := range t.Agents {
		for tool := range a.Tools {
			if _, ok := t.Connectors[tool]; !ok {
				return Team{}, fmt.Errorf("agent %s uses %q, but team.yaml has no connector with that name", a.Name, tool)
			}
		}
	}
	return t, nil
}

func checkConnector(c Connector) error {
	if !validName.MatchString(c.Name) {
		return errors.New("use lowercase letters, digits, - or _ in the name")
	}
	sources := 0
	for _, set := range []bool{c.Builtin != "", c.Command != "", c.URL != "", c.Claude != "", c.Repo != ""} {
		if set {
			sources++
		}
	}
	switch {
	case sources > 1:
		return errors.New("use one of builtin:, command:, url:, claude: or repo:")
	case c.Repo != "":
		if c.Builtin != "" || c.Command != "" {
			return errors.New("a repo connector has only repo:, base: and setup:")
		}
	case c.Builtin == "sqlite":
		if c.DB == "" {
			return errors.New("sqlite needs db: path/to/file.db")
		}
		if c.Seed != "" && c.Seed != "delivery" {
			return fmt.Errorf("unknown seed %q (the only one is delivery)", c.Seed)
		}
	case c.Builtin == "reports", c.Builtin == "workspace", c.Builtin == "team", c.Builtin == "skills":
	case c.Builtin != "":
		return fmt.Errorf("unknown builtin %q (use sqlite, reports or workspace)", c.Builtin)
	case c.Command == "" && c.URL == "" && c.Claude == "":
		return errors.New("needs builtin:, command:, url: or claude:")
	}
	return nil
}

// Repos are the team's repository connectors, by name, with absolute paths.
func (t Team) Repos() map[string]RepoSpec {
	out := map[string]RepoSpec{}
	for name, c := range t.Connectors {
		if c.Repo != "" {
			out[name] = RepoSpec{Path: t.Path(c.Repo), Base: c.Base, Setup: c.Setup}
		}
	}
	return out
}

// RepoSpec is where a repository is and how a task starts on it.
type RepoSpec struct{ Path, Base, Setup string }

// Path resolves a path from team.yaml against the team folder.
func (t Team) Path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(t.Dir, p)
}

// ReportsDir is where a reports connector writes.
func (t Team) ReportsDir(c Connector) string {
	if c.Dir == "" {
		return t.Path("reports")
	}
	return t.Path(c.Dir)
}

// WorkspacesDir holds one folder per agent; SharedDir is the team's.
func (t Team) WorkspacesDir() string { return filepath.Join(t.Dir, ".tubas", "workspaces") }

func (t Team) SharedDir() string { return filepath.Join(t.Dir, ".tubas", "shared") }

// Agent finds an agent by name.
func (t Team) Agent(name string) (agent.Agent, bool) {
	for _, a := range t.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return agent.Agent{}, false
}

// ConnectorNames lists the connectors, sorted.
func (t Team) ConnectorNames() []string {
	names := make([]string, 0, len(t.Connectors))
	for n := range t.Connectors {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Prepare creates what the connectors need before the first run, like
// the example database. force recreates it.
func (t Team) Prepare(force bool) error {
	for _, name := range t.ConnectorNames() {
		c := t.Connectors[name]
		if c.Builtin != "sqlite" || c.Seed == "" {
			continue
		}
		path := t.Path(c.DB)
		if _, err := os.Stat(path); err == nil && !force {
			continue
		}
		if err := data.Seed(path); err != nil {
			return fmt.Errorf("creating %s: %w", path, err)
		}
	}
	return nil
}
