package team

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The agents `tubas init` made before it started teams with one assistant,
// kept to recognize a personal team nobody changed (see MigratePersonal).
const legacyAna = `---
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

const legacyRui = `---
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

// MigratePersonal turns a personal team that still has the two agents an
// older tubas made (ana and rui, unchanged) into the plain chat new teams
// start with. It changes nothing a person edited. It says whether it did.
func MigratePersonal(dir string) bool {
	agents := filepath.Join(dir, "agents")
	entries, err := os.ReadDir(agents)
	if err != nil || len(entries) != 2 {
		return false
	}
	same := func(name, want string) bool {
		data, err := os.ReadFile(filepath.Join(agents, name))
		return err == nil && strings.ReplaceAll(string(data), "\r\n", "\n") == want
	}
	if !same("ana.md", legacyAna) || !same("rui.md", legacyRui) {
		return false
	}
	name := strings.ToLower(filepath.Base(dir))
	if !validName.MatchString(name) {
		name = "team"
	}
	if os.WriteFile(filepath.Join(agents, "tubas.md"), []byte(assistantTemplate), 0o644) != nil {
		return false
	}
	os.Remove(filepath.Join(agents, "ana.md"))
	os.Remove(filepath.Join(agents, "rui.md"))
	os.WriteFile(filepath.Join(dir, "team.yaml"), []byte(fmt.Sprintf(teamTemplate, name)), 0o644)
	return true
}
