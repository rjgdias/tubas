package engine

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"tubas/internal/agent"
)

// The handoff protocol. An agent passes the work on by ending its answer
// with handoff lines:
//
//	HANDOFF @leo: is it the pipeline?
//
// One line passes the baton: @leo takes over. Several lines (or several
// names on one line) run those agents in parallel; they answer back and
// the caller gets a new turn to combine their answers. That is the
// orchestrator-workers pattern.
var (
	handoffLine = regexp.MustCompile(`(?i)^\s*(?:→\s*)?handoff\s*((?:@[a-z0-9_-]+[\s,]*)+)[:\-—]?\s*(.*)$`)
	mention     = regexp.MustCompile(`@([a-zA-Z0-9_-]+)`)
)

// Handoff is one agent being asked something.
type Handoff struct{ To, Note string }

// ParseHandoffs splits an answer into its body and the handoff lines at
// its end, if any. Only the trailing lines count.
func ParseHandoffs(text string) (body string, handoffs []Handoff) {
	lines := strings.Split(strings.TrimRight(text, " \n\r\t"), "\n")
	end := len(lines)
	var found [][]Handoff
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		m := handoffLine.FindStringSubmatch(lines[i])
		if m == nil {
			break
		}
		var line []Handoff
		for _, name := range mention.FindAllStringSubmatch(m[1], -1) {
			line = append(line, Handoff{To: strings.ToLower(name[1]), Note: strings.TrimSpace(m[2])})
		}
		found = append(found, line)
		end = i
	}
	// found is bottom-up; keep the order the agent wrote, without repeats.
	seen := map[string]bool{}
	for i := len(found) - 1; i >= 0; i-- {
		for _, h := range found[i] {
			if !seen[h.To] {
				seen[h.To] = true
				handoffs = append(handoffs, h)
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines[:end], "\n")), handoffs
}

// SystemPrompt is the agent's instructions plus what every agent needs to
// know about working in tubas. Engines that talk to a model use it.
func SystemPrompt(a agent.Agent, team map[string]agent.Agent) string {
	var b strings.Builder
	b.WriteString(a.Instructions)
	b.WriteString("\n\n## Working in tubas\n\n")
	fmt.Fprintf(&b, "You are @%s (%s), one agent in a team. Each turn you get a task, do it, and answer.\n", a.Name, a.Role)

	if len(a.CanCall) > 0 {
		b.WriteString("\nTeammates you can hand work to:\n")
		names := append([]string(nil), a.CanCall...)
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "- @%s (%s)\n", n, team[n].Role)
		}
		b.WriteString("\nTo hand off, end your answer with exactly one line in this form:\n")
		b.WriteString("HANDOFF @name: what you need from them\n")
		if len(a.CanCall) > 1 {
			b.WriteString("To ask several teammates at the same time, write one HANDOFF line for each, with a clear task per line. ")
			b.WriteString("They work in parallel, answer back, and you get a new turn with all their answers to conclude.\n")
		}
		b.WriteString("Hand off only when someone else must act. If the work is done, just answer without a HANDOFF line.\n")
	} else {
		b.WriteString("\nYou cannot hand work to anyone: finish the task yourself.\n")
	}
	if access := a.Tools["workspace"]; access == "read" || access == "write" {
		b.WriteString("\nYou have a workspace folder of your own that survives between sessions, and the team has a shared folder (workspace tools). ")
		b.WriteString("Look there at the start of a task, and keep there what you will want next time: notes, queries that worked, decisions.\n")
	}
	if len(a.Skills) > 0 {
		b.WriteString("\nYou have skills: ways of doing recurring work that worked before. Before a task, see if one fits (load_skill lists them) and follow it.\n")
	}
	if a.Escalate != nil && a.Escalate.Has("ask") {
		b.WriteString("\nWhen you are stuck or unsure on a hard step, ask_senior asks a stronger model. Use it rarely. ")
		b.WriteString("Lines that start with ↑ in a tool result come from that stronger model: weigh its advice.\n")
	}
	if a.Tools["team"] == "write" {
		b.WriteString("\nYou may propose new agents and routines (propose_agent, propose_routine) when the team lacks someone for recurring work. A human approves each one.\n")
	}
	b.WriteString("\nWork in few steps: every step costs the whole conversation again. When you need several independent lookups, call those tools together in the same step, not one by one. What you need to know about a tool is in its description: do not spend steps rediscovering it.\n")
	b.WriteString("Keep the answer short: the conclusion first, then the numbers behind it.\n")
	b.WriteString("Tools that change anything wait for a human to approve; if one is denied, say so and stop.\n")
	return b.String()
}

// The prompts each kind of turn receives. Every turn is stateless: the
// agent sees only what is written here.

func firstPrompt(req Request) string {
	if req.Context == "" {
		return req.Question
	}
	return req.Context + "\n\nNew question from the human: " + req.Question
}

func briefing(req Request, history []string) string {
	var b strings.Builder
	if req.Context != "" {
		b.WriteString(req.Context + "\n\n")
	}
	fmt.Fprintf(&b, "Question from the human: %s\n\n", req.Question)
	b.WriteString("What the team has done so far:\n")
	for _, h := range history {
		b.WriteString(h + "\n")
	}
	return b.String()
}

func handoffPrompt(req Request, history []string, from, note string) string {
	return briefing(req, history) + fmt.Sprintf("\n@%s is handing this to you and asks: %s\n", from, note)
}

func workerPrompt(req Request, history []string, from, note string) string {
	return briefing(req, history) + fmt.Sprintf(
		"\n@%s asked you, while others work in parallel: %s\nAnswer that directly. @%s is waiting for your answer, so do not hand off.\n",
		from, note, from)
}

func teamPrompt(req Request, a agent.Agent) string {
	return firstPrompt(req) + fmt.Sprintf(
		"\n\nThe question went to several agents of the team at once. Answer it from your role (@%s, %s), briefly.\n"+
			"If you have nothing useful to add from your role, answer with exactly: PASS\n", a.Name, a.Role)
}

func teamJoinPrompt(req Request, history []string) string {
	return briefing(req, history) +
		"\nSeveral agents answered the question in parallel; their answers are above. " +
		"As the lead, combine them into one answer for the human: conclude, or hand off.\n"
}

func joinPrompt(req Request, history []string, names string) string {
	return briefing(req, history) + fmt.Sprintf(
		"\nYou asked %s in parallel, and their answers are above. Combine them and continue: conclude, or hand off.\n", names)
}

// briefLimit caps what each turn leaves in the history the next agents
// read: the conclusion, not the whole answer. Every later prompt carries
// the history, so long entries cost again and again.
const briefLimit = 800

// brief squeezes an answer for the history: one paragraph, at most
// briefLimit characters.
func brief(answer string) string {
	s := strings.Join(strings.Fields(answer), " ")
	if r := []rune(s); len(r) > briefLimit {
		s = string(r[:briefLimit]) + "… (cut)"
	}
	return s
}
