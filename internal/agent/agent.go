// Package agent reads an agent definition from a Markdown file: a YAML
// header (between --- lines) with the settings, followed by free-form
// instructions.
//
// Remember: an agent is not a program. It is just a configuration that
// the harness uses to run the model.
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Agent is the configuration of one agent.
//
// The struct tags between backticks (`yaml:"name"`) tell the yaml package
// which key in the file goes into which field. `yaml:"-"` means "not read
// from YAML".
type Agent struct {
	Name       string            `yaml:"name"`
	Role       string            `yaml:"role"`
	Color      string            `yaml:"color"`
	Harness    string            `yaml:"harness"`  // what runs it: claude, codex, api, or an ACP agent (opencode, gemini…)
	Provider   string            `yaml:"provider"` // where the model is served: anthropic (default), deepseek, glm, kimi, qwen…
	Model      string            `yaml:"model"`
	Effort     string            `yaml:"effort"` // how hard the model thinks: low, medium, high ("" = its default)
	Tools      map[string]string `yaml:"tools"`
	CanCall    []string          `yaml:"can_call"`
	TokenLimit int               `yaml:"token_limit"`
	Skills     []string          `yaml:"skills"`   // skills this agent may load (see .tubas/skills)
	Escalate   *Escalation       `yaml:"escalate"` // a stronger model for the hard step; nil = off

	Instructions string `yaml:"-"` // the text after the header
	File         string `yaml:"-"` // which file it came from
}

// Escalation lets an agent on a cheap model ask a stronger one for the
// hard step, then carry on. The tool gateway does it: it sees every call.
type Escalation struct {
	To     string   `yaml:"to"`      // the stronger model: sonnet, opus or a model id
	On     []string `yaml:"on"`      // tool_fails, before_write, ask (empty = all three)
	PerDay int      `yaml:"per_day"` // at most this many a day (0 = 5)
}

// Triggers are the escalation triggers in effect.
func (e Escalation) Triggers() []string {
	if len(e.On) == 0 {
		return []string{"tool_fails", "before_write", "ask"}
	}
	return e.On
}

// Has says whether a trigger is on.
func (e Escalation) Has(trigger string) bool {
	for _, t := range e.Triggers() {
		if t == trigger {
			return true
		}
	}
	return false
}

// Limit is the daily cap.
func (e Escalation) Limit() int {
	if e.PerDay <= 0 {
		return 5
	}
	return e.PerDay
}

