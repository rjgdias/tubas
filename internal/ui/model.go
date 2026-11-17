// Package ui is the tubas terminal interface, built with Bubble Tea.
//
// Bubble Tea follows the Elm architecture, which has three pieces:
//
//   - Model:  all the state of the screen, in one struct.
//   - Update: receives a message (a key press, a resize, an engine
//     event...) and returns the new model. The only place state changes.
//   - View:   turns the model into a string. Bubble Tea draws that string.
//
// The loop is: message -> Update -> new model -> View -> screen.
package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tubas/internal/agent"
	"tubas/internal/engine"
	"tubas/internal/pane"
	"tubas/internal/repo"
	"tubas/internal/routine"
	"tubas/internal/server"
	"tubas/internal/skill"
)

const tickEvery = 100 * time.Millisecond

// Config describes the team, for the screen.
type Config struct {
	Name       string        // the team's name, shown in the header
	Example    string        // example question shown on the start screen
	Lead       string        // who answers when nobody else is picked
	Routing    string        // for questions that name no agent: lead, router or all
	Router     engine.Router // used when Routing is "router"
	MaxHops    int           // how many turns a session may take
	Connectors []string      // where the team's tools come from

	HistoryFile string    // where the conversation is saved ("" = not saved)
	Past        []Session // earlier sessions, when resuming a conversation

	TeamDir        string            // the team folder, for the files section
	ReportsDir     string            // where reports go, relative to TeamDir
	ConnectorKinds map[string]string // connector -> what runs it ("sqlite · data/tubas.db")

	// Remote, when set, is the team's background server: sessions run
	// there and this screen is a client (see remote.go).
	Remote *server.Client

	// Repos are the team's repository connectors (see review.go).
	Repos map[string]repo.Spec

	// Panes is where terminal panes run: the server, or this process
	// (see panes.go). Nil turns panes off.
	Panes pane.Host

	// WorkDir is the folder tubas was opened from: a new shell pane
	// starts there.
	WorkDir string

	// Notify is team.yaml's notify: all, window or off.
	Notify string
	// Notice is shown at the bottom when the window opens.
	Notice string

	// Harnesses are the ones this computer can run (claude, codex, api,
	// ACP agents); Providers maps each model provider to its models. The
	// agent form offers them.
	Harnesses []string
	Providers map[string][]string
}

// pendingApproval is a tool call waiting for your y or n. Agents running
// in parallel can ask at the same time, so they wait in a queue.
type pendingApproval struct {
	agent, call string
	reply       chan<- bool
	at          time.Time // when it started waiting
	detail      string    // the whole call, and a senior review when escalation ran
	routine     string    // the routine that asked, "" for the chat
	id          string    // the server's id for it, in remote mode
	session     string    // the server session it belongs to, in remote mode
}

// Model is the whole state of the screen.
type Model struct {
	team    []agent.Agent
	colors  map[string]lipgloss.Color // agent name -> its color
	eng     engine.Engine
	cfg     Config
	example string // example question shown on the start screen

	// The session and the goroutine that feeds it.
	session Session
	past    []Session           // earlier sessions of this conversation
	run     int                 // increases on every (re)start; see the messages below
	events  <-chan engine.Event // receive-only end of the engine's channel
	cancel  context.CancelFunc  // stops the running session
	queue   []pendingApproval   // approvals waiting for you, oldest first
	running bool
	frame   int // spinner animation frame

	// What you typed and the @mention menu.
	input          []rune
	popupIdx       int
	popupDismissed bool
	last           request // for ctrl+r (run again)

	// What you are looking at.
	section Section          // which of the seven sections is open
	secIdx  [numSections]int // the selected item in each section's list
	files   []fileEntry      // the files section, read when you open it
	form    *form            // the form being filled, if any
	confirm string           // an action waiting for its second key press

	// agents › permissions and agents › skills
	permMode, permDirty bool
	permRow, permCol    int
	skillsMode          bool
	skillIdx            int
	skills, external    []skill.Skill

	// Work that runs on its own.
	routines    routine.File
	routinesErr string
	runs        []*routineRun // routines running now
	runID       int
	lastStart   map[string]time.Time // when each routine last started, this time tubas is open
	retryAt     map[string]time.Time // failed runs waiting to try again
	startedAt   time.Time
	notes       []inboxNote       // inbox items that are not approvals
	tracker     *skill.Tracker    // learns skills from repeated work
	providers   []engine.Detected // AI providers found on this computer

	// With a background server.
	remoteID     string         // the session on screen
	conversation string         // the conversation it belongs to: follow-ups continue it
	status       *server.Status // the last picture from the server
	picker       bool           // the session picker (ctrl+g) is open
	pickIdx      int
	prefix       bool // ctrl+b was pressed: the next key is an app action

	// Code: the worktrees of the session on screen, and the review.
	code        *repo.Set
	changes     []wtChanges
	review      bool
	fileIdx     int
	reviewWT    int
	panes       *paneState   // terminal panes next to the chat, nil when off
	selected    int          // index of the selected turn, -1 for none
	scroll      int          // lines the conversation is scrolled back from the bottom
	modelPick   *modelPicker // /model, while open
	gatewayNote string       // why the gateway's models are missing, when they are
	mcp         *mcpView     // /mcp: the MCP servers, open over the chat
	deleting    string       // a conversation waiting for y to be deleted
	menu        *ctxMenu     // the right-click menu, when open
	// aliases are what your Claude settings make sonnet, opus and haiku
	// mean; with a gateway that lists its models, only those show.
	aliases     map[string]string
	onGateway   bool
	grid        bool
	showDetails bool
	showHelp    bool
	notice      string // short message shown next to the input
	width       int
	height      int
}

