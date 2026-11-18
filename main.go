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
	engineName := flag.String("engine", "native", "who runs the agents: native or fake")
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
	cfg.Providers = map[string][]string{}
	cfg.ProviderSpecs = mcp.TeamProviders(t)
	for name, spec := range cfg.ProviderSpecs {
		cfg.Providers[name] = spec.Models
	}
	cfg.Notify = t.Notify

	// Sessions and routines live in a background server, so closing this
	// window (or ctrl+b q) leaves them running. -local, the fake engine and
	// snapshots keep everything in this process.
	if !*local && *snapshot == "" && (*engineName == "native" || *engineName == "claude") {
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

// nativeEngine wires tubas's own harness: the approval broker, the
// gateway for the team's connectors, your providers and keys.
func nativeEngine(t team.Team, model string) (engine.NativeEngine, func(), error) {
	exe, err := os.Executable() // the gateway is this same binary: tubas mcp
	if err != nil {
		return engine.NativeEngine{}, nil, err
	}
	broker, err := engine.NewBroker()
	if err != nil {
		return engine.NativeEngine{}, nil, err
	}
	providers := mcp.TeamProviders(t)
	prices := map[string]engine.Price{}
	for _, spec := range providers {
		for model, p := range spec.Prices {
			prices[model] = p
		}
	}
	return engine.NativeEngine{
		Setup:           engine.FindProvider(t.Dir), // a company gateway in your Claude settings
		Providers:       providers,
		DefaultProvider: t.Provider,
		WorkDir:         t.Dir,
		Gateway:         mcp.GatewayLauncher(exe, t.Dir, broker.URL, broker.Token),
		Approvals:       broker,
		Prices:          prices,
		AllowCommands:   t.AllowCommands,
		Model:           model,
	}, func() { broker.Close() }, nil
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
