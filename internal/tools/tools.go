// Package tools is the native harness's own tools: reading, writing and
// editing files, finding them, searching them, a shell, a checklist and
// fetching a web page. They run inside tubas, in the turn that calls
// them: they keep the shell's folder between calls, remember which files
// the agent read, stop with the turn and ask you directly.
//
// Every tool stays inside the agent's folders: it reads the repositories
// it may read, and changes only its own worktree. Every shell command
// waits for your yes, except the ones team.yaml allows.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Asker asks you to approve a call. call is the one-line form the inbox
// shows, detail the whole of it.
type Asker func(ctx context.Context, call, detail string) (approved bool, reason string, err error)

// Todo is one item of the agent's checklist.
type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress or completed
}

// Box is one turn's tools and their state.
type Box struct {
	Read  []string // folders it may read (the worktrees first)
	Write []string // worktrees it may change; the shell runs in the first
	Ask   Asker    // nil: nothing that needs a yes can run
	Allow []string // command prefixes that need no yes (team.yaml allow_commands)

	// Review is a senior model's look at a command before you approve it
	// (escalation before_write); nil when there is none.
	Review func(ctx context.Context, prompt string) string
	// Todos receives the checklist each time the agent changes it.
	Todos func([]Todo)
	// Domains are the web sites you allowed this conversation, shared
	// between its turns; nil keeps them for this turn only.
	Domains *sync.Map

	mu    sync.Mutex
	cwd   string              // the shell's folder, kept between calls
	seen  map[string]fileMark // files read this turn, as they were then
	todos []Todo
}

type fileMark struct {
	size int64
	mod  time.Time
}

// Tool is one tool as the model sees it.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	ReadOnly    bool // changes nothing: may run alongside others
	run         func(ctx context.Context, b *Box, input json.RawMessage) (string, error)
}

// Output limits, in characters: a shell's output is mostly noise past
// its end, a file is worth more.
const (
	maxOutput      = 60000
	maxShellOutput = 30000
)

// Tools lists what this box offers: files and search where it has
// folders, the shell where it has a worktree, and always the checklist
// and the web.
func (b *Box) Tools() []Tool {
	var list []Tool
	if len(b.Read)+len(b.Write) > 0 {
		list = append(list, readTool, globTool, grepTool)
	}
	if len(b.Write) > 0 {
		list = append(list, writeTool, editTool, bashTool)
	}
	return append(list, todoTool, fetchTool)
}

// Has says whether name is one of this box's tools.
func (b *Box) Has(name string) bool {
	for _, t := range b.Tools() {
		if t.Name == name {
			return true
		}
	}
	return false
}

// ReadOnly says whether the tool changes nothing.
func (b *Box) ReadOnly(name string) bool {
	for _, t := range b.Tools() {
		if t.Name == name {
			return t.ReadOnly
		}
	}
	return false
}

// Call runs a tool. A failure is an answer for the model, not an error:
// it reads what went wrong and tries something else.
func (b *Box) Call(ctx context.Context, name string, input json.RawMessage) (output string, failed bool) {
	for _, t := range b.Tools() {
		if t.Name != name {
			continue
		}
		limit := maxOutput
		if name == "bash" {
			limit = maxShellOutput
		}
		out, err := t.run(ctx, b, input)
		if err != nil {
			if ctx.Err() != nil {
				return "stopped: the turn was cancelled", true
			}
			return clip(err.Error(), limit), true
		}
		return clip(out, limit), false
	}
	return "no tool named " + name, true
}

// Prompt tells the agent where it works and with what.
func (b *Box) Prompt(names map[string]string) string {
	if len(b.Read)+len(b.Write) == 0 {
		return ""
	}
	var lines []string
	for _, dir := range b.Write {
		lines = append(lines, fmt.Sprintf("- %s: your own worktree at %s. Change files there with edit and write: a human reviews the whole diff at the end. "+
			"Every bash command waits for a human, so run only what you need (tests, linters, a build). "+
			"Never commit, push, switch branches or touch files outside it.", orName(names[dir], dir), dir))
	}
	for _, dir := range b.Read {
		if !contains(b.Write, dir) {
			lines = append(lines, fmt.Sprintf("- %s (read only): %s. Read and search it; never change it.", orName(names[dir], dir), dir))
		}
	}
	tools := "read, glob and grep"
	shell := ""
	if len(b.Write) > 0 {
		tools = "read, glob, grep, edit, write and bash"
		shell = " bash runs commands in bash."
		if _, kind := Shell(); kind == "powershell" {
			shell = " bash runs commands in Windows PowerShell 5.1: chain them with ; (there's no &&)."
		}
	}
	return "\n\n## Code\n\nYou work on these repositories with your tools (" + tools + "):\n" + strings.Join(lines, "\n") + "\n" +
		"Relative paths start at " + b.home() + ". Read a file before you edit or replace it." + shell + "\n"
}

// home is where relative paths start: the worktree, else the first folder.
func (b *Box) home() string {
	if len(b.Write) > 0 {
		return b.Write[0]
	}
	if len(b.Read) > 0 {
		return b.Read[0]
	}
	return "."
}

// all is every folder the box may read.
func (b *Box) all() []string {
	out := append([]string{}, b.Write...)
	for _, d := range b.Read {
		if !contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// remember notes a file as the agent saw it, so a write can tell whether
// it changed since.
func (b *Box) remember(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seen == nil {
		b.seen = map[string]fileMark{}
	}
	if info, err := os.Stat(path); err == nil {
		b.seen[path] = fileMark{info.Size(), info.ModTime()}
	}
}

// checkSeen refuses to change a file the agent hasn't read, or that
// changed after it did.
func (b *Box) checkSeen(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil // a new file
	}
	if err != nil {
		return err
	}
	b.mu.Lock()
	mark, ok := b.seen[path]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("read %s first: a file is changed only after you've seen it", path)
	}
	if mark.size != info.Size() || !mark.mod.Equal(info.ModTime()) {
		return fmt.Errorf("%s changed since you read it: read it again", path)
	}
	return nil
}

func decode(input json.RawMessage, v any) error {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(input, v); err != nil {
		return fmt.Errorf("the arguments aren't valid JSON for this tool: %v", err)
	}
	return nil
}

// schema writes a JSON schema for an object.
func schema(props map[string]any, required ...string) json.RawMessage {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		sort.Strings(required)
		s["required"] = required
	}
	data, _ := json.Marshal(s)
	return data
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

// clip keeps the start and the end of a long text.
func clip(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	head := limit * 2 / 3
	tail := limit - head
	return string(r[:head]) + fmt.Sprintf("\n… (%d characters cut) …\n", len(r)-limit) + string(r[len(r)-tail:])
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func orName(name, dir string) string {
	if name != "" {
		return name
	}
	return dir
}
