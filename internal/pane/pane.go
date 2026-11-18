// Package pane runs programs in real terminals: your shell, a dev server,
// any command. Each pane is a pseudo-terminal (ConPTY on Windows, a pty on
// Linux and macOS) with a terminal emulator reading its output, so tubas
// can draw the program's own screen inside its layout and send it keys.
//
// Panes live in the team's background server, like sessions: close the
// window and the programs keep running; open tubas again and they are
// where you left them.
package pane

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"tubas/internal/repo"
)

// States, the same words as sessions:
//
//	blocked  the program asks you something (a y/n, a "Do you want to…")
//	working  it is printing on its own
//	idle     it waits quietly (a prompt, an input box)
//	done     it exited, or an agent finished and you have not looked yet
const (
	Blocked = "blocked"
	Working = "working"
	Idle    = "idle"
	Done    = "done"
)

// Spec says what a pane runs and where.
type Spec struct {
	Title   string   `json:"title,omitempty"`
	Command string   `json:"command,omitempty"` // "" runs your shell
	Args    []string `json:"args,omitempty"`
	Dir     string   `json:"dir,omitempty"`

	// Repo is the repository connector it works on, for the header. With
	// Worktree, the pane gets a new worktree of it (Dir is the checkout)
	// on its own branch, the same way a tubas session does.
	Repo     string `json:"repo,omitempty"`
	Worktree bool   `json:"worktree,omitempty"`
	Base     string `json:"base,omitempty"`
	Setup    string `json:"setup,omitempty"`
}

// Info is what a client sees of a pane.
type Info struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Command  string         `json:"command"` // the program, as you typed it
	Dir      string         `json:"dir"`
	Repo     string         `json:"repo,omitempty"`
	Worktree *repo.Worktree `json:"worktree,omitempty"`
	State    string         `json:"state"`
	Started  time.Time      `json:"started"`
	Exit     string         `json:"exit,omitempty"`  // how it ended, once it exited
	Agent    string         `json:"agent,omitempty"` // the agent CLI it runs, when tubas knows it
	W        int            `json:"w"`
	H        int            `json:"h"`
}

// Screen is a picture of a pane's terminal.
type Screen struct {
	Version int      `json:"version"` // grows with every change
	Lines   []string `json:"lines"`   // one per row, with colors, the cursor drawn
	State   string   `json:"state"`
	Title   string   `json:"title,omitempty"` // what the program calls itself
}

// Key is one key press, or text, sent to a pane.
type Key struct {
	Name  string `json:"key,omitempty"`   // "enter", "ctrl+c", "alt+up"…
	Text  string `json:"text,omitempty"`  // typed text
	Paste string `json:"paste,omitempty"` // pasted text (bracketed when the program asks)
}

// Pane is one running program.
type Pane struct {
	info Info

	mu         sync.Mutex
	em         *vt.Emulator
	pt         pty.Pty
	cmd        *pty.Cmd
	version    int
	lastOutput time.Time
	lastInput  time.Time
	exited     bool
	title      string
	changed    chan struct{} // closed on every change, then replaced
	closeOnce  sync.Once     // closing a pseudo-console twice crashes Windows
	copyDone   chan struct{} // the input copy loop ended

	onExit func() // called when the program ends on its own
}

// scrollback is how many lines that left the screen a pane keeps.
const scrollback = 5000

// quietAfterInput is how long output right after a key press counts as
// the echo of what you typed, not as the program working.
const quietAfterInput = 400 * time.Millisecond

// workingFor is how long a pane stays "working" after its last output.
const workingFor = 1500 * time.Millisecond