// Messages are plain types. Bubble Tea delivers them to Update.
//
// Each one carries the run number. After a restart, a message from the
// old run may still arrive; comparing run numbers lets us drop it.
type (
	eventMsg struct {
		run   int
		event engine.Event
	}
	closedMsg struct{ run int }
	tickMsg   struct{ run int }
)

// New creates the model. Nothing runs until you send a question.
func New(team []agent.Agent, eng engine.Engine, cfg Config) Model {
	if cfg.MaxHops == 0 {
		cfg.MaxHops = 6
	}
	if cfg.Lead == "" && len(team) > 0 {
		cfg.Lead = team[0].Name
	}
	if cfg.Example == "" {
		cfg.Example = "what should we look at first?"
	}
	m := Model{
		team:     team,
		colors:   map[string]lipgloss.Color{},
		eng:      eng,
		cfg:      cfg,
		example:  cfg.Example,
		selected: -1,
		past:     cfg.Past,

		lastStart: map[string]time.Time{},
		retryAt:   map[string]time.Time{},
		startedAt: time.Now(),
	}
	for _, a := range team {
		m.colors[a.Name] = palette[a.Color]
	}
	if cfg.Panes != nil {
		m.panes = newPaneState(cfg.Panes)
	}
	if cfg.Notice != "" {
		m.notice = warnStyle.Render(cfg.Notice)
	}
	return m.loadWork()
}

// Init runs once at startup and returns the first command.
//
// A command (tea.Cmd) is a function Bubble Tea runs in the background;
// whatever message it returns is delivered to Update. If a session was
// already started (tubas -ask), we start listening to it here.
func (m Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.running {
		cmds = append(cmds, waitForEvent(m.run, m.events), tick(m.run))
	}
	if m.cfg.TeamDir != "" {
		cmds = append(cmds, routineTick(), detectProviders(m.cfg.TeamDir))
	}
	if m.cfg.Remote != nil {
		cmds = append(cmds, pollStatus(m.cfg.Remote), nextStatusTick())
	}
	if m.panes != nil {
		cmds = append(cmds, loadPanes(m.panes.host))
	}
	return tea.Batch(cmds...)
}

// request is a question and who it is for.
type request struct {
	target   string // the agent named; "" lets the team's routing decide
	question string
	everyone bool // @todos
}

