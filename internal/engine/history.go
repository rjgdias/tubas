package engine

import (
	"path/filepath"
	"sync"
	"time"

	"tubas/internal/llm"
	"tubas/internal/thread"
)

// history is a turn's conversation with the model: the agent's thread
// when the session belongs to a conversation (it outlives the turn), else
// only this turn's messages.
type history struct {
	th    *thread.Thread
	frame string
	msgs  []llm.Message
	input int
}

// openHistory opens the agent's thread, locked for the turn.
func openHistory(req TurnRequest, frame string) (*history, func(), error) {
	if req.Threads == "" || req.Conversation == "" {
		return &history{frame: frame}, func() {}, nil
	}
	th, unlock, err := thread.Open(filepath.Clean(req.Threads), req.Conversation, req.Agent.Name)
	if err != nil {
		return nil, nil, err
	}
	return &history{th: th, frame: frame}, unlock, nil
}

// continues says whether the agent has spoken in this conversation.
func (h *history) continues() bool { return h.th != nil && !h.th.Empty() }

func (h *history) lastAt() time.Time {
	if h.th == nil {
		return time.Time{}
	}
	return h.th.LastAt()
}

func (h *history) lastInput() int {
	if h.th == nil {
		return h.input
	}
	return h.th.LastInput()
}

func (h *history) add(m llm.Message) error {
	if h.th == nil {
		h.msgs = append(h.msgs, m)
		return nil
	}
	return h.th.Add(m, h.frame)
}

func (h *history) addAnswer(m llm.Message, input int) error {
	if h.th == nil {
		h.msgs, h.input = append(h.msgs, m), input
		return nil
	}
	return h.th.AddAnswer(m, h.frame, input)
}

func (h *history) messages() []llm.Message {
	if h.th == nil {
		return append([]llm.Message{}, h.msgs...)
	}
	return h.th.Messages(h.frame)
}

func (h *history) compact(summary string) error {
	if h.th == nil {
		h.msgs, h.input = []llm.Message{llm.UserText(thread.SummaryIntro + summary)}, 0
		return nil
	}
	return h.th.Compact(summary)
}

// compactAt is how big a request may get before the conversation is
// compacted: the agent's token_limit, else most of the model's context,
// up to 150K (past that, models get slower and dearer for little gain).
func compactAt(limit int, caps llm.Caps) int {
	if limit > 0 {
		return limit
	}
	return min(150_000, caps.Context*3/4)
}

// siteApprovals are the web sites you allowed, by conversation: every
// agent and turn of it reads them without asking again.
var siteApprovals sync.Map // threads folder + "|" + conversation → *sync.Map
