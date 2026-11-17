// tubas: a team of agents in the terminal that call each other by @mention.
//
//	tubas -team DIR              open the team in DIR (default: this folder)
//	tubas -team DIR -engine fake same interface, scripted agents, no model calls
//	tubas -team DIR -resume      reopen the team's last conversation
//	tubas init DIR               create a new team to start from
//	tubas seed -team DIR         recreate the team's example databases
//	tubas mcp -team DIR -agent A the tool gateway; claude starts it, not you
//	tubas status | stop | session new | wait | read   see cli.go
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"tubas/internal/engine"
	"tubas/internal/mcp"
	"tubas/internal/pane"
	"tubas/internal/repo"
	"tubas/internal/server"
	"tubas/internal/team"
	"tubas/internal/ui"
)

func main() {
	engine.AdoptPath() // agent CLIs where installers put them, and your login shell's PATH
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mcp":
			runMCP(os.Args[2:])
			return
		case "seed":
			runSeed(os.Args[2:])
			return
		case "init":
			dir := "."
			if len(os.Args) > 2 {
				dir = os.Args[2]
			}
			if err := team.Init(dir); err != nil {
				fail("init: %v", err)
			}
			fmt.Printf("created a team in %s\nrun it with: tubas -team %s\n", dir, dir)
			return
		case "serve":
			runServe(os.Args[2:])
			return
		case "status":
			runStatus(os.Args[2:])
			return
		case "stop":
			runStop(os.Args[2:])
			return
		case "session":
			runSession(os.Args[2:])
			return
		case "wait":
			runWait(os.Args[2:])
			return
		case "read":
			runRead(os.Args[2:])
			return
		case "answer":
			runAnswer(os.Args[2:])
			return
		case "pane":
			runPane(os.Args[2:])
			return
		case "doctor":
			runDoctor(os.Args[2:])
			return
		}
	}

	teamDir := flag.String("team", ".", "the team folder (with team.yaml and agents/)")
	engineName := flag.String("engine", "claude", "who runs the agents: claude or fake")
	model := flag.String("model", "", "use this model for every agent (e.g. haiku), ignoring their files")
	ask := flag.String("ask", "", `start right away with a question, e.g. -ask "@iris ..."`)
	snapshot := flag.String("snapshot", "", "print one frame at WIDTHxHEIGHT (e.g. 128x32) and exit")
	events := flag.Int("events", 0, "with -snapshot: stop after this many events (0 = until it pauses)")
	keys := flag.String("keys", "", `with -snapshot: keys to press first, e.g. "up up enter" or "tab"`)
	resume := flag.Bool("resume", false, "reopen the team's last conversation")
	local := flag.Bool("local", false, "run everything in this window, without the background server")
	mouse := flag.Bool("mouse", true, "use the mouse (click, drag borders, scroll, select); -mouse=false leaves it to your terminal")
	flag.Parse()

	t, err := team.Load(findTeam(*teamDir))
	if err != nil {
		fail("%v", err)
	}
	if err := t.Prepare(false); err != nil {
		fail("%v", err)
	}

	eng, router, closeEngine := buildEngine(t, *engineName, *model, *snapshot != "")
	defer closeEngine()

	cfg := ui.Config{Name: t.Name, Example: t.Example, Lead: t.Lead, MaxHops: t.MaxHops,
		Connectors: t.ConnectorNames(), Routing: t.Routing, Router: router,
		TeamDir: t.Dir, ReportsDir: reportsDir(t), ConnectorKinds: connectorKinds(t), Repos: repoSpecs(t)}
	cfg.WorkDir, _ = os.Getwd()
	if by, ok := eng.(engine.ByHarness); ok {
		cfg.Harnesses = by.Names()
	}
	cfg.Providers = map[string][]string{}
	for name, spec := range teamProviders(t) {
		cfg.Providers[name] = spec.Models
	}
	cfg.Notify = t.Notify

	// Sessions and routines live in a background server, so closing this
	// window (or ctrl+b q) leaves them running. -local, the fake engine and
	// snapshots keep everything in this process.
	if !*local && *snapshot == "" && *engineName == "claude" {
		exe, _ := os.Executable()
		var serveArgs []string
		if *model != "" {
			serveArgs = append(serveArgs, "-model", *model)
		}
		if c, _, err := server.Ensure(t.Dir, exe, serveArgs...); err == nil {
			c.Window = true
			cfg.Remote = c
			if c.Outdated {
				cfg.Notice = "tubas was updated, but the background server still runs the old one (something is running in it) · tubas stop, then tubas"
			}
		} else {
			fmt.Fprintf(os.Stderr, "tubas: %v · running in this window only\n", err)
		}
	}
	// Terminal panes live where sessions do: in the server, or here.
	if *snapshot == "" {
		if cfg.Remote != nil {
			cfg.Panes = cfg.Remote.Panes()
		} else {
			localPanes := pane.NewManager(t.Dir)
			localPanes.Resolve = server.Resolve
			cfg.Panes = localPanes
			defer localPanes.CloseAll()
		}
	}

	// Conversations are saved in the team folder, under .tubas/history.
	historyDir := filepath.Join(t.Dir, ".tubas", "history")
	if *resume {
		path, err := ui.LatestHistory(historyDir)
		if err != nil {
			fail("resume: %v", err)
		}
		if cfg.Past, err = ui.LoadHistory(path); err != nil {
			fail("resume: %v", err)
		}
		cfg.HistoryFile = path // keep adding to the same conversation
	} else if *snapshot == "" {
		cfg.HistoryFile = ui.NewHistoryFile(historyDir)
	}

	if *snapshot != "" {
		var w, h int
		if _, err := fmt.Sscanf(*snapshot, "%dx%d", &w, &h); err != nil {
			fail("use -snapshot WIDTHxHEIGHT, e.g. 128x32")
		}
		m := ui.New(t.Agents, eng, cfg)
		if *ask != "" {
			m = m.Ask(*ask).Settle(*events)
		}
		if *keys != "" {
			m = m.PressKeys(strings.Fields(*keys)...)
			if *events == 0 {
				m = m.Settle(0) // let the agents react (after y, n or esc)
			}
		}
		defer m.Stop()
		// Output is not a terminal here, so Lip Gloss would drop the
		// colors. Force full color.
		lipgloss.SetColorProfile(termenv.TrueColor)
		fmt.Println(m.Snapshot(w, h))
		return
	}

	tui := ui.New(t.Agents, eng, cfg)
	if *ask != "" {
		tui = tui.Ask(*ask)
	}
	// WithAltScreen uses the terminal's alternate screen, like vim or
	// htop: on exit, your previous terminal content comes back.
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if *mouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	if _, err := tea.NewProgram(tui, opts...).Run(); err != nil {
		fail("error: %v", err)
	}
	if cfg.Remote != nil {
		detached(cfg.Remote)
	}
}