// Accepted values. A map with struct{} values is the Go way to build a
// "set": all that matters is whether the key exists.
var (
	validName     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,30}$`) // harness and provider names
	validAccess   = map[string]struct{}{"none": {}, "read": {}, "write": {}}
	validEffort   = map[string]struct{}{"": {}, "low": {}, "medium": {}, "high": {}, "xhigh": {}, "max": {}}
	validTriggers = map[string]struct{}{"tool_fails": {}, "before_write": {}, "ask": {}}
	validColors   = map[string]struct{}{
		"orange": {}, "blue": {}, "purple": {}, "green": {}, "pink": {}, "cyan": {},
	}
)

// Parse reads the content of an agent file.
func Parse(content string) (Agent, error) {
	var a Agent

	// Files saved on Windows end lines with \r\n. Normalizing to \n keeps
	// the "---" lines recognizable.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimPrefix(content, string(rune(0xFEFF))) // BOM added by some editors

	if !strings.HasPrefix(content, "---\n") {
		return a, errors.New("the file must start with a --- line")
	}
	rest := content[len("---\n"):]

	end := strings.Index(rest, "\n---")
	if end < 0 {
		return a, errors.New("missing the --- line that closes the header")
	}
	header := rest[:end]
	body := rest[end+len("\n---"):]

	if err := yaml.Unmarshal([]byte(header), &a); err != nil {
		return a, fmt.Errorf("invalid YAML header: %w", err)
	}
	a.Instructions = strings.TrimSpace(body)

	applyDefaults(&a)
	return a, validate(a)
}

// applyDefaults fills empty fields with sensible values.
func applyDefaults(a *Agent) {
	if a.Harness == "" {
		a.Harness = "claude"
	}
	if a.Model == "" {
		a.Model = "sonnet"
	}
	if a.Color == "" {
		a.Color = "cyan"
	}
	if a.TokenLimit == 0 {
		a.TokenLimit = 50000
	}
	if a.Tools == nil {
		a.Tools = map[string]string{}
	}
}

// validate checks a single agent. Rules that depend on the other agents
// (like can_call) live in LoadTeam.
func validate(a Agent) error {
	if a.Name == "" {
		return errors.New("missing the name field")
	}
	if strings.ContainsAny(a.Name, " @") || a.Name != strings.ToLower(a.Name) {
		return fmt.Errorf("invalid name %q: use lowercase only, no spaces or @", a.Name)
	}
	if a.Instructions == "" {
		return fmt.Errorf("agent %s has no instructions after the header", a.Name)
	}
	// Which harnesses and providers exist depends on this computer and the
	// team, so the engine checks that when the agent runs.
	if !validName.MatchString(a.Harness) {
		return fmt.Errorf("agent %s: harness %q (use claude, codex, api, or an ACP agent like opencode)", a.Name, a.Harness)
	}
	if a.Provider != "" && !validName.MatchString(a.Provider) {
		return fmt.Errorf("agent %s: provider %q (use lowercase, like deepseek)", a.Name, a.Provider)
	}
	if _, ok := validColors[a.Color]; !ok {
		return fmt.Errorf("agent %s: unknown color %q", a.Name, a.Color)
	}
	if _, ok := validEffort[a.Effort]; !ok {
		return fmt.Errorf("agent %s: effort %q (use low, medium, high, xhigh or max)", a.Name, a.Effort)
	}
	for tool, access := range a.Tools {
		if _, ok := validAccess[access]; !ok {
			return fmt.Errorf("agent %s: invalid access %q for %s (use none, read or write)", a.Name, access, tool)
		}
	}
	if a.TokenLimit < 0 {
		return fmt.Errorf("agent %s: token_limit cannot be negative", a.Name)
	}
	if e := a.Escalate; e != nil {
		if e.To == "" {
			return fmt.Errorf("agent %s: escalate needs to: (sonnet, opus or a model id)", a.Name)
		}
		for _, t := range e.On {
			if _, ok := validTriggers[t]; !ok {
				return fmt.Errorf("agent %s: unknown escalate trigger %q (use tool_fails, before_write or ask)", a.Name, t)
			}
		}
	}
	return nil
}

// LoadTeam reads every .md file in a folder and checks the rules that
// involve more than one agent.
func LoadTeam(dir string) ([]Agent, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .md files in %s", dir)
	}
	sort.Strings(files) // stable order on every OS

	var team []Agent
	byName := map[string]bool{}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		a, err := Parse(string(data))
		if err != nil {
			// %w wraps the original error, keeping the cause.
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if byName[a.Name] {
			return nil, fmt.Errorf("%s: an agent named %s already exists", filepath.Base(path), a.Name)
		}
		byName[a.Name] = true
		a.File = path
		team = append(team, a)
	}

	// Only now, with everyone loaded, can we check can_call.
	for _, a := range team {
		for _, other := range a.CanCall {
			if other == a.Name {
				return nil, fmt.Errorf("agent %s cannot call itself", a.Name)
			}
			if !byName[other] {
				return nil, fmt.Errorf("agent %s can call %q, but that agent does not exist", a.Name, other)
			}
		}
	}
	return team, nil
}

// ToolsString returns something like "data=read, slack=write".
func (a Agent) ToolsString() string {
	if len(a.Tools) == 0 {
		return "none"
	}
	names := make([]string, 0, len(a.Tools))
	for name := range a.Tools {
		names = append(names, name)
	}
	sort.Strings(names) // Go maps have no order; sort to keep output stable
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name + "=" + a.Tools[name]
	}
	return strings.Join(parts, ", ")
}
