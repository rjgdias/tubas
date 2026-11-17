package team

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Init creates a new team folder with a team.yaml and two agents to
// start from: one who does the work and one who reviews it.
func Init(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "team.yaml")); err == nil {
		return fmt.Errorf("%s already has a team.yaml", dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	name := strings.ToLower(filepath.Base(abs))
	if !validName.MatchString(name) {
		name = "team"
	}

	files := map[string]string{
		"team.yaml":     fmt.Sprintf(teamTemplate, name, name),
		"agents/ana.md": anaTemplate,
		"agents/rui.md": ruiTemplate,
	}
	for path, content := range files {
		full := filepath.Join(abs, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(full); err == nil {
			return errors.New(full + " already exists")
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

const teamTemplate = `# Your team. Run it with:  tubas -team <this folder>

name: %s
example: "what should we look at first?"
lead: ana      # questions that mention no one go to her
max_hops: 6

connectors:
  # Publishing a markdown report: a write, so it waits for your yes.
  reports:
    builtin: reports
    dir: reports
    channel: "#%s"

  # Add tools from any MCP server that runs locally, for example:
  #
  # files:
  #   command: npx
  #   args: ["-y", "@modelcontextprotocol/server-filesystem", "."]
  #   read: [read_text_file, list_directory, search_files]
  #
  # Or a SQLite database, read-only:
  #
  # db:
  #   builtin: sqlite
  #   db: data/my.db
  #
  # Then give an agent access in its file, e.g.  tools: { files: read }
`

const anaTemplate = `---
name: ana
role: researcher
color: orange
harness: claude
model: sonnet
tools:
  reports: read
can_call: [rui]
token_limit: 50000
---
You are Ana, the team's researcher.

Understand the question, use your tools to find what you need, and answer
with a clear conclusion and the evidence behind it.

When you have a conclusion worth sharing, ask @rui to review it.
Answer in the language the human used.
`

const ruiTemplate = `---
name: rui
role: reviewer
color: blue
harness: claude
model: sonnet
tools:
  reports: write
can_call: [ana]
token_limit: 40000
---
You are Rui, the team's reviewer.

Check that every claim has evidence and that nothing important is missing.
If something is weak, send it back to @ana saying exactly what to redo.
If it holds, publish a short report (that waits for human approval).
Answer in the language the human used.
`
