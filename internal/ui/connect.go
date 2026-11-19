package ui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tubas/internal/agent"
	"tubas/internal/auth"
	"tubas/internal/engine"
)

// ── /connect ────────────────────────────────────────────────────────────
//
// /connect lists the model providers and how each is reached: your
// company gateway (Claude settings), an environment variable, a key you
// saved here, or nothing yet. Pick one and paste its key: tubas checks it
// by listing the provider's models, then keeps it in the system's
// keychain (a file only you can read where there is none). Your own
// OpenAI-compatible server is added by its address. Opened by itself the
// first time, when no model can be reached.

type connectStage int

const (
	connList  connectStage = iota
	connKey                // pasting a key
	connURL                // your own server: its address
	connModel              // your own server: its model
	connChecking
)

type connectScreen struct {
	rows   []engine.Connection
	idx    int
	stage  connectStage
	input  []rune
	target string // the provider being connected
	url    string // your own server's address
	model  string // and its model
	err    string
	forget string // a provider waiting for a second d
	first  bool   // opened because nothing can be reached yet
}

// connectCheckedMsg is how a key check ended.
type connectCheckedMsg struct {
	provider, key string
	custom        *auth.Custom
	models        []string
	err           error
}

// providerSpecs are the providers tubas knows now: the team's, the
// built-in ones and yours.
func (m Model) providerSpecs() map[string]engine.ProviderSpec {
	specs := maps.Clone(m.cfg.ProviderSpecs)
	if specs == nil {
		specs = engine.Providers(nil)
	}
	for name, c := range auth.Customs() {
		specs[name] = engine.ProviderSpec{Name: name, OpenAI: c.OpenAI, Models: c.Models}
	}
	return specs
}

func (m Model) connections() []engine.Connection {
	return engine.Connections(engine.FindProvider(m.cfg.WorkDir), m.providerSpecs())
}

// openConnect shows /connect, on the provider the assistant uses.
func (m Model) openConnect(first bool) Model {
	c := &connectScreen{rows: m.connections(), first: first}
	now := "anthropic"
	if a := m.agentByName(m.pickerAgent()); a != nil && a.Provider != "" {
		now = a.Provider
	}
	for i, r := range c.rows {
		if r.Name == now {
			c.idx = i
		}
	}
	m.connect = c
	m.section = SecChat
	return m
}

// addRow is the last line: your own server.
func (c *connectScreen) addRow() int { return len(c.rows) }

func (m Model) connectKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	c := *m.connect
	key := msg.String()
	if key == "ctrl+c" || (key == "esc" && c.stage == connList) {
		m.connect = nil
		return m, nil
	}
	switch c.stage {
	case connChecking:
		if key == "esc" {
			c.stage, c.err = connKey, ""
		}
	case connList:
		if key != "d" {
			c.forget = ""
		}
		switch key {
		case "up", "k":
			c.idx = max(0, c.idx-1)
		case "down", "j":
			c.idx = min(c.addRow(), c.idx+1)
		case "enter":
			return m.connectPick(c)
		case "d", "delete":
			return m.connectForget(c)
		}
	default:
		switch msg.Type {
		case tea.KeyEsc:
			c.stage, c.input, c.err = connList, nil, ""
			if c.target != "" && c.url != "" { // your own server: back to its address
				c.stage, c.input = connURL, []rune(c.url)
			}
		case tea.KeyBackspace:
			if len(c.input) > 0 {
				c.input = c.input[:len(c.input)-1]
			}
		case tea.KeyEnter:
			return m.connectNext(c)
		case tea.KeyRunes, tea.KeySpace:
			c.input = append(c.input, []rune(strings.TrimSpace(typedText(msg)))...)
			c.err = ""
		}
	}
	m.connect = &c
	return m, nil
}

// connectPick acts on the chosen line.
func (m Model) connectPick(c connectScreen) (Model, tea.Cmd) {
	c.err = ""
	if c.idx == c.addRow() {
		c.stage, c.target, c.url, c.model, c.input = connURL, "", "", "", nil
		m.connect = &c
		return m, nil
	}
	row := c.rows[c.idx]
	switch row.State {
	case engine.ConnGateway:
		m.notice = okStyle.Render("✓ " + row.Name + " goes through " + row.Detail + ": nothing to connect")
		m.connect = &c
		return m, nil
	case engine.ConnFree: // a model on this computer: see that it answers
		c.target, c.stage = row.Name, connChecking
		m.connect = &c
		return m, checkKey(m.providerSpecs()[row.Name], "", nil)
	}
	c.target, c.stage, c.input, c.url = row.Name, connKey, nil, ""
	m.connect = &c
	return m, nil
}

