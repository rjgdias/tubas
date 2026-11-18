// Package thread keeps each agent's side of a conversation: every message
// it was sent and every answer it gave, with the tool calls and results,
// in one append-only file per agent:
//
//	.tubas/conversations/<conversation>/<agent>.jsonl
//
// A follow-up question continues that history instead of starting over
// from a summary. Nothing in the file is ever edited: the models check
// that what came before a thinking block is unchanged, so a history only
// grows, and a long one is compacted by appending a summary that the
// next requests start from.
package thread

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"tubas/internal/llm"
)

// Record is one line of the file.
type Record struct {
	Kind    string      `json:"kind"`           // "message" or "compaction"
	Role    string      `json:"role,omitempty"` // user or assistant
	Blocks  []llm.Block `json:"blocks,omitempty"`
	Summary string      `json:"summary,omitempty"` // a compaction: everything before it, in a few paragraphs
	// Frame is the fingerprint of what the message was sent with: the
	// system prompt, the tools and the model. Thinking blocks stay valid
	// only while it holds.
	Frame string    `json:"frame,omitempty"`
	Input int       `json:"input,omitempty"` // an answer: the tokens the request that got it sent
	At    time.Time `json:"at"`
}

// Thread is one agent's history in one conversation.
type Thread struct {
	path    string
	records []Record
}

