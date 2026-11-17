package mcp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"tubas/internal/agent"
	"tubas/internal/routine"
	"tubas/internal/skill"
	"tubas/internal/team"
)

// ── built-in: team ──────────────────────────────────────────────────────
//
// Through the team connector an agent (the lead, by default) proposes new
// agents and routines. Proposing writes files, so each one waits for your
// y in the inbox like any other write. What it can never do: give team
// access to the agents it creates (they can't create agents), or change
// an agent that already exists.

func teamTools(t team.Team) []tool {
	agentsDir := filepath.Join(t.Dir, "agents")
	connectors := strings.Join(t.ConnectorNames(), ", ")
	return []tool{
		textTool("list_team", "The team's agents (name, role, model, access) and its routines (name, agent, schedule).",
			object(map[string]any{}), true,
			func(map[string]any) (string, error) { return listTeam(t) }),
		textTool("propose_agent",
			"Propose a new agent for the team. A human reviews it before anything is created. "+
				"model: haiku (cheap, watching, triage), sonnet (everyday) or opus (hard digging). "+
				"access: connector -> none, read or write; connectors: "+connectors+". "+
				"instructions: how the agent works, in a few short lines.",
			object(map[string]any{
				"name":         str("lowercase, no spaces: how people call it, @name"),
				"role":         str("a few words, e.g. watcher"),
				"model":        str("haiku, sonnet or opus"),
				"effort":       str("low, medium or high (optional)"),
				"access":       map[string]any{"type": "object", "description": "connector -> none, read or write", "additionalProperties": map[string]any{"type": "string"}},
				"can_call":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "teammates it may hand work to"},
				"instructions": str("how it works"),
				"why":          str("one line: why the team needs it"),
			}, "name", "role", "model", "instructions"), false,
			func(args map[string]any) (string, error) { return proposeAgent(t, agentsDir, args) }),
		textTool("propose_routine",
			"Propose a routine: an agent working on its own on a schedule. A human reviews it first. "+
				`every: "1h", "30m", "daily 09:30", "weekdays 08:00", "mon,wed 10:00" or "cron: 0 8 * * 1-5". `+
				`between: a window for intervals, like "08:00-20:00". deliver: chat or report.`,
			object(map[string]any{
				"name":    str("lowercase-with-dashes"),
				"agent":   str("who runs it (an existing agent, or one proposed in the same answer)"),
				"every":   str("the schedule"),
				"between": str("optional window for intervals"),
				"prompt":  str("what the agent is asked on every run"),
				"deliver": str("chat or report"),
				"why":     str("one line: why"),
			}, "name", "agent", "every", "prompt"), false,
			func(args map[string]any) (string, error) { return proposeRoutine(t, args) }),
	}
}

func listTeam(t team.Team) (string, error) {
	var b strings.Builder
	b.WriteString("Agents:\n")
	for _, a := range t.Agents {
		fmt.Fprintf(&b, "- %s (%s) · %s · %s\n", a.Name, a.Role, a.Model, a.ToolsString())
	}
	f, err := routine.Load(t.Dir)
	if err != nil {
		return "", err
	}
	b.WriteString("Routines:\n")
	if len(f.Routines) == 0 {
		b.WriteString("- none\n")
	}
	for _, r := range f.Routines {
		state := "on"
		if !r.On() {
			state = "off"
		}
		fmt.Fprintf(&b, "- %s · %s · %s · %s\n", r.Name, r.Agent, r.Every, state)
	}
	return b.String(), nil
}

func proposeAgent(t team.Team, dir string, args map[string]any) (string, error) {
	a := agent.Agent{
		Name: text(args, "name"), Role: text(args, "role"), Model: text(args, "model"),
		Effort: text(args, "effort"), Instructions: text(args, "instructions"),
		Tools: map[string]string{},
	}
	if _, exists := t.Agent(a.Name); exists {
		return "", fmt.Errorf("an agent named %s already exists: propose another name", a.Name)
	}
	if access, ok := args["access"].(map[string]any); ok {
		for conn, v := range access {
			level := fmt.Sprint(v)
			if conn == "team" && level != "none" {
				return "", fmt.Errorf("new agents can't get team access: only a human gives that")
			}
			if _, ok := t.Connectors[conn]; !ok {
				return "", fmt.Errorf("there is no connector named %s (use %s)", conn, strings.Join(t.ConnectorNames(), ", "))
			}
			a.Tools[conn] = level
		}
	}
	if calls, ok := args["can_call"].([]any); ok {
		for _, c := range calls {
			name := strings.TrimPrefix(fmt.Sprint(c), "@")
			if _, ok := t.Agent(name); !ok {
				return "", fmt.Errorf("can_call: no agent named %s", name)
			}
			a.CanCall = append(a.CanCall, name)
		}
	}
	a.Color = freeColor(t)
	saved, err := agent.Save(dir, a)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("created @%s in agents/%s: it can work from the next question", saved.Name, filepath.Base(saved.File)), nil
}