// start launches a new session in its own goroutine.
func (m Model) start(r request) (Model, tea.Cmd) {
	target, question := r.target, r.question
	if m.cancel != nil {
		m.cancel() // stop the previous run, if any
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan engine.Event, 64) // buffered: the engine rarely waits for us

	team := map[string]agent.Agent{}
	for _, a := range m.team {
		team[a.Name] = a
	}
	req := engine.Request{Team: team, Target: target, Everyone: r.everyone, Question: question,
		MaxHops: m.cfg.MaxHops, Context: conversationContext(m.past),
		Lead: m.cfg.Lead, Routing: m.cfg.Routing, Router: m.cfg.Router}
	m.changes, m.review = nil, false
	if len(m.cfg.Repos) > 0 && m.cfg.Remote == nil {
		m.code = repo.NewSet(m.cfg.TeamDir, fmt.Sprintf("l%d", time.Now().Unix()%100000), question, m.cfg.Repos)
		req.Repos, req.Workspace = map[string]string{}, m.code.Workspace
		for name, spec := range m.cfg.Repos {
			req.Repos[name] = spec.Path
		}
	}

	if m.cfg.Remote != nil {
		// The server runs it; this screen listens.
		var err error
		if m, err = m.startRemote(r, ctx, events); err != nil {
			cancel()
			m.notice = badStyle.Render("server: " + err.Error())
			return m, nil
		}
	} else {
		// `go` starts a goroutine: Run works in parallel, and this function
		// returns right away. The TUI stays responsive while agents work.
		go engine.Run(ctx, m.eng, req, events)
	}

	m.run++
	m.events, m.cancel, m.queue, m.running = events, cancel, nil, true
	m.last = r
	m.selected, m.showDetails, m.notice = -1, false, ""
	m.session = Session{
		Team:      m.cfg.Name,
		AskedAt:   time.Now().Format("15:04"),
		Target:    target,
		Question:  question,
		MaxHops:   m.cfg.MaxHops,
		StartedAt: time.Now(),
	}
	// Batch runs both commands: wait for the first event, start the spinner.
	return m, tea.Batch(waitForEvent(m.run, events), tick(m.run))
}

// waitForEvent is a command that blocks until the next event arrives.
// Update asks for it again after each event, which forms a loop.
func waitForEvent(run int, events <-chan engine.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-events // ok is false once the engine closed the channel
		if !ok {
			return closedMsg{run}
		}
		return eventMsg{run, e}
	}
}

func tick(run int) tea.Cmd {
	return tea.Tick(tickEvery, func(time.Time) tea.Msg { return tickMsg{run} })
}

// Update handles one message and returns the new model.
//
// The receiver is a value (m Model), not a pointer: Update gets a copy,
// changes it and returns it. The old model is never modified.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(Model); ok && nm.panes != nil {
		// Whatever changed (the window, the layout, the bottom bar), each
		// terminal learns its new size.
		if more := nm.panesAfter(); more != nil {
			return nm, tea.Batch(cmd, more)
		}
	}
	return next, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case panesLoadedMsg:
		return m.panesLoaded(msg)

	case paneScreenMsg:
		return m.paneScreen(msg)

	case paneOpenedMsg:
		return m.paneOpened(msg)

	case paneListTick:
		return m.paneListTickMsg()

	case paneListMsg:
		return m.paneList(msg)

	case copyLoadedMsg:
		return m.copyLoaded(msg), nil

	case tea.MouseMsg:
		return m.mouse(msg)

	case tea.WindowSizeMsg:
		// Sent at startup and every time the terminal is resized.
		m.width, m.height = msg.Width, msg.Height

	case eventMsg:
		if msg.run != m.run {
			return m, nil // left over from a previous run
		}
		m = m.handle(msg.event)
		next := waitForEvent(m.run, m.events)
		switch msg.event.(type) {
		case engine.SessionDone:
			if m.cfg.Remote != nil && m.remoteID != "" {
				go m.cfg.Remote.Seen(m.remoteID) // you are looking at it
			}
			m = m.loadChanges()
			m = m.learn(m.session)
			m = m.reloadTeam().loadWork() // the lead may have created an agent or a routine
			return m, tea.Batch(next, m.save())
		case engine.HopLimitReached, engine.Failed:
			return m, tea.Batch(next, m.save()) // the session ended: write it down
		}
		return m, next

	case closedMsg:
		if msg.run == m.run {
			m.running, m.queue = false, nil
		}

	case noticeMsg:
		m.notice = string(msg)

	case routineTickMsg:
		m.frame++
		m, cmds := m.checkRoutines(time.Now())
		return m, tea.Batch(append(cmds, routineTick())...)

	case routineEventMsg:
		return m.routineEvent(msg)

	case routineClosedMsg:
		for _, r := range m.runs {
			if r.id == msg.id && r.session.Outcome == Ongoing { // ended without a final event
				r.session = r.session.end(Failed, "the run stopped")
				return m.routineFinished(r), nil
			}
		}

	case mcpProbedMsg:
		return m.mcpProbed(msg), nil

	case conversationDeletedMsg:
		return m.conversationDeleted(msg)

	case conversationRenamedMsg:
		return m.conversationRenamed(msg)

	case providersMsg:
		m.providers = msg
		m.gatewayNote = ""
		for _, d := range msg {
			if d.Name == "gateway" && len(d.Models) == 0 && d.ModelsErr != "" {
				m.gatewayNote = "your gateway did not list its models: " + d.ModelsErr + " · you can type a model name"
			}
		}
		for _, d := range msg { // a gateway's own model names, for the agent form
			if d.Name == "gateway" && len(d.Models) > 0 {
				if m.cfg.Providers == nil {
					m.cfg.Providers = map[string][]string{}
				}
				m.cfg.Providers["anthropic"] = d.Models
				m.onGateway = true
				m.aliases = aliasMeanings(m.cfg.WorkDir)
			}
		}

	case statusMsg:
		first := m.status == nil
		alert := sessionAlerts(m.status, server.Status(msg), m.remoteID)
		m = m.applyStatus(server.Status(msg))
		var ring tea.Cmd
		if alert != "" {
			m.notice = alert
			if m.cfg.Notify != "off" {
				ring = bell()
			}
		}
		if first && !m.running && !m.session.started() {
			m, cmd := m.firstAttach(server.Status(msg))
			return m, tea.Batch(cmd, ring)
		}
		return m, ring

	case statusTick:
		if m.cfg.Remote != nil {
			return m, tea.Batch(pollStatus(m.cfg.Remote), nextStatusTick())
		}

	case editorDoneMsg:
		return m.editorDone(msg), nil

	case changesMsg:
		m.changes = msg

	case codeActionMsg:
		m = m.codeActionDone(msg)

	case tickMsg:
		if msg.run != m.run || !m.running {
			return m, nil // returning no command ends the tick loop
		}
		m.frame++
		return m, tick(m.run)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handle applies one engine event to the model.