func start(id string, spec Spec, w, h int, env []string) (*Pane, error) {
	w, h = max(w, 10), max(h, 3)
	name, args := spec.Command, spec.Args
	shown := strings.TrimSpace(strings.Join(append([]string{name}, args...), " "))
	if name == "" {
		name, args = Shell()
		shown = "shell"
	}
	// go-pty looks a bare name up in the pane's folder, not on the PATH.
	if !strings.ContainsAny(name, `/\`) {
		if found, err := lookPath(name); err == nil {
			name = found
		} else {
			return nil, fmt.Errorf("%s is not installed (not on the PATH)", name)
		}
	}
	pt, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("no terminal: %w", err)
	}
	pt.Resize(w, h)
	cmd := pt.Command(name, args...)
	cmd.Dir = spec.Dir
	cmd.Env = append(append(cleanEnv(os.Environ()), env...), "TERM=xterm-256color", "COLORTERM=truecolor", "TUBAS_PANE="+id)
	if err := cmd.Start(); err != nil {
		pt.Close()
		return nil, fmt.Errorf("starting %s: %w", shown, err)
	}
	title := spec.Title
	if title == "" {
		title = shown
	}
	p := &Pane{
		info:     Info{ID: id, Title: title, Command: shown, Dir: spec.Dir, Repo: spec.Repo, Started: time.Now(), W: w, H: h},
		em:       vt.NewEmulator(w, h),
		pt:       pt,
		cmd:      cmd,
		changed:  make(chan struct{}),
		copyDone: make(chan struct{}),
	}
	p.em.SetScrollbackSize(scrollback)
	p.em.SetCallbacks(vt.Callbacks{Title: func(t string) { p.title = t }})
	go p.readOutput()
	go func() { // keys, and the emulator's answers to the program's questions (cursor position, colors…)
		defer close(p.copyDone)
		io.Copy(pt, p.em)
	}()
	go p.waitExit()
	return p, nil
}

// cleanEnv drops what marks a process as running inside a Claude Code
// session (its ids, sockets and tokens): a pane is a fresh terminal, and
// a claude started in it is a session of its own. The variables that
// pick your AI provider (CLAUDE_CODE_USE_BEDROCK…) stay.
func cleanEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		switch {
		case strings.HasPrefix(upper, "CLAUDE_CODE_USE_"):
		case upper == "CLAUDECODE", upper == "CLAUDE_PID", upper == "CLAUDE_EFFORT",
			strings.HasPrefix(upper, "CLAUDE_CODE_"), strings.HasPrefix(upper, "CLAUDE_AGENT_SDK_"),
			strings.HasPrefix(upper, "CLAUDE_PREVIEW_"):
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Shell is the program a plain pane runs.
func Shell() (string, []string) {
	if isWindows {
		if _, err := lookPath("pwsh.exe"); err == nil {
			return "pwsh.exe", []string{"-NoLogo"}
		}
		return "powershell.exe", []string{"-NoLogo"}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, nil
	}
	return "/bin/sh", nil
}

func (p *Pane) readOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := p.pt.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.em.Write(buf[:n])
			p.version++
			p.lastOutput = time.Now()
			p.notify()
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (p *Pane) waitExit() {
	err := p.cmd.Wait()
	exit := "exited"
	if st := p.cmd.ProcessState; st != nil {
		exit = fmt.Sprintf("exited %d", st.ExitCode())
	} else if err != nil {
		exit = err.Error()
	}
	time.Sleep(100 * time.Millisecond) // let the last output arrive
	p.mu.Lock()
	p.exited, p.info.Exit = true, exit
	p.version++
	p.notify()
	onExit := p.onExit
	p.mu.Unlock()
	p.closePty()
	if onExit != nil {
		onExit()
	}
}

func (p *Pane) closePty() { p.closeOnce.Do(func() { p.pt.Close() }) }

// notify wakes everyone waiting for a change. Call with p.mu held.
func (p *Pane) notify() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// Info describes the pane now.
func (p *Pane) Info() Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.state(time.Now()) // first: reading the screen can name the agent
	i := p.info
	i.State = state
	return i
}

// state reads the screen: a question on it waits for you, output means
// working, quiet means idle. Call with p.mu held.
func (p *Pane) state(now time.Time) string {
	if p.exited {
		return Done
	}
	if asks(p.plainLines(15)) {
		return Blocked
	}
	if now.Sub(p.lastOutput) < workingFor && p.lastOutput.Sub(p.lastInput) > quietAfterInput {
		return Working
	}
	return Idle
}

// Seen says you looked at the pane. Plain terminals keep no "done" to
// clear, so there is nothing to do.
func (p *Pane) Seen() {}

// History is everything the pane kept: the lines that scrolled away,
// then the screen, with colors and as plain text.
func (p *Pane) History() (lines, plain []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	add := func(l uv.Line) {
		lines = append(lines, l.Render()+ansi.ResetStyle)
		plain = append(plain, strings.TrimRight(l.String(), " "))
	}
	if sb := p.em.Scrollback(); sb != nil {
		for _, l := range sb.Lines() {
			add(l)
		}
	}
	w, h := p.em.Width(), p.em.Height()
	last := -1 // drop the empty rows under the last one written
	for y := range h {
		for x := range w {
			if c := p.em.CellAt(x, y); c != nil && c.Content != "" && c.Content != " " {
				last = y
				break
			}
		}
	}
	for y := 0; y <= last; y++ {
		line := make(uv.Line, w)
		for x := range w {
			if c := p.em.CellAt(x, y); c != nil {
				line[x] = *c
			} else {
				line[x] = uv.EmptyCell
			}
		}
		add(line)
	}
	return lines, plain
}

// prompts are what programs print when they wait for an answer.
var prompts = []string{
	"do you want to", "(y/n)", "[y/n]", "(yes/no)", "[yes/no]", "enter to confirm",
	"press enter to continue", "yes, i trust", "allow once", "❯ 1. yes", "> 1. yes",
}

// asks says whether the bottom of a screen is a question.
func asks(lines []string) bool {
	for _, l := range lines {
		l = strings.ToLower(l)
		for _, p := range prompts {
			if strings.Contains(l, p) {
				return true
			}
		}
	}
	return false
}

// plainLines is the text of the last n rows that have any. Call with
// p.mu held.
func (p *Pane) plainLines(n int) []string {
	var out []string
	for y := p.em.Height() - 1; y >= 0 && len(out) < n; y-- {
		var b strings.Builder
		for x := 0; x < p.em.Width(); x++ {
			if c := p.em.CellAt(x, y); c != nil && c.Content != "" {
				b.WriteString(c.Content)
			} else {
				b.WriteByte(' ')
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Screen draws the terminal. Call with p.mu held.
func (p *Pane) screen() Screen {
	w, h := p.em.Width(), p.em.Height()
	cur := p.em.CursorPosition()
	showCursor := !p.em.CursorHidden() && !p.exited
	lines := make([]string, h)
	for y := range h {
		line := make(uv.Line, w)
		for x := range w {
			if c := p.em.CellAt(x, y); c != nil {
				line[x] = *c
			} else {
				line[x] = uv.EmptyCell
			}
		}
		if showCursor && y == cur.Y && cur.X >= 0 && cur.X < w {
			c := line[cur.X]
			if c.Content == "" || c.Width == 0 {
				c = uv.EmptyCell
				c.Content = " "
				c.Width = 1
			}
			c.Style.Attrs |= uv.AttrReverse
			line[cur.X] = c
		}
		lines[y] = line.Render() + ansi.ResetStyle
	}
	return Screen{Version: p.version, Lines: lines, State: p.state(time.Now()), Title: p.title}
}

// Wait returns the screen once its version passes since, or after wait.
// Bursts of output are gathered into one picture.
func (p *Pane) Wait(since int, wait time.Duration) Screen {
	p.mu.Lock()
	if p.version > since || wait <= 0 {
		s := p.screen()
		p.mu.Unlock()
		return s
	}
	ch := p.changed
	p.mu.Unlock()
	select {
	case <-ch:
		time.Sleep(16 * time.Millisecond) // one frame: let the rest of the burst land
	case <-time.After(wait):
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.screen()
}

// Send types keys into the pane.
func (p *Pane) Send(keys []Key) error {
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		return errors.New("the program has exited")
	}
	p.lastInput = time.Now()
	p.mu.Unlock()
	// The emulator turns keys into the bytes the program expects (it knows
	// the program's modes, like application cursor keys and bracketed
	// paste) and hands them to the copy loop. Keys and pastes read those
	// modes, so they take the lock; plain text doesn't need it.
	for _, k := range keys {
		switch {
		case k.Paste != "":
			p.mu.Lock()
			p.em.Paste(k.Paste)
			p.mu.Unlock()
		case k.Text != "":
			p.em.SendText(k.Text)
		case k.Name != "":
			if ev, ok := ParseKey(k.Name); ok {
				p.mu.Lock()
				p.em.SendKey(ev)
				p.mu.Unlock()
			}
		}
	}
	return nil
}

// Resize changes the terminal's size.
func (p *Pane) Resize(w, h int) {
	w, h = max(w, 10), max(h, 3)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.info.W == w && p.info.H == h {
		return
	}
	p.info.W, p.info.H = w, h
	p.em.Resize(w, h)
	if !p.exited {
		p.pt.Resize(w, h)
	}
	p.version++
	p.notify()
}

// Text is the screen as plain text, for `tubas pane read`.
func (p *Pane) Text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.em.String()
}

// Kill stops the program.
func (p *Pane) Kill() {
	p.mu.Lock()
	exited := p.exited
	p.mu.Unlock()
	if !exited && p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	p.closePty()
	// The copy loop waits for input; wake it so it finds the terminal
	// closed and ends, then close the emulator behind it.
	go p.em.SendText("\r")
	select {
	case <-p.copyDone:
		p.em.Close()
	case <-time.After(3 * time.Second):
		// Still reading: leave the emulator to the garbage collector
		// rather than close it under the reader's feet.
	}
}