// freeColor picks a color no one has yet, so the new agent stands out.
func freeColor(t team.Team) string {
	used := map[string]bool{}
	for _, a := range t.Agents {
		used[a.Color] = true
	}
	for _, c := range []string{"green", "pink", "cyan", "orange", "blue", "purple"} {
		if !used[c] {
			return c
		}
	}
	return "cyan"
}

func proposeRoutine(t team.Team, args map[string]any) (string, error) {
	r := routine.Routine{Name: text(args, "name"), Agent: strings.TrimPrefix(text(args, "agent"), "@"),
		Every: text(args, "every"), Between: text(args, "between"), Prompt: text(args, "prompt"), Deliver: text(args, "deliver")}
	f, err := routine.Load(t.Dir)
	if err != nil {
		return "", err
	}
	if _, i := f.Find(r.Name); i >= 0 {
		return "", fmt.Errorf("a routine named %s already exists", r.Name)
	}
	// The agent may have been created a moment ago, in this same turn.
	if _, ok := t.Agent(r.Agent); !ok && !fileExists(filepath.Join(t.Dir, "agents", r.Agent+".md")) {
		return "", fmt.Errorf("no agent named %s", r.Agent)
	}
	f.Routines = append(f.Routines, r)
	if err := routine.Save(t.Dir, f); err != nil {
		return "", err
	}
	s, _ := routine.Parse(r.Every, r.Between)
	return fmt.Sprintf("created the routine %s: %s runs it %s", r.Name, r.Agent, s), nil
}

// ── built-in: skills ────────────────────────────────────────────────────
//
// The prompt only carries each skill's name and when to use it (in this
// tool's description); the steps load when the agent asks. A skill
// costs a line until it is used.

func skillTools(t team.Team, a agent.Agent) []tool {
	all, _ := skill.LoadAll(t.Dir) // a broken skill file is skipped
	mine := map[string]skill.Skill{}
	var list []string
	for _, s := range all {
		if s.Archived {
			continue
		}
		for _, n := range a.Skills {
			if n == s.Name {
				mine[s.Name] = s
				list = append(list, s.Name+": "+s.Description)
			}
		}
	}
	if len(mine) == 0 {
		return nil
	}
	sort.Strings(list)
	return []tool{
		textTool("load_skill",
			"Load one of your skills and follow its steps. Before a task, check if one fits. Your skills:\n- "+strings.Join(list, "\n- "),
			object(map[string]any{"name": str("the skill's name")}, "name"), true,
			func(args map[string]any) (string, error) {
				s, ok := mine[text(args, "name")]
				if !ok {
					return "", fmt.Errorf("you have no skill named %s", text(args, "name"))
				}
				out := s.Body
				if len(s.Files) > 0 {
					out += "\n\nFiles (read_skill_file): " + strings.Join(s.Files, ", ")
				}
				return out, nil
			}),
		textTool("read_skill_file", "Read a file of one of your skills, when its steps point to it.",
			object(map[string]any{"name": str("the skill"), "path": str("the file, as listed")}, "name", "path"), true,
			func(args map[string]any) (string, error) {
				s, ok := mine[text(args, "name")]
				if !ok {
					return "", fmt.Errorf("you have no skill named %s", text(args, "name"))
				}
				path := text(args, "path")
				for _, f := range s.Files {
					if f == path {
						return readCapped(filepath.Join(s.Dir, filepath.FromSlash(f)), 16000)
					}
				}
				return "", fmt.Errorf("%s has no file %s", s.Name, path)
			}),
	}
}

// prettyArgs is the whole call, one argument per line, for the inbox.
func prettyArgs(raw json.RawMessage) string {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil || len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := args[k]
		var s string
		switch v := v.(type) {
		case string:
			s = v
		default:
			j, _ := json.Marshal(v)
			s = string(j)
		}
		fmt.Fprintf(&b, "%s: %s\n", k, s)
	}
	return strings.TrimRight(b.String(), "\n")
}