func (m Model) handle(e engine.Event) Model {
	m.session = m.session.apply(e)
	switch e := e.(type) {
	case engine.ApprovalRequested:
		if e.ID != "" && slices.ContainsFunc(m.queue, func(p pendingApproval) bool { return p.id == e.ID }) {
			break // already in the inbox, from the server's status
		}
		m.queue = append(slices.Clone(m.queue), pendingApproval{e.Agent, e.Call, e.Reply, time.Now(), e.Detail, "", e.ID, ""})
	case engine.SessionDone, engine.HopLimitReached, engine.Failed:
		m.showDetails = false
	}
	return m
}

// handleKey decides what a key means. The same key can mean different
// things: "q" quits when the input is empty, but is a letter while you
// type. So the order of the checks below matters.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// The right-click menu takes the keys while it is open.
	if m.menu != nil {
		return m.menuKey(msg)
	}
	// The model picker (/model) takes every key until it closes.
	if m.modelPick != nil {
		return m.modelPickerKey(msg)
	}
	// So does the MCP screen (/mcp).
	if m.mcp != nil {
		return m.mcpKey(key)
	}

	// A terminal pane has the keys: everything goes to it but ctrl+b
	// (and alt+1…7, tubas's sections).
	if m.panes != nil && m.panes.closingWS != nil && m.form == nil {
		ws := m.panes.closingWS
		m.panes.closingWS, m.notice = nil, ""
		if key == "y" {
			return m.closeWorkspace(ws)
		}
		return m, nil
	}
	// A conversation waiting for y (or d again) to be deleted.
	if m.deleting != "" {
		id := m.deleting
		m.deleting, m.notice = "", ""
		if key == "y" || key == "d" {
			return m.deleteConversation(id)
		}
		if m.picker {
			return m, nil // the key only said no; the list stays
		}
	}
	if m.panes != nil && m.panes.closing != "" && m.form == nil {
		id := m.panes.closing
		m.panes.closing, m.notice = "", ""
		if key == "y" {
			return m.closePane(id), m.closePaneCmd(id)
		}
		return m, nil
	}
	if m.paneFocused() && m.form == nil && !m.showHelp && !m.prefix && key != "ctrl+b" {
		if s, ok := sectionForKey(key, false); ok && msg.Alt {
			return m.goTo(s), nil
		}
		return m.paneInput(msg)
	}

	if key == "ctrl+o" && m.form == nil {
		return m.openModelPicker(), nil
	}

	// 1. Always available.
	if key == "ctrl+c" {
		m.Stop()
		return m, tea.Quit
	}

	// 2. The keys overlay takes every key until it closes.
	if m.showHelp {
		if key == "esc" || key == "?" || key == "q" {
			m.showHelp = false
		}
		return m, nil
	}

	// A form takes every key until it is saved or cancelled.
	if m.form != nil {
		return m.formKey(msg)
	}

	// ctrl+b then a key: app actions that work even inside a pane, like
	// tmux. q detaches: everything keeps running on the server.
	if m.prefix {
		m.prefix = false
		m.notice = ""
		if pm, cmd, ok := m.paneKey(key); ok {
			return pm, cmd
		}
		switch key {
		case "ctrl+b": // ctrl+b twice sends one to the focused terminal
			if m.paneFocused() {
				return m.sendToPane(msg)
			}
		case "q", "d":
			return m, tea.Quit
		case "g":
			if m.cfg.Remote != nil {
				m.picker, m.pickIdx = true, 0
			}
		case "n":
			if m.cfg.Remote != nil {
				return m.newSession(), nil
			}
		}
		return m, nil
	}
	if key == "ctrl+b" {
		m.prefix = true
		m.notice = hiStyle.Render("ctrl+b") + dimStyle.Render(" v - new pane · a agent · hjkl move · z zoom · x close · q detach · g sessions · n new")
		return m, nil
	}
	if m.picker {
		return m.pickerKey(key)
	}
	if m.review {
		return m.reviewKey(key)
	}
	if m.cfg.Remote != nil {
		switch key {
		case "ctrl+g":
			m.picker, m.pickIdx = true, 0
			return m, tea.Batch(pollStatus(m.cfg.Remote))
		case "ctrl+n":
			return m.newSession(), nil
		}
	}

	// 3. alt+1…7 opens a section from anywhere. Outside the chat there is
	// nothing to type, so the section takes the keys.
	if s, ok := sectionForKey(key, false); ok {
		return m.goTo(s), nil
	}
	if m.section != SecChat {
		return m.sectionKey(key)
	}

	// The conversation scrolls whatever you are typing.
	switch key {
	case "pgup":
		return m.scrollBy(max(3, m.height-12)), nil
	case "pgdown":
		return m.scrollBy(-max(3, m.height-12)), nil
	}

	// 4. The @mention menu, while it is open.
	if m.popupOpen() {
		switch key {
		case "up":
			m.popupIdx = max(0, m.popupIdx-1)
			return m, nil
		case "down":
			m.popupIdx = min(len(m.candidates())-1, m.popupIdx+1)
			return m, nil
		case "tab", "enter":
			if c := m.commandCandidates(); len(c) > 0 {
				return m.pickCommand(m.popupIdx)
			}
			return m.complete(), nil
		case "esc":
			m.popupDismissed = true
			return m, nil
		}
	}

	// 5. While you are typing, keys are text.
	if len(m.input) > 0 {
		return m.editInput(msg)
	}

	// 6. Empty input: a few keys are commands. Letters stay free for
	// typing, so a question can start with any word; only y, n and v act,
	// and only while an approval is on screen.
	switch key {
	case "ctrl+r":
		if m.last.question != "" && !m.running {
			return m.start(m.last)
		}
		return m, nil
	case "?":
		m.showHelp = true
		return m, nil
	case "tab":
		m.grid = !m.grid
		return m, nil
	case "up":
		if m.solo() {
			return m.scrollBy(3), nil
		}
		m = m.moveSelection(-1)
		return m, nil
	case "down":
		if m.solo() {
			return m.scrollBy(-3), nil
		}
		m = m.moveSelection(+1)
		return m, nil
	case "enter":
		m.session = m.session.toggleOpen(m.selected)
		return m, nil
	case "esc":
		wasRunning := m.running
		m = m.escape()
		if wasRunning && m.session.Outcome == Cancelled {
			return m, m.save()
		}
		return m, nil
	case "y", "n":
		if len(m.queue) > 0 {
			return m.answerApproval(key == "y"), nil
		}
	case "v":
		if m.session.Pending != nil {
			m.showDetails = !m.showDetails
			return m, nil
		}
		if len(m.changes) > 0 && !m.running {
			m.review, m.fileIdx, m.reviewWT = true, 0, 0
			return m, nil
		}
	}
	// Anything else starts typing.
	return m.editInput(msg)
}

