package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// header is the agent file's YAML header, in the order people read it.
// omitempty keeps files short: only what differs from the defaults.
type header struct {
	Name       string            `yaml:"name"`
	Role       string            `yaml:"role,omitempty"`
	Color      string            `yaml:"color,omitempty"`
	Provider   string            `yaml:"provider,omitempty"`
	Model      string            `yaml:"model,omitempty"`
	Effort     string            `yaml:"effort,omitempty"`
	Tools      map[string]string `yaml:"tools,omitempty"`
	CanCall    []string          `yaml:"can_call,omitempty,flow"`
	TokenLimit int               `yaml:"token_limit,omitempty"`
	Skills     []string          `yaml:"skills,omitempty,flow"`
	Escalate   *Escalation       `yaml:"escalate,omitempty"`
}

// Markdown is the whole agent file: the header and the instructions.
func (a Agent) Markdown() string {
	h := header{a.Name, a.Role, a.Color, a.Provider, a.Model, a.Effort, nil, a.CanCall, a.TokenLimit, a.Skills, a.Escalate}
	if h.TokenLimit == 50000 {
		h.TokenLimit = 0
	}
	if len(a.Tools) > 0 {
		h.Tools = map[string]string{}
		for k, v := range a.Tools {
			if v != "none" { // none is the same as not listed
				h.Tools[k] = v
			}
		}
	}
	data, _ := yaml.Marshal(h)
	return "---\n" + string(data) + "---\n\n" + strings.TrimSpace(a.Instructions) + "\n"
}

// Save writes the agent's file in dir (agents/<name>.md). It checks the
// agent first, and writes through a temporary file, so a crash never
// leaves half a file. Renaming an agent removes its old file.
func Save(dir string, a Agent) (Agent, error) {
	applyDefaults(&a)
	if err := validate(a); err != nil {
		return a, err
	}
	path := filepath.Join(dir, a.Name+".md")
	if err := writeAtomic(path, a.Markdown()); err != nil {
		return a, err
	}
	if a.File != "" && !samePath(a.File, path) {
		_ = os.Remove(a.File)
	}
	a.File = path
	return a, nil
}

func samePath(a, b string) bool {
	x, err1 := filepath.Abs(a)
	y, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && strings.EqualFold(x, y)
}

func writeAtomic(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		var link *os.LinkError
		if errors.As(err, &link) {
			err = link.Err // the reason; the paths are long and say little
		}
		return fmt.Errorf("saving %s: %w", filepath.Base(path), err)
	}
	return nil
}

// Template is a starting point for a new agent.
type Template struct {
	Key, About   string
	Role         string
	Model        string
	Effort       string
	Access       string // the access it gets on every data connector
	Instructions string
}

// Templates are the starting points shown when you create an agent.
var Templates = []Template{
	{Key: "blank", About: "you write everything", Model: "sonnet",
		Instructions: "Describe how this agent works: what it answers, how, and when it hands off."},
	{Key: "analyst", About: "answers with numbers from the data and shows the query behind each one",
		Role: "data analyst", Model: "sonnet", Effort: "medium", Access: "read",
		Instructions: "You are a data analyst.\n\n- Answer with numbers from the data, never from memory.\n- Show the query behind each number.\n- If a number looks odd, ask the engineer to check the load before concluding.\n- End with one sentence: what changed and why."},
	{Key: "engineer", About: "checks loads, jobs and data quality before anyone blames the business",
		Role: "data engineer", Model: "sonnet", Effort: "low", Access: "read",
		Instructions: "You are a data engineer.\n\nWhen called, check whether a problem comes from the data rather than the business:\n- late or failed loads in the period;\n- tables with duplicated or missing rows;\n- schema or definition changes.\n\nAnswer in a few lines: is the data reliable or not, and why."},
	{Key: "reviewer", About: "checks numbers and claims, sends weak work back, publishes the rest",
		Role: "reviewer", Model: "sonnet", Effort: "medium", Access: "read",
		Instructions: "You are the reviewer.\n\nCheck every number and claim you are handed against the data. If something doesn't hold, hand it back with what is wrong. If it holds, say so in one line."},
	{Key: "watcher", About: "made for routines: looks at a channel or a job and speaks only when needed",
		Role: "watcher", Model: "haiku", Effort: "low", Access: "read",
		Instructions: "You watch for one thing on a schedule.\n\n- Look only at what is new since the last run.\n- If nothing matters, answer PASS.\n- If something does, say it in two lines, with where you saw it."},
}
