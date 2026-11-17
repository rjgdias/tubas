package team

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Init creates a new team folder with a team.yaml and one assistant: a
// plain chat to start from. Teammates come when you add them.
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
		"team.yaml":       fmt.Sprintf(teamTemplate, name),
		"agents/tubas.md": assistantTemplate,
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

const teamTemplate = `# Your tubas. Run it with:  tubas -team <this folder>
#
# It starts as a plain chat with one assistant. Change its model with
# /model; add teammates with /agents (then they hand work to each other).

name: %s
lead: tubas
max_hops: 6

connectors:
  # Give the assistant tools from any MCP server, a repository or a
  # database, then access in its file, e.g.  tools: { files: read }
  #
  # files:
  #   command: npx
  #   args: ["-y", "@modelcontextprotocol/server-filesystem", "."]
  #   read: [read_text_file, list_directory, search_files]
  #
  # api:
  #   repo: /path/to/your/repository
  #
  # db:
  #   builtin: sqlite
  #   db: data/my.db
`

const assistantTemplate = `---
name: tubas
role: assistant
color: cyan
model: sonnet
token_limit: 80000
---
You are a helpful assistant in a terminal chat.

Answer clearly and directly, in the language the person writes in. Use
markdown when it helps (lists, code blocks, tables), and keep answers as
short as the question allows.
`