// typedText is what a key typed, without control characters: invisible,
// they would turn "/model" into a question or break a file name.
func typedText(msg tea.KeyMsg) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, string(msg.Runes))
}

// editInput changes the text you are typing.
func (m Model) editInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		return m.send()
	case tea.KeyEsc, tea.KeyCtrlU:
		m.input = nil
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	case tea.KeySpace:
		m.input = append(slices.Clone(m.input), ' ')
	case tea.KeyRunes:
		// Clone before append, so the old model's input stays untouched.
		// Control characters (a ctrl key some terminals also send as
		// text) are invisible: kept, they would turn "/model" into a
		// question.
		typed := typedText(msg)
		if typed == "" {
			return m, nil
		}
		m.input = append(slices.Clone(m.input), []rune(typed)...)
	default:
		return m, nil
	}
	m.popupDismissed, m.popupIdx, m.notice = false, 0, ""
	return m, nil
}

// send starts a session from what you typed: "@agent question".
func (m Model) send() (Model, tea.Cmd) {
	if m.running {
		m.notice = badStyle.Render("agents are still working · esc stops them")
		return m, nil
	}
	if text := strings.TrimSpace(string(m.input)); strings.HasPrefix(text, "/") {
		name, arg, _ := strings.Cut(text[1:], " ")
		return m.runCommand(name, strings.TrimSpace(arg))
	}
	r, err := m.parseAsk(string(m.input))
	if err != nil {
		m.notice = badStyle.Render(err.Error())
		return m, nil
	}
	m.input = nil
	m.scroll = 0 // back to the bottom, where the answer comes
	if m.session.started() {
		// A follow-up: the finished session joins the conversation's past,
		// and the agents get it as context.
		m.past = append(slices.Clone(m.past), m.session)
		if len(m.past) > 20 {
			m.past = m.past[len(m.past)-20:]
		}
	}
	return m.start(r)
}