// connectNext moves on from a text field: the address, the model, the key.
func (m Model) connectNext(c connectScreen) (Model, tea.Cmd) {
	text := strings.TrimSpace(string(c.input))
	switch c.stage {
	case connURL:
		u, err := url.Parse(text)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			c.err = "give the full address, like http://localhost:8000/v1"
			break
		}
		c.url, c.target = strings.TrimRight(text, "/"), customName(u.Hostname(), m.providerSpecs())
		c.stage, c.input = connModel, nil
	case connModel:
		if text == "" {
			c.err = "give the model's name, as the server calls it"
			break
		}
		c.model, c.stage, c.input = text, connKey, nil
	case connKey:
		if text == "" && c.url == "" {
			c.err = "paste the key"
			break
		}
		spec := m.providerSpecs()[c.target]
		var custom *auth.Custom
		if c.url != "" {
			custom = &auth.Custom{OpenAI: c.url, Models: []string{c.model}}
			spec = engine.ProviderSpec{Name: c.target, OpenAI: c.url}
		}
		if c.target == engine.Anthropic {
			spec = engine.ProviderSpec{Name: engine.Anthropic}
		}
		c.stage = connChecking
		m.connect = &c
		return m, checkKey(spec, text, custom)
	}
	m.connect = &c
	return m, nil
}

// customName names your own server after its host: localhost, my-gpu…
func customName(host string, taken map[string]engine.ProviderSpec) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return '-'
	}, strings.TrimPrefix(host, "www."))
	name = strings.Trim(name, "-")
	if name == "" {
		name = "custom"
	}
	if _, builtin := engine.BuiltinProviders[name]; builtin || name == engine.Anthropic {
		name += "-own"
	}
	_ = taken // a server you add again under the same host replaces it
	return name
}

// checkKey lists the provider's models with the key, off the screen's
// goroutine.
func checkKey(spec engine.ProviderSpec, key string, custom *auth.Custom) tea.Cmd {
	return func() tea.Msg {
		models, err := engine.CheckKey(context.Background(), spec, key)
		return connectCheckedMsg{provider: spec.Name, key: key, custom: custom, models: models, err: err}
	}
}

// connectChecked saves a key that works, and says what it can do now.
func (m Model) connectChecked(msg connectCheckedMsg) (Model, tea.Cmd) {
	if m.connect == nil {
		return m, nil
	}
	c := *m.connect
	if errors.Is(msg.err, engine.ErrKeyRefused) {
		c.stage, c.input, c.err = connKey, nil, msg.provider+" refused this key: copy it again, whole"
		m.connect = &c
		return m, nil
	}
	if msg.custom == nil && msg.key == "" && msg.err != nil { // a model on this computer that doesn't answer
		c.stage, c.err = connList, msg.provider+" isn't answering: "+firstLineText(msg.err.Error())
		m.connect = &c
		return m, nil
	}
	if msg.custom != nil {
		if err := auth.AddCustom(msg.provider, *msg.custom); err != nil {
			c.stage, c.err = connKey, "could not save: "+err.Error()
			m.connect = &c
			return m, nil
		}
	}
	where := ""
	if msg.key != "" {
		w, err := auth.Set(msg.provider, msg.key)
		if err != nil {
			c.stage, c.err = connKey, "could not save the key: "+err.Error()
			m.connect = &c
			return m, nil
		}
		where = " · key kept in " + auth.Where(w)
	}
	models := msg.models
	if msg.custom != nil && len(models) == 0 {
		models = msg.custom.Models
	}
	if len(models) > 0 {
		if m.cfg.Providers == nil {
			m.cfg.Providers = map[string][]string{}
		}
		m.cfg.Providers[msg.provider] = models
	}
	note := fmt.Sprintf("✓ %s connected · %d models%s", msg.provider, len(models), where)
	if msg.err != nil {
		note = fmt.Sprintf("✓ %s saved, but its models weren't listed (%s)%s", msg.provider, firstLineText(msg.err.Error()), where)
	}
	m, moved := m.useIfStranded(msg.provider, msg.custom)
	if moved != "" {
		note += " · " + moved
	}
	m.notice = okStyle.Render(note)
	first := c.first
	c = connectScreen{rows: m.connections(), first: first}
	for i, r := range c.rows {
		if r.Name == msg.provider {
			c.idx = i
		}
	}
	if first { // done: the chat is ready
		m.connect = nil
		return m, nil
	}
	m.connect = &c
	return m, nil
}