// detached says what keeps running after the window closes.
func detached(c *server.Client) {
	st, err := c.Status()
	if err != nil {
		return
	}
	fmt.Println(ui.Tubaleia())
	fmt.Println()
	fmt.Println("tubas detached · everything keeps running in the background")
	line := "  " + rollup(st.Sessions)
	if n := len(st.Panes); n > 0 {
		line += fmt.Sprintf(" · %d panes", n)
	}
	for _, r := range st.Routines {
		if r.On && !r.Next.IsZero() {
			line += " · routine " + r.Name + " next " + r.Next.Format("Mon 15:04")
			break
		}
	}
	fmt.Println(line)
	fmt.Println("  tubas comes back · tubas status shows this · tubas stop ends it")
}

// connectorKinds says what runs each connector, for the connectors section.
func connectorKinds(t team.Team) map[string]string {
	kinds := map[string]string{}
	for name, c := range t.Connectors {
		switch {
		case c.Repo != "":
			kinds[name] = "repo · " + t.Path(c.Repo) + " · a worktree per task"
		case c.Builtin == "sqlite":
			kinds[name] = "sqlite · " + c.DB
		case c.Builtin == "reports":
			kinds[name] = "built in · writes to " + reportsDir(t) + "/"
		case c.Builtin != "":
			kinds[name] = "built in · each agent's folder and shared/"
		default:
			kinds[name] = "mcp · " + strings.TrimSpace(c.Command+" "+strings.Join(c.Args, " "))
		}
	}
	return kinds
}

// repoSpecs are the team's repositories, for worktrees.
func repoSpecs(t team.Team) map[string]repo.Spec {
	out := map[string]repo.Spec{}
	for name, r := range t.Repos() {
		out[name] = repo.Spec{Path: r.Path, Base: r.Base, Setup: r.Setup}
	}
	return out
}

// reportsDir is where the reports connector writes, if the team has one.
func reportsDir(t team.Team) string {
	for _, c := range t.Connectors {
		if c.Builtin == "reports" {
			if c.Dir == "" {
				return "reports"
			}
			return c.Dir
		}
	}
	return ""
}