// everyoneHandles all mean "the whole team".
var everyoneHandles = map[string]bool{"todos": true, "all": true, "everyone": true, "time": true}

// parseAsk decides who gets the question:
//
//	"@leo is the pipeline ok?"   starts with a mention: leo
//	"is the pipeline ok? @leo"   mentions an agent anywhere: leo
//	"@todos what do you think?"  everyone answers
//	"is the pipeline ok?"        names no one: the team's routing decides
func (m Model) parseAsk(text string) (request, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return request{}, errors.New("type a question first")
	}
	if strings.HasPrefix(text, "@") {
		name, rest, _ := strings.Cut(text[1:], " ")
		rest = strings.TrimSpace(rest)
		if !everyoneHandles[name] && m.agentByName(name) == nil {
			return request{}, errors.New("no agent named @" + name)
		}
		if rest == "" {
			return request{}, errors.New("write a question after @" + name)
		}
		if everyoneHandles[name] {
			return request{question: rest, everyone: true}, nil
		}
		return request{target: name, question: rest}, nil
	}
	for _, word := range strings.Fields(text) {
		name := strings.TrimRight(strings.TrimPrefix(word, "@"), ".,;:!?")
		if !strings.HasPrefix(word, "@") {
			continue
		}
		if everyoneHandles[name] {
			return request{question: text, everyone: true}, nil
		}
		if m.agentByName(name) != nil {
			return request{target: name, question: text}, nil
		}
	}
	return request{question: text}, nil
}

// candidates are the agents that match the @name being typed, plus
// @todos for the whole team.
func (m Model) candidates() []agent.Agent {
	if c := m.commandCandidates(); c != nil {
		return c
	}
	s := string(m.input)
	if !strings.HasPrefix(s, "@") || strings.ContainsRune(s, ' ') {
		return nil
	}
	var out []agent.Agent
	for _, a := range m.team {
		if strings.HasPrefix(a.Name, s[1:]) {
			out = append(out, a)
		}
	}
	if strings.HasPrefix("todos", s[1:]) {
		out = append(out, agent.Agent{Name: "todos", Role: "everyone answers"})
	}
	return out
}

func (m Model) popupOpen() bool {
	return !m.popupDismissed && len(m.candidates()) > 0
}

// complete replaces the partial @name with the chosen agent.
func (m Model) complete() Model {
	c := m.candidates()
	i := min(m.popupIdx, len(c)-1)
	m.input = []rune("@" + c[i].Name + " ")
	m.popupIdx = 0
	return m
}