// useIfStranded points the assistant at the provider you just connected
// when its own can't be reached: connecting is all it takes to start.
func (m Model) useIfStranded(provider string, custom *auth.Custom) (Model, string) {
	name := m.pickerAgent()
	a := m.agentByName(name)
	if a == nil || m.cfg.TeamDir == "" {
		return m, ""
	}
	current := orDefault(a.Provider, engine.Anthropic)
	if current == provider {
		return m, ""
	}
	for _, r := range m.connections() {
		if r.Name == current && r.Ready() {
			return m, "" // it already works: keep it
		}
	}
	model := ""
	if spec, ok := engine.Spec(m.providerSpecs(), provider); ok && len(spec.Models) > 0 && provider != engine.Anthropic {
		model = spec.Models[0]
	}
	if custom != nil {
		model = custom.Models[0]
	}
	if provider == engine.Anthropic {
		model = "sonnet"
	}
	err := m.editAgent(name, func(a *agent.Agent) {
		a.Provider, a.Model = provider, model
		if provider == engine.Anthropic {
			a.Provider = ""
		}
	})
	if err != nil {
		return m, ""
	}
	m = m.reloadTeam()
	return m, name + " now answers with " + orDefault(model, provider)
}

// connectForget removes a saved key (or your own server) on a second d.
func (m Model) connectForget(c connectScreen) (Model, tea.Cmd) {
	if c.idx >= len(c.rows) {
		m.connect = &c
		return m, nil
	}
	row := c.rows[c.idx]
	if row.State != engine.ConnSaved && !row.Custom {
		c.err = row.Name + " has no key saved here"
		if row.State == engine.ConnEnv {
			c.err = row.Name + "'s key comes from " + row.Detail + ": unset it there"
		}
		m.connect = &c
		return m, nil
	}
	if c.forget != row.Name {
		c.forget, c.err = row.Name, ""
		m.connect = &c
		return m, nil
	}
	var err error
	if row.Custom {
		err = auth.RemoveCustom(row.Name)
	} else {
		err = auth.Delete(row.Name)
	}
	if err != nil {
		c.err = "could not forget it: " + err.Error()
	} else {
		m.notice = okStyle.Render("✓ " + row.Name + "'s key forgotten")
		c.rows, c.forget = m.connections(), ""
		c.idx = min(c.idx, c.addRow())
	}
	m.connect = &c
	return m, nil
}

// keyPages are where each provider gives out keys.
var keyPages = map[string]string{
	"anthropic":  "console.anthropic.com/settings/keys",
	"openai":     "platform.openai.com/api-keys",
	"openrouter": "openrouter.ai/keys",
	"gemini":     "aistudio.google.com/apikey",
	"deepseek":   "platform.deepseek.com/api_keys",
}

// connectHead is the top of the screen, above the list, cw wide.
func (m Model) connectHead(cw int) []string {
	if !m.connect.first {
		return []string{boldStyle.Render("connect") + dimStyle.Render(" · where agents' models come from"), ""}
	}
	lines := []string{boldStyle.Render("Welcome to tubas"), "",
		textStyle.Render("Agents need a model to talk to. Pick a provider and paste its key:"),
		textStyle.Render("tubas checks it and keeps it on this computer."), ""}
	if note := m.claudeLoginNote(); note != "" {
		for _, l := range wrap(note, cw) {
			lines = append(lines, dimStyle.Render(l))
		}
		lines = append(lines, "")
	}
	return lines
}

func (m Model) viewConnect(cw, h int) []string {
	c := m.connect
	lines := m.connectHead(cw)
	switch c.stage {
	case connList:
		lines = append(lines, m.connectRows(cw)...)
		if c.err != "" {
			lines = append(lines, "", badStyle.Render(c.err))
		}
		if c.forget != "" {
			lines = append(lines, "", warnStyle.Render("d again forgets "+c.forget+"'s key"))
		}
		hint := "↑↓ choose · enter connects · d forgets a saved key · esc closes"
		if c.first {
			hint = "↑↓ choose · enter connects · esc: later"
		}
		lines = append(lines, "", dimStyle.Render(hint), dimStyle.Render("Keys stay in "+auth.Destination()+", never in the team folder."))
	default:
		lines = append(lines, m.connectField(c)...)
	}
	return fitHeight(lines, h, false)
}

