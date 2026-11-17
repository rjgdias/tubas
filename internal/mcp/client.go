package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"time"

	"tubas/internal/team"
)

// client talks to one external MCP server: the gateway is the server for
// claude and, at the same time, a client for the team's connectors.
type client struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex // one request at a time
	nextID int
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// startClient starts the connector's command and does the MCP handshake.
func startClient(c team.Connector, dir string) (*client, error) {
	cmd := exec.Command(c.Command, c.Args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+os.ExpandEnv(v)) // ${GITHUB_TOKEN} comes from your environment
	}
	cmd.Stderr = os.Stderr // the server's logs go where claude collects ours
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", c.Command, err)
	}
	cl := &client{name: c.Name, cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}

	_, err = cl.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tubas", "version": "0.2.0"},
	})
	if err != nil {
		cl.close()
		return nil, fmt.Errorf("handshake with %s: %w", c.Name, err)
	}
	if err := cl.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		cl.close()
		return nil, err
	}
	return cl, nil
}

// request sends one request and waits for its answer. Messages the server
// sends meanwhile (logs, pings) are handled or skipped.
func (cl *client) request(method string, params any) (json.RawMessage, error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.nextID++
	id := strconv.Itoa(cl.nextID)
	if err := cl.write(map[string]any{"jsonrpc": "2.0", "id": cl.nextID, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		line, err := cl.stdout.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var msg message
			if json.Unmarshal(line, &msg) != nil {
				continue // not JSON-RPC: ignore
			}
			if msg.Method != "" { // a request or notification from the server
				if len(msg.ID) > 0 {
					cl.answerServer(msg)
				}
				continue
			}
			if string(msg.ID) == id {
				if msg.Error != nil {
					return nil, fmt.Errorf("%s: %s", cl.name, msg.Error.Message)
				}
				return msg.Result, nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s closed: %w", cl.name, err)
		}
	}
}

// answerServer replies to a request the server made to us.
func (cl *client) answerServer(msg message) {
	reply := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
	if msg.Method == "ping" {
		reply["result"] = map[string]any{}
	} else {
		reply["error"] = rpcError{-32601, "tubas does not support " + msg.Method}
	}
	cl.write(reply)
}

func (cl *client) write(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = cl.stdin.Write(append(line, '\n'))
	return err
}

// tools lists the server's tools and decides which ones only read.
func (cl *client) tools(c team.Connector) ([]tool, error) {
	var out []tool
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := cl.request("tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
				Annotations struct {
					ReadOnlyHint bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		for _, t := range page.Tools {
			name := t.Name
			readOnly := t.Annotations.ReadOnlyHint
			if slices.Contains(c.Read, name) {
				readOnly = true
			}
			if slices.Contains(c.Write, name) {
				readOnly = false
			}
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			out = append(out, tool{
				name: name, description: t.Description, schema: schema, readOnly: readOnly,
				call: func(args json.RawMessage) (any, error) {
					if len(args) == 0 {
						args = json.RawMessage(`{}`)
					}
					return cl.request("tools/call", map[string]any{"name": name, "arguments": args})
				},
			})
		}
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// close stops the server: close its stdin, give it a moment, then kill.
func (cl *client) close() {
	cl.stdin.Close()
	done := make(chan struct{})
	go func() { cl.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cl.cmd.Process.Kill()
	}
}
