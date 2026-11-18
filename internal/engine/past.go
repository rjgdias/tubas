package engine

import (
	"fmt"
	"strings"
	"time"
)

// Exchange is one earlier question of the conversation and how it ended.
type Exchange struct {
	Target   string    `json:"target,omitempty"` // the agent it was addressed to; "" for the team
	Question string    `json:"question"`
	Agent    string    `json:"agent,omitempty"` // who gave the final answer
	Answer   string    `json:"answer,omitempty"`
	Ended    time.Time `json:"ended"`
}

// pastLimit caps each earlier answer an agent is reminded of.
const pastLimit = 1200

// PastContext is the earlier questions and answers as one text, for an
// agent that hasn't spoken in the conversation yet (and for the router).
func PastContext(past []Exchange) string {
	if len(past) == 0 {
		return ""
	}
	if len(past) > 5 {
		past = past[len(past)-5:]
	}
	return "Earlier in this conversation (for context):\n" + exchanges(past)
}

// pastDigest is what happened in the conversation since the agent last
// spoke: the questions it wasn't part of, and how they ended. Its own
// answers it already has, in its history.
func pastDigest(past []Exchange, agentName string, since time.Time) string {
	var news []Exchange
	for _, x := range past {
		if !x.Ended.Before(since) && x.Agent != agentName { // the same instant counts: clocks are coarse
			news = append(news, x)
		}
	}
	if len(news) == 0 {
		return ""
	}
	if len(news) > 5 {
		news = news[len(news)-5:]
	}
	return "Since your last turn, the conversation went on without you:\n" + exchanges(news)
}

func exchanges(past []Exchange) string {
	var b strings.Builder
	for _, x := range past {
		to := "the team"
		if x.Target != "" {
			to = "@" + x.Target
		}
		who, answer := x.Agent, x.Answer
		if answer == "" {
			who, answer = "nobody", "(no answer: the session was stopped)"
		}
		if r := []rune(answer); len(r) > pastLimit {
			answer = string(r[:pastLimit]) + "…"
		}
		fmt.Fprintf(&b, "- The human asked %s: %s\n  Answer from @%s: %s\n", to, x.Question, who, answer)
	}
	return b.String()
}
