package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tubas/internal/team"
)

// fakeHTTPMCP is an MCP server over streamable HTTP: it wants a bearer
// token, gives a session id at the handshake, answers tools/list as an
// event stream and tools/call as JSON.
func fakeHTTPMCP(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &msg)
		if msg.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "sess-1" {
			http.Error(w, "no session", http.StatusBadRequest)
			return
		}
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{}}}`, msg.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tools\":[{\"name\":\"search\",\"annotations\":{\"readOnlyHint\":true}},{\"name\":\"deploy\"}]}}\n\n", msg.ID)
		case "tools/call":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"found 3"}]}}`, msg.ID)
		}
	}))
}

func TestAnMCPServerOverHTTP(t *testing.T) {
	srv := fakeHTTPMCP(t)
	defer srv.Close()
	t.Setenv("DDL_TOKEN", "good")
	c := team.Connector{Name: "ddl", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer ${DDL_TOKEN}"}}
	cl, err := startClient(c, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cl.tools(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || !tools[0].readOnly || tools[1].readOnly {
		t.Fatalf("tools = %+v", tools)
	}
	out, err := tools[0].call(json.RawMessage(`{"q":"x"}`))
	if err != nil || !strings.Contains(fmt.Sprint(string(out.(json.RawMessage))), "found 3") {
		t.Errorf("call = %v, %v", out, err)
	}

	bad := team.Connector{Name: "ddl", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer wrong"}}
	if _, err := startClient(bad, t.TempDir()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a refused token says so: %v", err)
	}
}