func (m Model) moveSelection(delta int) Model {
	n := len(m.session.Turns)
	switch {
	case n == 0:
	case m.selected < 0 && delta < 0:
		m.selected = n - 1 // first press of up selects the latest turn
	case m.selected >= 0:
		m.selected = max(0, min(n-1, m.selected+delta))
	}
	return m
}

// escape backs out of whatever is open, one layer at a time.
func (m Model) escape() Model {
	switch {
	case m.showDetails:
		m.showDetails = false
	case m.selected >= 0:
		m.selected = -1
	case m.grid:
		m.grid = false
	case m.running:
		if m.cfg.Remote != nil && m.remoteID != "" {
			go m.cfg.Remote.Cancel(m.remoteID) // esc stops it on the server too
		}
		m.Stop()
		m.session = m.session.cancelled()
		m.queue = nil
	}
	return m
}

// answerApproval sends your answer to the oldest waiting approval, then
// shows the next one, if another agent is also waiting.
func (m Model) answerApproval(approved bool) Model {
	for i, p := range m.queue {
		if p.routine == "" { // the chat's bar answers the chat's approvals
			return m.answerAt(i, approved)
		}
	}
	return m
}

// answerAt answers approval i of the queue: the inbox can answer them in
// any order.
func (m Model) answerAt(i int, approved bool) Model {
	if i < 0 || i >= len(m.queue) {
		return m
	}
	p := m.queue[i]
	p.reply <- approved // the channel has room for 1, so this never blocks
	m.queue = slices.Delete(slices.Clone(m.queue), i, i+1)
	m.showDetails = false
	if p.routine != "" {
		for _, r := range m.runs {
			if r.name == p.routine {
				r.session.Pending = &Approval{Agent: p.agent, Call: p.call}
				r.session = r.session.approvalAnswered(approved, time.Now())
			}
		}
		return m
	}
	m.session.Pending = &Approval{Agent: p.agent, Call: p.call}
	m.session = m.session.approvalAnswered(approved, time.Now())
	for _, next := range m.queue {
		if next.routine == "" {
			m.session.Pending = &Approval{Agent: next.agent, Call: next.call}
			break
		}
	}
	return m
}

func (m Model) agentByName(name string) *agent.Agent {
	for i := range m.team {
		if m.team[i].Name == name {
			return &m.team[i]
		}
	}
	return nil
}

// Stop cancels the running session, if any.
func (m Model) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

// Ask types text and presses enter. Used by tests and the -ask flag.
func (m Model) Ask(text string) Model {
	m.input = []rune(text)
	m, _ = m.send()
	return m
}

// PressKeys presses keys by name ("tab", "up", "enter", "esc", or text).
// Used by tests and the -keys flag.
func (m Model) PressKeys(keys ...string) Model {
	named := map[string]tea.KeyType{
		"tab": tea.KeyTab, "enter": tea.KeyEnter, "esc": tea.KeyEsc, "ctrl+r": tea.KeyCtrlR,
		"up": tea.KeyUp, "down": tea.KeyDown, "backspace": tea.KeyBackspace,
		"space": tea.KeySpace, "shift+tab": tea.KeyShiftTab,
		"left": tea.KeyLeft, "right": tea.KeyRight, "ctrl+s": tea.KeyCtrlS, "ctrl+u": tea.KeyCtrlU, "ctrl+n": tea.KeyCtrlN, "ctrl+b": tea.KeyCtrlB, "ctrl+g": tea.KeyCtrlG,
	}
	for _, k := range keys {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		if t, ok := named[k]; ok {
			msg = tea.KeyMsg{Type: t}
		}
		if rest, ok := strings.CutPrefix(k, "alt+"); ok && rest != "" {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(rest), Alt: true}
		}
		next, _ := m.Update(msg)
		m = next.(Model) // type assertion: tea.Model back to our Model
	}
	return m
}

// Settle reads engine events directly, without Bubble Tea, until the
// session pauses for approval, ends, or limit events were read (0 means
// no limit). It blocks, so it is only for tests and snapshots.
func (m Model) Settle(limit int) Model {
	for n := 0; m.running && len(m.queue) == 0 && (limit == 0 || n < limit); n++ {
		e, ok := <-m.events
		if !ok {
			m.running = false
			break
		}
		m = m.handle(e)
	}
	return m
}

// Snapshot renders a single frame at a fixed size, without a terminal.
func (m Model) Snapshot(width, height int) string {
	m.width, m.height = width, height
	return m.View()
}
