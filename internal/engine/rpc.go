package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// rpcConn speaks JSON-RPC over a child's stdin and stdout, one message
// per line: requests from us wait for their answer, notifications and
// requests from the child go to the handlers: the tubas gateway (MCP)
// talks this way.
type rpcConn struct {
	w        io.Writer
	version  bool // send "jsonrpc": "2.0"
	onNotify func(method string, params json.RawMessage)
	// onRequest answers a request from the child. It runs in its own
	// goroutine: an answer may wait for you (an approval).
	onRequest func(method string, params json.RawMessage) (any, error)

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int
	pending map[string]chan rpcMsg
	done    chan struct{}
	err     error
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErrorBody   `json:"error,omitempty"`
}

type rpcErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newRPC(r io.Reader, w io.Writer, version bool, onNotify func(string, json.RawMessage),
	onRequest func(string, json.RawMessage) (any, error)) *rpcConn {
	c := &rpcConn{w: w, version: version, onNotify: onNotify, onRequest: onRequest,
		pending: map[string]chan rpcMsg{}, done: make(chan struct{})}
	go c.read(r)
	return c
}

func (c *rpcConn) read(r io.Reader) {
	reader := bufio.NewReader(r)
	defer func() {
		c.mu.Lock()
		if c.err == nil {
			c.err = errors.New("the agent closed the connection")
		}
		c.mu.Unlock()
		close(c.done)
	}()
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m rpcMsg
			if json.Unmarshal(line, &m) == nil {
				c.dispatch(m)
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *rpcConn) dispatch(m rpcMsg) {
	switch {
	case m.Method != "" && len(m.ID) > 0: // a request from the child
		go func() {
			result, err := c.onRequest(m.Method, m.Params)
			reply := rpcMsg{ID: m.ID}
			if err != nil {
				reply.Error = &rpcErrorBody{-32601, err.Error()}
			} else {
				reply.Result, _ = json.Marshal(result)
			}
			c.send(reply)
		}()
	case m.Method != "": // a notification
		if c.onNotify != nil {
			c.onNotify(m.Method, m.Params)
		}
	case len(m.ID) > 0: // an answer to one of ours
		c.mu.Lock()
		ch := c.pending[string(m.ID)]
		delete(c.pending, string(m.ID))
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
}

func (c *rpcConn) send(m rpcMsg) error {
	if c.version {
		m.JSONRPC = "2.0"
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// call sends a request and waits for its answer.
func (c *rpcConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := json.RawMessage(strconv.Itoa(c.nextID))
	ch := make(chan rpcMsg, 1)
	c.pending[string(id)] = ch
	c.mu.Unlock()
	raw, _ := json.Marshal(params)
	if err := c.send(rpcMsg{ID: id, Method: method, Params: raw}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		return m.Result, nil
	case <-c.done:
		return nil, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// notify sends a notification.
func (c *rpcConn) notify(method string, params any) error {
	m := rpcMsg{Method: method}
	if params != nil {
		m.Params, _ = json.Marshal(params)
	}
	return c.send(m)
}
