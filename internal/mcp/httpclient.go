package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"tubas/internal/team"
)

// httpConn speaks MCP's streamable HTTP: every message is a POST; the
// answer comes as JSON or as a short event stream; the server may give a
// session id, sent back on every request after.
type httpConn struct {
	name    string
	url     string
	headers map[string]string
	session string
	proto   string // the protocol version, sent after the handshake
	client  *http.Client
}

func newHTTPConn(c team.Connector) *httpConn {
	headers := map[string]string{}
	for k, v := range c.Headers {
		headers[k] = os.ExpandEnv(v) // ${TOKEN} comes from your environment
	}
	return &httpConn{name: c.Name, url: c.URL, headers: headers, client: &http.Client{Timeout: 5 * time.Minute}}
}

// call posts one message. With an id, it waits for the answer to it; a
// notification (id "") only needs the server to accept it.
func (h *httpConn) call(msg any, id string) (json.RawMessage, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", h.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}
	if h.proto != "" {
		req.Header.Set("MCP-Protocol-Version", h.proto)
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", h.name, err)
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.session = sid
	}
	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("%s answered %s: %s", h.name, resp.Status, strings.TrimSpace(string(snippet)))
	}
	if id == "" {
		io.Copy(io.Discard, resp.Body)
		return nil, nil
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return h.fromStream(resp.Body, id)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return h.answer(data, id)
}

// fromStream reads server-sent events until the answer with id comes.
func (h *httpConn) fromStream(r io.Reader, id string) (json.RawMessage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case line == "" && data.Len() > 0: // the end of one event
			if result, err := h.answer([]byte(data.String()), id); result != nil || err != nil {
				return result, err
			}
			data.Reset()
		}
	}
	if data.Len() > 0 {
		if result, err := h.answer([]byte(data.String()), id); result != nil || err != nil {
			return result, err
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", h.name, err)
	}
	return nil, fmt.Errorf("%s: the answer never came", h.name)
}

// answer finds the reply to id in a message (or a batch of them).
func (h *httpConn) answer(data []byte, id string) (json.RawMessage, error) {
	var msgs []message
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &msgs); err != nil {
			return nil, fmt.Errorf("%s: unreadable answer: %w", h.name, err)
		}
	} else {
		var m message
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return nil, fmt.Errorf("%s: unreadable answer: %w", h.name, err)
		}
		msgs = []message{m}
	}
	for _, m := range msgs {
		if string(m.ID) != id {
			continue // a notification or a request from the server
		}
		if m.Error != nil {
			return nil, fmt.Errorf("%s: %s", h.name, m.Error.Message)
		}
		if m.Result == nil {
			return json.RawMessage(`{}`), nil
		}
		return m.Result, nil
	}
	return nil, nil
}
