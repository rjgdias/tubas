package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Learning: when an agent keeps doing the same work (the same tools, in
// the same order) tubas suggests turning it into a skill. The Tracker
// counts that work per agent and remembers what you answered, in
// <teamDir>/.tubas/learn.json.

// Call is one tool call of a turn, e.g. {"databricks__get_job_runs", "job=x limit=1"}.
type Call struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
}

// Example is one turn that counted towards a suggestion.
type Example struct {
	Question string    `json:"question"`
	Calls    []Call    `json:"calls"`
	At       time.Time `json:"at"`
	Failures []string  `json:"failures,omitempty"` // tool results that failed in that turn, short text
}

// Suggestion says: this agent did this work often enough to make it a skill.
type Suggestion struct {
	Agent     string    `json:"agent"`
	Signature string    `json:"signature"`
	Examples  []Example `json:"examples"` // the runs that made it, oldest first, at most 5
	Draft     Skill     `json:"draft"`
}

const (
	maxExamples = 5
	maxStored   = 500 // characters kept per question, args or failure in learn.json
	maxCopied   = 200 // characters copied into a draft from any args or failure
	maxSteps    = 20
	maxPitfalls = 10
	maxDraftLen = 40 // characters of a learned skill's name
)

// pattern is one agent's signature: how often it was seen and what you
// said about it.
type pattern struct {
	Count     int       `json:"count"`
	Examples  []Example `json:"examples,omitempty"`
	Suggested bool      `json:"suggested,omitempty"`
	Never     bool      `json:"never,omitempty"`
}

// Tracker counts repeated work. It is safe to use from several goroutines.
type Tracker struct {
	mu        sync.Mutex
	path      string
	threshold int
	agents    map[string]map[string]*pattern // agent → signature → pattern
}

