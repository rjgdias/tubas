package mcp

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"tubas/internal/data"
	"tubas/internal/team"
)

// TestMain runs once for the package. When started with TUBAS_FAKE_MCP=1,
// this test binary turns into a fake external MCP server (see fakeServer):
// that way the gateway is tested against a real separate process.
func TestMain(m *testing.M) {
	if os.Getenv("TUBAS_FAKE_MCP") == "1" {
		fakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeServer is a tiny MCP server with one read-only tool and one that
// writes. It also sends a notification first, which the client must skip.
func fakeServer() {
	in := bufio.NewReader(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			} `json:"params"`
		}
		json.Unmarshal(line, &req)
		if len(req.ID) == 0 {
			continue
		}
		enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"level": "info"}})
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "get_weather", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
				{"name": "send_email", "inputSchema": map[string]any{"type": "object", "required": []string{"to"}}},
			}}
		case "tools/call":
			text := "sunny in " + req.Params.Arguments["city"]
			if req.Params.Name == "send_email" {
				text = "sent to " + req.Params.Arguments["to"]
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		}
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

// testTeam builds a team with three connectors (sqlite, reports and the
// fake external server) and two agents: one that reads, one that writes.
func testTeam(t *testing.T) team.Team {
	t.Helper()
	dir := t.TempDir()
	if err := data.Seed(filepath.Join(dir, "x.db")); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	teamYAML := `name: test
connectors:
  data:
    builtin: sqlite
    db: x.db
  reports:
    builtin: reports
  mail:
    command: ` + strconv.Quote(exe) + `
    args: ["-test.run=^$"]
    env: { TUBAS_FAKE_MCP: "1" }
`
	files := map[string]string{
		"team.yaml":        teamYAML,
		"agents/reader.md": "---\nname: reader\ntools: { data: read, reports: read, mail: read, workspace: read, team: none }\n---\nRead.\n",
		"agents/writer.md": "---\nname: writer\ntools: { data: none, reports: write, mail: write }\n---\nWrite.\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tm, err := team.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func gateway(t *testing.T, tm team.Team, agentName, url, token string) *Gateway {
	t.Helper()
	g, err := NewGateway(tm, agentName, url, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

// talk sends requests to the gateway and returns its responses, decoded.
func talk(t *testing.T, g *Gateway, requests ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := g.Serve(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("bad response %q: %v", line, err)
		}
		responses = append(responses, r)
	}
	return responses
}

func testResultText(r map[string]any) (string, bool) {
	res := r["result"].(map[string]any)
	content := res["content"].([]any)[0].(map[string]any)
	isErr, _ := res["isError"].(bool)
	return content["text"].(string), isErr
}

func call(name, args string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

// Read access shows only tools that read, from every kind of connector.
func TestReaderSeesOnlyReadTools(t *testing.T) {
	g := gateway(t, testTeam(t), "reader", "", "")
	want := "data__run_sql,data__list_tables,data__describe_table,mail__get_weather,reports__list_reports,workspace__list_files,workspace__read_file"
	if got := strings.Join(g.ToolNames(), ","); got != want {
		t.Errorf("tools =\n %s\nwant\n %s", got, want)
	}
}

func TestWriterSeesEverythingButNone(t *testing.T) {
	g := gateway(t, testTeam(t), "writer", "", "")
	want := "mail__get_weather,mail__send_email,reports__publish_report,reports__list_reports,workspace__list_files,workspace__read_file,workspace__write_file"
	if got := strings.Join(g.ToolNames(), ","); got != want {
		t.Errorf("tools =\n %s\nwant\n %s", got, want)
	}
}

func TestHandshakeListAndCall(t *testing.T) {
	g := gateway(t, testTeam(t), "reader", "", "")
	rs := talk(t, g,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`, // no answer expected
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		call("data__run_sql", `{"query":"SELECT COUNT(DISTINCT regiao) AS n FROM pedidos"}`),
		call("mail__get_weather", `{"city":"Recife"}`), // forwarded to the external server
	)
	if len(rs) != 4 {
		t.Fatalf("expected 4 responses (the notification gets none), got %d", len(rs))
	}
	if text, _ := testResultText(rs[2]); text != "1 row · n\n5" {
		t.Errorf("run_sql = %q", text)
	}
	if text, _ := testResultText(rs[3]); text != "sunny in Recife" {
		t.Errorf("external tool = %q", text)
	}
}

// A tool the agent was not given cannot be called, even if the model
// knows its name.
func TestToolNotAvailable(t *testing.T) {
	g := gateway(t, testTeam(t), "reader", "", "")
	text, isErr := testResultText(talk(t, g, call("mail__send_email", `{"to":"x"}`))[0])
	if !isErr || !strings.Contains(text, "not available to @reader") {
		t.Errorf("got %q (error=%v)", text, isErr)
	}
}

// Tools that write wait for the human: approved, they run; denied, they
// never reach the server.
func TestWritesWaitForApproval(t *testing.T) {
	for _, approve := range []bool{true, false} {
		var asked map[string]string
		var calls atomic.Int32
		broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Header.Get("Authorization") != "Bearer secret" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			json.NewDecoder(r.Body).Decode(&asked)
			json.NewEncoder(w).Encode(map[string]any{"approved": approve})
		}))
		tm := testTeam(t)
		g := gateway(t, tm, "writer", broker.URL, "secret")

		rs := talk(t, g,
			call("mail__send_email", `{"to":"time@empresa.com","body":"oi"}`),
			call("reports__publish_report", `{"channel":"#team","title":"Queda em março","body":"Caiu."}`),
			call("mail__get_weather", `{"city":"Natal"}`), // reads never ask
		)
		broker.Close()

		mail, mailErr := testResultText(rs[0])
		_, reportErr := testResultText(rs[1])
		files, _ := os.ReadDir(filepath.Join(tm.Dir, "reports"))
		if calls.Load() != 2 {
			t.Errorf("expected 2 approval requests (the read asks none), got %d", calls.Load())
		}
		if asked["agent"] != "writer" || !strings.HasPrefix(asked["call"], "publish_report(#team, Queda") {
			t.Errorf("last approval request = %v", asked)
		}
		if approve && (mailErr || mail != "sent to time@empresa.com" || reportErr || len(files) != 1) {
			t.Errorf("approved: mail %q, report error %v, files %v", mail, reportErr, files)
		}
		if !approve && (!mailErr || !strings.Contains(mail, "do not try again") || !reportErr || len(files) != 0) {
			t.Errorf("denied: mail %q, files %v", mail, files)
		}
	}
}

func TestDescribeFollowsRequiredOrder(t *testing.T) {
	schema := object(map[string]any{}, "channel", "title", "body")
	got := describe("publish_report", schema, json.RawMessage(`{"body":"um texto bem longo que passa de trinta e dois caracteres","title":"Queda","channel":"#dados"}`))
	if got != "publish_report(#dados, Queda, um texto bem longo que passa de…)" {
		t.Errorf("got %q", got)
	}
}
