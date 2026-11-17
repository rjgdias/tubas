// Package mcp is the tool gateway tubas puts between its agents and their
// tools.
//
// MCP (Model Context Protocol) is how a harness like Claude Code learns
// about tools that live outside it. Claude starts `tubas mcp` as a child
// process and talks JSON-RPC 2.0 with it over stdin/stdout, one JSON
// object per line:
//
//	→ {"jsonrpc":"2.0","id":1,"method":"tools/list"}
//	← {"jsonrpc":"2.0","id":1,"result":{"tools":[...]}}
//	→ {"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"data__run_sql","arguments":{...}}}
//	← {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"..."}]}}
//
// The gateway is the agent's only source of tools. It gathers them from
// the team's connectors (built-in ones, and any MCP server it starts),
// shows the agent only what its access level allows, and stops every tool
// that writes until a human approves it. Permissions live here, in code,
// not in the prompt.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"tubas/internal/data"
)

// tool is one tool the gateway can offer.
type tool struct {
	name        string
	description string
	schema      any  // JSON Schema of the arguments
	readOnly    bool // false: it writes, so it waits for approval...
	noApproval  bool // ...unless it only writes in the agent's own space
	call        func(args json.RawMessage) (result any, err error)
}

// textTool wraps a function that returns text into the MCP result shape.
func textTool(name, description string, schema any, readOnly bool, run func(args map[string]any) (string, error)) tool {
	return tool{name: name, description: description, schema: schema, readOnly: readOnly,
		call: func(raw json.RawMessage) (any, error) {
			args := map[string]any{}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return toolResult("bad arguments: "+err.Error(), true), nil
				}
			}
			out, err := run(args)
			if err != nil {
				return toolResult(err.Error(), true), nil
			}
			return toolResult(out, false), nil
		}}
}

// toolResult is the MCP shape for a tool's answer. Errors are results
// too (isError), so the model reads them and can try something else.
func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}

// ── built-in: sqlite ────────────────────────────────────────────────────

func sqliteTools(db string) []tool {
	// The schema goes in the description: the agent knows the tables from
	// its first step, and the text never changes, so it stays in the cache.
	description := "Run one read-only SQL query (SQLite) and get the rows back. Prefer aggregates (GROUP BY) to raw rows."
	if schema, err := data.Schema(db); err == nil && schema != "" {
		description += "\nTables:\n" + schema
	}
	return []tool{
		textTool("run_sql", description,
			object(map[string]any{"query": str("A single SELECT or WITH ... SELECT statement")}, "query"),
			true,
			func(args map[string]any) (string, error) { return data.Query(db, text(args, "query"), 30) }),
		textTool("list_tables", "List the tables in the database (they are also in run_sql's description).", object(map[string]any{}), true,
			func(map[string]any) (string, error) { return data.Tables(db) }),
		textTool("describe_table", "List the columns of one table.",
			object(map[string]any{"table": str("Table name")}, "table"), true,
			func(args map[string]any) (string, error) { return data.Describe(db, text(args, "table")) }),
	}
}

// ── built-in: reports ───────────────────────────────────────────────────

func reportTools(dir, author, channel string) []tool {
	if channel == "" {
		channel = "#general"
	}
	return []tool{
		textTool("publish_report",
			"Publish a markdown report to a channel. A human must approve it first; if denied, do not try again.",
			object(map[string]any{
				"channel": str("Channel to publish in; use " + channel + " unless asked otherwise"),
				"title":   str("Short title"),
				"body":    str("The report, in markdown"),
			}, "channel", "title", "body"),
			false, // writes: the gateway asks for approval before this runs
			func(args map[string]any) (string, error) {
				ch := text(args, "channel")
				if ch == "" {
					ch = channel
				}
				return publishReport(dir, author, ch, text(args, "title"), text(args, "body"))
			}),
		textTool("list_reports", "List the reports already published.", object(map[string]any{}), true,
			func(map[string]any) (string, error) { return listReports(dir) }),
	}
}

func publishReport(dir, author, channel, title, body string) (string, error) {
	if title == "" || body == "" {
		return "", errors.New("title and body are required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	now := time.Now()
	name := now.Format("2006-01-02-1504") + "-" + slug(title) + ".md"
	content := fmt.Sprintf("---\ntitle: %s\nchannel: %s\nauthor: %s\npublished: %s\n---\n\n%s\n",
		title, channel, author, now.Format(time.RFC3339), body)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		return "", err
	}
	return "report published in " + channel + " · " + filepath.ToSlash(filepath.Join(filepath.Base(dir), name)), nil
}

func listReports(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "no reports yet", nil
	}
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "no reports yet", nil
	}
	sort.Strings(names)
	return fmt.Sprintf("%d reports\n%s", len(names), strings.Join(names, "\n")), nil
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	replacer := strings.NewReplacer("á", "a", "à", "a", "ã", "a", "â", "a", "é", "e", "ê", "e",
		"í", "i", "ó", "o", "õ", "o", "ô", "o", "ú", "u", "ç", "c")
	s = strings.Trim(nonWord.ReplaceAllString(replacer.Replace(strings.ToLower(s)), "-"), "-")
	if len(s) > 50 {
		s = strings.TrimRight(s[:50], "-")
	}
	if s == "" {
		s = "report"
	}
	return s
}

// ── small helpers for JSON Schema and arguments ─────────────────────────

func object(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func text(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}
