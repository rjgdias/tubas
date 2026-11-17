package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"tubas/internal/team"
)

// ServerName is how claude prefixes the gateway's tools: mcp__tubas__...
const ServerName = "tubas"

// Gateway serves one agent's tools for one turn.
type Gateway struct {
	agent         string
	turn          string // which turn this gateway serves, for the approval broker
	approvalURL   string
	approvalToken string
	tools         []tool // already filtered by access and named connector__tool
	clients       []*client
	senior        *senior // nil unless the agent escalates
}

// NewGateway gathers the agent's tools from the team's connectors.
// A connector that fails to start is skipped (and logged), so one broken
// server does not take the whole agent down.
func NewGateway(t team.Team, agentName, approvalURL, approvalToken string) (*Gateway, error) {
	a, ok := t.Agent(agentName)
	if !ok {
		return nil, fmt.Errorf("no agent named %s", agentName)
	}
	g := &Gateway{agent: agentName, turn: os.Getenv("TUBAS_TURN"), approvalURL: approvalURL, approvalToken: approvalToken}

	names := make([]string, 0, len(a.Tools))
	for n := range a.Tools {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, connName := range names {
		access := a.Tools[connName]
		if access == "none" {
			continue
		}
		c := t.Connectors[connName]
		var tools []tool
		switch c.Kind() {
		case "sqlite":
			tools = sqliteTools(t.Path(c.DB))
		case "reports":
			tools = reportTools(t.ReportsDir(c), agentName, c.Channel)
		case "workspace":
			tools = workspaceTools(t.WorkspacesDir(), t.SharedDir(), agentName)
		case "team":
			tools = teamTools(t)
		case "skills":
			tools = skillTools(t, a)
		case "repo":
			continue // claude's own file tools work there (see permission.go)
		default:
			cl, err := startClient(c, t.Dir)
			if err == nil {
				tools, err = cl.tools(c)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "tubas: skipping connector %s: %v\n", connName, err)
				continue
			}
			g.clients = append(g.clients, cl)
		}
		for _, tl := range tools {
			if access == "read" && !tl.readOnly {
				continue // read access: tools that write are not even listed
			}
			tl.name = connName + "__" + tl.name
			g.tools = append(g.tools, tl)
		}
	}
	if dirs := os.Getenv("TUBAS_WRITE_DIRS"); dirs != "" {
		// claude asks tubas before its own shell and edits (permission);
		// other harnesses run with their shell off and get run_command.
		if h := os.Getenv("TUBAS_HARNESS"); h == "" || h == "claude" {
			g.tools = append(g.tools, g.permissionTool(filepath.SplitList(dirs)))
		} else {
			tl := g.shellTool(filepath.SplitList(dirs))
			tl.name = "code__" + tl.name
			g.tools = append(g.tools, tl)
		}
	}
	// The api harness has no file tools of its own: the gateway's.
	if os.Getenv("TUBAS_HARNESS") == "api" {
		var read, write []string
		if v := os.Getenv("TUBAS_READ_DIRS"); v != "" {
			read = filepath.SplitList(v)
		}
		if v := os.Getenv("TUBAS_WRITE_DIRS"); v != "" {
			write = filepath.SplitList(v)
		}
		if len(read)+len(write) > 0 {
			for _, tl := range codeTools(read, write) {
				tl.name = "code__" + tl.name
				g.tools = append(g.tools, tl)
			}
		}
	}
	if g.senior = newSenior(a, t.Dir); g.senior != nil && g.senior.cfg.Has("ask") {
		tl := g.senior.askTool()
		tl.name = "escalate__" + tl.name
		g.tools = append(g.tools, tl)
	}
	return g, nil
}

// ToolNames lists what the agent can see, for tests and debugging.
func (g *Gateway) ToolNames() []string {
	names := make([]string, len(g.tools))
	for i, t := range g.tools {
		names[i] = t.name
	}
	return names
}

// Close stops the external servers.
func (g *Gateway) Close() {
	for _, cl := range g.clients {
		cl.close()
	}
}