var (
	locksMu sync.Mutex
	locks   = map[string]*sync.Mutex{}
	safe    = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// Valid says whether a name can be a conversation's or an agent's
// folder or file: no separators, no way up.
func Valid(name string) bool { return safe.MatchString(name) && !strings.Contains(name, "..") }

// Path is where an agent's history in a conversation lives.
func Path(dir, conversation, agentName string) (string, error) {
	if !Valid(conversation) || !Valid(agentName) {
		return "", fmt.Errorf("not a conversation or agent name: %q, %q", conversation, agentName)
	}
	return filepath.Join(dir, conversation, agentName+".jsonl"), nil
}

// Open locks the agent's history in this conversation and reads it. Call
// the returned function when the turn ends: one turn at a time writes it.
func Open(dir, conversation, agentName string) (*Thread, func(), error) {
	path, err := Path(dir, conversation, agentName)
	if err != nil {
		return nil, nil, err
	}
	locksMu.Lock()
	mu, ok := locks[path]
	if !ok {
		mu = &sync.Mutex{}
		locks[path] = mu
	}
	locksMu.Unlock()
	mu.Lock()
	t := &Thread{path: path}
	if err := t.load(); err != nil {
		mu.Unlock()
		return nil, nil, err
	}
	return t, mu.Unlock, nil
}

// load reads the file. A last line cut by a crash is left out; a turn
// that stopped between a tool call and its result gets the result it
// never had, appended, so the history is whole again.
func (t *Thread) load() error {
	data, err := os.ReadFile(t.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Kind != "" {
			t.records = append(t.records, r)
		}
	}
	if !bytes.HasSuffix(data, []byte("\n")) && len(data) > 0 {
		// a write cut half-way: end the line, so the next record starts clean
		if f, err := os.OpenFile(t.path, os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			f.Write([]byte("\n"))
			f.Close()
		}
	}
	if open := t.unanswered(); len(open) > 0 {
		fix := Record{Kind: "message", Role: "user", At: time.Now()}
		for _, id := range open {
			fix.Blocks = append(fix.Blocks, llm.Block{Type: "tool_result", ID: id, Content: "(not run: the turn stopped before this call)", IsError: true})
		}
		return t.Append(fix)
	}
	return nil
}

// unanswered are the calls of the last answer that have no result.
func (t *Thread) unanswered() []string {
	for i := len(t.records) - 1; i >= 0; i-- {
		r := t.records[i]
		if r.Kind != "message" {
			return nil
		}
		if r.Role == "user" {
			return nil
		}
		var ids []string
		for _, b := range r.Blocks {
			if b.Type == "tool_use" {
				ids = append(ids, b.ID)
			}
		}
		return ids
	}
	return nil
}

// Empty says whether the agent has said nothing in this conversation yet.
func (t *Thread) Empty() bool { return len(t.records) == 0 }

// LastAt is when the agent's history last grew.
func (t *Thread) LastAt() time.Time {
	if len(t.records) == 0 {
		return time.Time{}
	}
	return t.records[len(t.records)-1].At
}

// Append writes records at the end of the file.
func (t *Thread) Append(recs ...Record) error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	now := time.Now()
	for i := range recs {
		if recs[i].At.IsZero() {
			recs[i].At = now
		}
	}
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	f, err := os.OpenFile(t.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	t.records = append(t.records, recs...)
	return nil
}

// Add appends one message, sent under frame.
func (t *Thread) Add(m llm.Message, frame string) error {
	return t.Append(Record{Kind: "message", Role: m.Role, Blocks: m.Blocks, Frame: frame})
}

// AddAnswer appends the model's answer and how big the request was.
func (t *Thread) AddAnswer(m llm.Message, frame string, input int) error {
	return t.Append(Record{Kind: "message", Role: m.Role, Blocks: m.Blocks, Frame: frame, Input: input})
}

// LastInput is how big the latest request was, since the last
// compaction: what says it's time for the next one.
func (t *Thread) LastInput() int {
	for i := len(t.records) - 1; i >= 0; i-- {
		switch r := t.records[i]; {
		case r.Kind == "compaction":
			return 0
		case r.Input > 0:
			return r.Input
		}
	}
	return 0
}

// Compact appends a summary: the next requests start from it.
func (t *Thread) Compact(summary string) error {
	return t.Append(Record{Kind: "compaction", Summary: summary})
}

// Messages is the history to send under frame: from the latest
// compaction on, its summary first. Thinking blocks written under another
// frame (another system prompt, other tools, another model) are left
// out, oldest first, as the models allow: they would be refused.
func (t *Thread) Messages(frame string) []llm.Message {
	start := 0
	summary := ""
	for i, r := range t.records {
		if r.Kind == "compaction" {
			start, summary = i+1, r.Summary
		}
	}
	recs := t.records[start:]
	stale := -1 // thinking up to here goes
	for i, r := range recs {
		if r.Kind == "message" && r.Role == "assistant" && r.Frame != frame {
			stale = i
		}
	}
	var out []llm.Message
	if summary != "" {
		out = append(out, llm.UserText(SummaryIntro+summary))
	}
	for i, r := range recs {
		if r.Kind != "message" || len(r.Blocks) == 0 {
			continue
		}
		blocks := r.Blocks
		if i <= stale {
			blocks = withoutThinking(blocks)
			if len(blocks) == 0 {
				continue
			}
		}
		if n := len(out); n > 0 && out[n-1].Role == r.Role {
			// two in a row (a turn that stopped before its answer): one message
			out[n-1].Blocks = append(append([]llm.Block{}, out[n-1].Blocks...), blocks...)
			continue
		}
		out = append(out, llm.Message{Role: r.Role, Blocks: blocks})
	}
	return out
}

// SummaryIntro starts the message that carries a compaction's summary.
const SummaryIntro = "This conversation was long, so what happened before is summarized here; continue from it.\n\n"

func withoutThinking(blocks []llm.Block) []llm.Block {
	var out []llm.Block
	for _, b := range blocks {
		if b.Type != "thinking" && b.Type != "redacted_thinking" {
			out = append(out, b)
		}
	}
	return out
}

// Frame fingerprints what thinking blocks are tied to.
func Frame(model, system string, tools []llm.Tool) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", model, system)
	for _, tl := range tools {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", tl.Name, tl.Description, tl.Schema)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// CompactPrompt asks for the summary a compaction keeps. The last line
// matters: the request still carries the tools, and a model sometimes
// calls one instead of writing.
const CompactPrompt = `The conversation is getting long, so it will be compacted: the next requests will see only your summary of it, nothing else.

Write that summary now. Keep everything needed to carry on:
- what the human asked, in their words where it matters, and what they decided or corrected;
- what you found and concluded, with the facts, numbers, names, paths and commands that support it;
- what you changed (files, worktrees, reports) and what is left to do, with the next step;
- anything you were told not to do, or that failed and shouldn't be tried again.

Write it as notes to yourself, in the human's language. Do not call any tools: answer with the summary only.`