// connectRows are the providers, each with how it's reached.
func (m Model) connectRows(cw int) []string {
	c := m.connect
	var lines []string
	for i, r := range c.rows {
		mark, state := dimStyle.Render("·"), dimStyle.Render("not connected")
		switch r.State {
		case engine.ConnGateway:
			mark, state = okStyle.Render("✓"), textStyle.Render(r.Detail)
		case engine.ConnEnv:
			mark, state = okStyle.Render("✓"), textStyle.Render("key from "+r.Detail)
		case engine.ConnSaved:
			mark, state = okStyle.Render("✓"), textStyle.Render("key saved")
		case engine.ConnFree:
			mark, state = dimStyle.Render("○"), dimStyle.Render("no key needed · "+r.Detail)
		}
		name := fmt.Sprintf("%-12s", r.Name)
		if i == c.idx {
			lines = append(lines, fit(barBold.Render(" "+name+" ")+" "+mark+" "+state, cw))
		} else {
			lines = append(lines, fit("  "+textStyle.Render(name)+"  "+mark+" "+state, cw))
		}
	}
	add := "+ your own server (OpenAI-compatible address)"
	if c.idx == c.addRow() {
		lines = append(lines, barBold.Render(" "+add+" "))
	} else {
		lines = append(lines, "  "+hiStyle.Render(add))
	}
	return lines
}

// connectField is the text being typed: an address, a model, a key.
func (m Model) connectField(c *connectScreen) []string {
	var lines []string
	shown := string(c.input)
	switch c.stage {
	case connURL:
		lines = append(lines, textStyle.Render("Your server's address (OpenAI-compatible: vLLM, LM Studio, LiteLLM…)"))
	case connModel:
		lines = append(lines, textStyle.Render("The model to use, as "+c.url+" calls it"))
	case connKey, connChecking:
		what := "Paste " + c.target + "'s key"
		if c.url != "" {
			what = "Its key, if it needs one (enter with nothing: none)"
		}
		lines = append(lines, textStyle.Render(what))
		if page := keyPages[c.target]; page != "" {
			lines = append(lines, dimStyle.Render("get one at "+page))
		}
		shown = strings.Repeat("•", len(c.input)) // never on screen
		if n := len(c.input); n > 4 {
			shown = strings.Repeat("•", n-4) + string(c.input[n-4:])
		}
	}
	lines = append(lines, "", hiStyle.Render("› ")+textStyle.Render(shown)+barStyle.Render(" "), "")
	switch {
	case c.stage == connChecking:
		lines = append(lines, hiStyle.Render(spinner[m.frame%len(spinner)]+" checking with "+c.target+"…"))
	case c.err != "":
		lines = append(lines, badStyle.Render(c.err))
	}
	lines = append(lines, "", dimStyle.Render("enter: next · esc: back"))
	return lines
}

// claudeLoginNote explains, on the first screen, why a Claude Code login
// isn't enough.
func (m Model) claudeLoginNote() string {
	for _, d := range m.providers {
		if d.Name == "claude-login" {
			return "Your Claude Code login works only inside Claude Code: Anthropic doesn't allow it in other tools. An API key (anthropic) or another provider works here."
		}
	}
	return ""
}

// stranded says whether no agent can reach a model: the first screen.
func (m Model) stranded() bool {
	if len(m.team) == 0 || m.cfg.TeamDir == "" {
		return false
	}
	ready := map[string]bool{}
	for _, r := range m.connections() {
		ready[r.Name] = r.Ready()
	}
	for _, a := range m.team {
		if ready[orDefault(a.Provider, engine.Anthropic)] {
			return false
		}
	}
	return true
}

// clickConnect picks a provider with the mouse.
func (m Model) clickConnect(msg tea.MouseMsg, by int) (Model, tea.Cmd) {
	c := *m.connect
	if c.stage != connList {
		return m.clickHint(msg)
	}
	if i := by - len(m.connectHead(m.width-2)); i >= 0 && i <= c.addRow() {
		c.idx = i
		m.connect = &c
		return m.connectPick(c)
	}
	return m.clickHint(msg)
}