// claudeEngine wires the real engine: the approval broker and the tool
// gateway each agent gets.
func claudeEngine(t team.Team, model string) (engine.ClaudeEngine, func(), error) {
	// Without Claude Code installed, command stays "" and claude agents
	// run on the api harness, through your Claude settings (see harnesses).
	command, _ := engine.FindClaude()
	exe, err := os.Executable() // claude will start this same binary as `tubas mcp`
	if err != nil {
		return engine.ClaudeEngine{}, nil, err
	}
	broker, err := engine.NewBroker()
	if err != nil {
		return engine.ClaudeEngine{}, nil, err
	}
	provider := engine.FindProvider(t.Dir) // a work gateway, Bedrock or Vertex set in your Claude settings
	plan := false
	if command != "" {
		_, plan, _ = engine.ClaudeAuth(command)
	}
	return engine.ClaudeEngine{
		Command:         command,
		Model:           model,
		WorkDir:         t.Dir, // agents run inside the team folder
		Tools:           mcp.ClaudeTools(exe, t.Dir, broker.URL, broker.Token),
		Approvals:       broker,
		Provider:        provider,
		Providers:       teamProviders(t),
		DefaultProvider: t.Provider,
		Plan:            plan && provider.Label() == "", // a gateway or Bedrock bills per token
	}, func() { broker.Close() }, nil
}

// harnesses are the engines this computer can run, claude first: an
// agent's harness: field picks one (tucano and t3code do the same:
// offer what is installed).
func harnesses(t team.Team, claude engine.ClaudeEngine, model string) engine.ByHarness {
	byHarness := engine.ByHarness{"claude": claude}
	exe, _ := os.Executable()
	gateway := mcp.GatewayLauncher(exe, t.Dir, claude.Approvals.URL, claude.Approvals.Token)
	if codex, err := engine.FindCodex(); err == nil {
		_, plan := engine.CodexLogin(codex)
		byHarness["codex"] = engine.CodexEngine{Command: codex, WorkDir: t.Dir, Gateway: gateway,
			Approvals: claude.Approvals, Plan: plan, Model: codexModel(model)}
	}
	// The api harness needs no program: tubas runs the loop itself.
	prices := map[string]engine.Price{}
	for _, spec := range claude.Providers {
		for model, p := range spec.Prices {
			prices[model] = p
		}
	}
	api := engine.APIEngine{Setup: claude.Provider, Providers: claude.Providers, DefaultProvider: t.Provider, WorkDir: t.Dir,
		Gateway: gateway, Approvals: claude.Approvals, Prices: prices}
	byHarness["api"] = api
	if claude.Command == "" {
		// No Claude Code here (a work laptop with only a gateway in the
		// Claude settings, say): claude agents run on tubas's own loop,
		// through that gateway, unchanged.
		byHarness["claude"] = api
	}
	acp := map[string]string{}
	for name, h := range t.Harnesses {
		acp[name] = h.ACP
	}
	for _, a := range engine.FindACP(acp) {
		if _, taken := byHarness[a.Name]; !taken {
			byHarness[a.Name] = engine.ACPEngine{Agent: a, WorkDir: t.Dir, Gateway: gateway, Approvals: claude.Approvals}
		}
	}
	return byHarness
}

// codexModel is -model for codex: only when it is not one of Claude's.
func codexModel(m string) string {
	switch m {
	case "", "haiku", "sonnet", "opus":
		return ""
	}
	return m
}

// teamProviders are the built-in model providers with team.yaml's.
func teamProviders(t team.Team) map[string]engine.ProviderSpec {
	own := map[string]engine.ProviderSpec{}
	for name, p := range t.Providers {
		prices := map[string]engine.Price{}
		for model, pr := range p.Prices {
			prices[model] = engine.Price{Input: pr[0], Cached: pr[1], Output: pr[2]}
		}
		own[name] = engine.ProviderSpec{Anthropic: p.Anthropic, OpenAI: p.OpenAI, KeyEnv: p.KeyEnv, Models: p.Models, Prices: prices}
	}
	return engine.Providers(own)
}

// runMCP is the tool gateway for one agent turn, started by claude.
func runMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	agentName := fs.String("agent", "", "the agent this gateway works for")
	fs.Parse(args)

	t, err := team.Load(*teamDir)
	if err != nil {
		fail("mcp: %v", err)
	}
	g, err := mcp.NewGateway(t, *agentName, os.Getenv("TUBAS_APPROVAL_URL"), os.Getenv("TUBAS_APPROVAL_TOKEN"))
	if err != nil {
		fail("mcp: %v", err)
	}
	defer g.Close()
	// stdout is the protocol channel: only JSON-RPC may be written there.
	if err := g.Serve(os.Stdin, os.Stdout); err != nil {
		fail("mcp: %v", err)
	}
}

func runSeed(args []string) {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	fs.Parse(args)
	t, err := team.Load(*teamDir)
	if err != nil {
		fail("%v", err)
	}
	if err := t.Prepare(true); err != nil {
		fail("%v", err)
	}
	fmt.Println("databases recreated for team", t.Name)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