// OpenTracker reads the team's learn.json (a missing file is an empty
// one). threshold is how many times the same work has to happen before
// tubas suggests a skill; 0 turns learning off.
func OpenTracker(teamDir string, threshold int) (*Tracker, error) {
	t := &Tracker{
		path:      filepath.Join(teamDir, ".tubas", "learn.json"),
		threshold: threshold,
		agents:    map[string]map[string]*pattern{},
	}
	data, err := os.ReadFile(t.path)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Agents map[string]map[string]*pattern `json:"agents"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", t.path, err)
	}
	for agent, sigs := range file.Agents {
		for sig, p := range sigs {
			if p != nil {
				*t.get(agent, sig) = *p
			}
		}
	}
	return t, nil
}

// get returns the pattern for agent and signature, creating it if needed.
func (t *Tracker) get(agent, sig string) *pattern {
	sigs := t.agents[agent]
	if sigs == nil {
		sigs = map[string]*pattern{}
		t.agents[agent] = sigs
	}
	p := sigs[sig]
	if p == nil {
		p = &pattern{}
		sigs[sig] = p
	}
	return p
}

func (t *Tracker) save() error {
	data, err := json.MarshalIndent(struct {
		Agents map[string]map[string]*pattern `json:"agents"`
	}{t.agents}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	return writeAtomic(t.path, data)
}

// Signature is the shape of a turn's work: the tool names in order, with
// repeats in a row collapsed (a,a,b → a,b). It is "" when the turn used
// fewer than 2 distinct tools: one tool alone is not a routine worth a
// skill.
func Signature(calls []Call) string {
	var tools []string
	distinct := map[string]bool{}
	for _, c := range calls {
		name := strings.TrimSpace(c.Tool)
		if name == "" {
			continue
		}
		distinct[name] = true
		if len(tools) == 0 || tools[len(tools)-1] != name {
			tools = append(tools, name)
		}
	}
	if len(distinct) < 2 {
		return ""
	}
	return strings.Join(tools, ",")
}

// Observe counts one finished turn. Only turns that went well and used a
// routine (see Signature) count. When an agent's routine reaches the
// threshold for the first time (and you did not say "never"), Observe
// returns a Suggestion with a draft skill; otherwise it returns nil.
func (t *Tracker) Observe(agent, question string, calls []Call, failures []string, ok bool, at time.Time) (*Suggestion, error) {
	if t == nil || t.threshold <= 0 || !ok {
		return nil, nil
	}
	sig := Signature(calls)
	if sig == "" || agent == "" {
		return nil, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.get(agent, sig)
	if p.Never || p.Suggested {
		return nil, nil // already answered: nothing changes
	}

	// Keep a trimmed copy: tool args and results can be long, and the
	// file only needs enough to write a draft.
	ex := Example{Question: clip(question, maxStored), At: at}
	for _, c := range calls {
		ex.Calls = append(ex.Calls, Call{Tool: clip(c.Tool, maxStored), Args: clip(c.Args, maxStored)})
	}
	for _, f := range failures {
		if f = clip(f, maxStored); f != "" {
			ex.Failures = append(ex.Failures, f)
		}
	}
	p.Count++
	p.Examples = append(p.Examples, ex)
	if len(p.Examples) > maxExamples {
		p.Examples = p.Examples[len(p.Examples)-maxExamples:]
	}

	var sug *Suggestion
	if p.Count >= t.threshold {
		p.Suggested = true
		examples := append([]Example(nil), p.Examples...)
		sug = &Suggestion{Agent: agent, Signature: sig, Examples: examples, Draft: Draft(agent, examples)}
	}
	if err := t.save(); err != nil {
		return nil, err
	}
	return sug, nil
}

// Never means: do not suggest this routine for this agent again.
func (t *Tracker) Never(agent, signature string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.get(agent, signature)
	*p = pattern{Never: true} // the examples are no longer needed
	return t.save()
}

// Later means: not now. The routine is counted again from zero, so it
// comes back only if the agent keeps doing it.
func (t *Tracker) Later(agent, signature string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.get(agent, signature)
	*p = pattern{}
	return t.save()
}

// ── drafts ──────────────────────────────────────────────────────────────

// Draft writes a first version of a skill from the runs that made a
// suggestion (oldest first), without asking a model: the person reviews
// it before saving. Args and failures come from tool output, which may
// be untrusted, so only short, single-line pieces of them are copied.
func Draft(agent string, examples []Example) Skill {
	agent = clip(agent, 64)
	var latest Example
	if len(examples) > 0 {
		latest = examples[len(examples)-1]
	}

	name := draftName(latest.Calls)
	question := clip(latest.Question, 150)
	if question == "" {
		question = "the same request again"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", name)
	runs := "runs"
	if len(examples) == 1 {
		runs = "run"
	}
	fmt.Fprintf(&b, "Learned from %d %s of %s.\n\n## Steps\n", len(examples), runs, agent)
	for i, c := range latest.Calls {
		if i == maxSteps {
			fmt.Fprintf(&b, "%d. … and %d more calls\n", i+1, len(latest.Calls)-i)
			break
		}
		step := clip(c.Tool, 100)
		if args := clip(c.Args, maxCopied); args != "" {
			step += " " + args
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, step)
	}

	var pitfalls []string
	seen := map[string]bool{}
	for _, ex := range examples {
		for _, f := range ex.Failures {
			f = clip(f, maxCopied)
			if f != "" && !seen[f] && len(pitfalls) < maxPitfalls {
				seen[f] = true
				pitfalls = append(pitfalls, f)
			}
		}
	}
	if len(pitfalls) > 0 {
		b.WriteString("\n## Pitfalls\n")
		for _, f := range pitfalls {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}

	return Skill{
		Name:        name,
		Description: "When asked something like: " + question,
		Body:        strings.TrimSpace(b.String()),
		Origin:      OriginLearned,
		Version:     1,
	}
}

var notKebab = regexp.MustCompile(`[^a-z0-9]+`)

// draftName names a learned skill after its tools, without connector
// prefixes: databricks__get_job_runs, databricks__list_run_tasks →
// "get-job-runs-then-list-run-tasks".
func draftName(calls []Call) string {
	var parts []string
	seen := map[string]bool{}
	for _, c := range calls {
		tool := c.Tool
		if i := strings.LastIndex(tool, "__"); i >= 0 {
			tool = tool[i+2:] // mcp__databricks__get_job_runs → get_job_runs
		}
		part := strings.Trim(notKebab.ReplaceAllString(strings.ToLower(tool), "-"), "-")
		if part != "" && !seen[part] {
			seen[part] = true
			parts = append(parts, part)
		}
	}
	name := strings.Join(parts, "-then-")

	// Trim at a hyphen so the name keeps whole words.
	if len(name) > maxDraftLen {
		cut := name[:maxDraftLen+1]
		if i := strings.LastIndex(cut, "-"); i > 0 {
			name = cut[:i]
		} else {
			name = name[:maxDraftLen]
		}
		name = strings.TrimSuffix(name, "-then")
		name = strings.Trim(name, "-")
	}
	if name == "" {
		return "learned-routine"
	}
	if name[0] < 'a' || name[0] > 'z' {
		name = "do-" + name // a name that reads as a word, not a number
	}
	if !ValidName(name) {
		return "learned-routine"
	}
	return name
}

// clip makes untrusted text safe to show and store: control characters
// (newlines included) become spaces, invisible formatting characters
// (like bidi overrides) go away, runs of spaces collapse, and it is cut
// to n characters.
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n])) + "…"
	}
	return s
}