// ── the JSON-RPC loop, as a server for claude ───────────────────────────

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // missing for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve answers requests from in until it closes.
func (g *Gateway) Serve(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	enc := json.NewEncoder(out) // Encode writes one JSON object plus "\n"
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var req request
			if jerr := json.Unmarshal(line, &req); jerr != nil {
				enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			} else if resp, ok := g.handle(req); ok {
				if werr := enc.Encode(resp); werr != nil {
					return werr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// handle answers one request. ok is false for notifications, which get
// no answer.
func (g *Gateway) handle(req request) (response, bool) {
	if len(req.ID) == 0 {
		return response{}, false // e.g. notifications/initialized
	}
	resp := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		resp.Result = map[string]any{
			"protocolVersion": p.ProtocolVersion, // speak whatever version the client asked for
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": ServerName, "version": "0.2.0"},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		list := make([]map[string]any, 0, len(g.tools))
		for _, t := range g.tools {
			list = append(list, map[string]any{
				"name": t.name, "description": t.description, "inputSchema": t.schema,
				"annotations": map[string]any{"readOnlyHint": t.readOnly},
			})
		}
		resp.Result = map[string]any{"tools": list}
	case "tools/call":
		resp.Result = g.call(req.Params)
	default:
		resp.Error = &rpcError{-32601, "method not found: " + req.Method}
	}
	return resp, true
}

func (g *Gateway) call(params json.RawMessage) any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return toolResult("bad request: "+err.Error(), true)
	}
	for _, t := range g.tools {
		if t.name != p.Name {
			continue
		}
		short := p.Name
		if i := strings.Index(p.Name, "__"); i >= 0 {
			short = p.Name[i+2:]
		}
		desc := describe(short, t.schema, p.Arguments)
		if !t.readOnly && !t.noApproval {
			// The tool writes: nothing runs until a human says yes.
			detail := prettyArgs(p.Arguments)
			if g.senior != nil && g.senior.cfg.Has("before_write") {
				detail += "\n\n" + g.senior.ask("Review this write before a human approves it. Is it right? "+
					"Start with OK or CONCERN, then say why in a line.\n"+p.Name+"\n"+detail)
			}
			approved, reason, err := g.askApproval(desc, detail)
			if err != nil {
				return toolResult("could not ask for approval: "+err.Error(), true)
			}
			if !approved {
				if reason == "" {
					reason = "denied by the human reviewer"
				}
				return toolResult(reason+": do not try again", true)
			}
		}
		result, err := t.call(p.Arguments)
		if err != nil {
			result = toolResult(err.Error(), true)
		}
		if g.senior != nil && !strings.HasPrefix(p.Name, "escalate__") {
			text, failed := resultText(result)
			g.senior.note(desc, text, failed)
			if failed && g.senior.failed(p.Name) {
				result = withNote(result, g.senior.ask("The tool "+p.Name+" failed twice in this task. Last error:\n"+clip(text, 1500)))
			}
		}
		return result
	}
	return toolResult(fmt.Sprintf("%s is not available to @%s", p.Name, g.agent), true)
}

// askApproval asks the TUI (through the broker) and waits for the answer.
func (g *Gateway) askApproval(call, detail string) (approved bool, reason string, err error) {
	if g.approvalURL == "" {
		return false, "no one is there to approve", nil
	}
	body, _ := json.Marshal(map[string]string{"agent": g.agent, "turn": g.turn, "call": call, "detail": detail})
	req, err := http.NewRequest(http.MethodPost, g.approvalURL, bytes.NewReader(body))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Authorization", "Bearer "+g.approvalToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req) // no timeout: a human may take a while
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("broker answered %s", resp.Status)
	}
	var answer struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return false, "", err
	}
	return answer.Approved, answer.Reason, nil
}

// describe turns a call into "publish_report(#team, Title…)", taking the
// arguments in the order the schema lists them as required.
func describe(name string, schema any, raw json.RawMessage) string {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil || len(args) == 0 {
		return name + "()"
	}
	var order []string
	var s struct {
		Required []string `json:"required"`
	}
	if b, err := json.Marshal(schema); err == nil && json.Unmarshal(b, &s) == nil {
		order = s.Required
	}
	seen := map[string]bool{}
	for _, k := range order {
		seen[k] = true
	}
	var rest []string
	for k := range args {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	var parts []string
	for _, k := range append(order, rest...) {
		v, ok := args[k]
		if !ok {
			continue
		}
		text := strings.Join(strings.Fields(fmt.Sprint(v)), " ")
		if r := []rune(text); len(r) > 32 {
			text = strings.TrimSpace(string(r[:32])) + "…"
		}
		parts = append(parts, text)
	}
	return name + "(" + strings.Join(parts, ", ") + ")"
}
