package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"tubas/internal/engine"
	"tubas/internal/server"
	"tubas/internal/team"
)

// The background server and the commands that talk to it:
//
//	tubas serve -team DIR            the server itself; tubas starts it for you
//	tubas status -team DIR           what runs, what waits for you
//	tubas stop -team DIR             stop the server and every session
//	tubas session new -team DIR [-agent A] "question"
//	tubas wait -team DIR ID [-until blocked,done] [-timeout 10m]
//	tubas read -team DIR ID [-last]
//	tubas answer -team DIR APPROVAL-ID yes|no
//
// Everything prints JSON except status and read, so scripts and other
// agents can drive tubas.

// buildEngine wires the engine and router the way the screen and the
// server both use them.
func buildEngine(t team.Team, name, model string, fast bool) (engine.Engine, engine.Router, func()) {
	switch name {
	case "fake":
		fake := engine.FakeEngine{Delay: 350 * time.Millisecond, Scripts: engine.DemoScripts()}
		if fast {
			fake.Delay = 0
		}
		return fake, engine.RuleRouter{}, func() {}
	case "claude":
		claude, closeBroker, err := claudeEngine(t, model)
		if err != nil {
			fail("%v", err)
		}
		// The router is one cheap call: a small model scores the agents,
		// on the team's provider.
		var router engine.Router = engine.RuleRouter{} // without claude, rules pick who answers
		if claude.Command != "" {
			cr := engine.ClaudeRouter{Command: claude.Command, Model: "haiku", WorkDir: t.Dir, Provider: claude.Provider}
			if spec, ok := claude.Providers[t.Provider]; ok {
				if p, err := spec.ClaudeProvider(spec.FastModel()); err == nil {
					cr.Provider, cr.Model = claude.Provider.With(p), p.Env["ANTHROPIC_MODEL"]
				}
			}
			router = cr
		}
		return harnesses(t, claude, model), router, closeBroker
	}
	fail("unknown engine %q: use claude or fake", name)
	return nil, nil, nil
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	engineName := fs.String("engine", "claude", "claude or fake")
	model := fs.String("model", "", "use this model for every agent")
	fs.Parse(args)
	t, err := team.Load(*teamDir)
	if err != nil {
		fail("serve: %v", err)
	}
	if err := t.Prepare(false); err != nil {
		fail("serve: %v", err)
	}
	eng, router, closeEngine := buildEngine(t, *engineName, *model, false)
	defer closeEngine()
	s := server.New(server.Options{TeamDir: t.Dir, Engine: eng, Router: router,
		Load: func() (team.Team, error) { return team.Load(t.Dir) }})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Printf("%s tubas server for %s\n", time.Now().Format(time.RFC3339), t.Dir)
	if err := s.Run(ctx); err != nil {
		fail("serve: %v", err)
	}
}

// client finds the team's server; with start, it starts one if needed.
func client(teamDir string, start bool) *server.Client {
	t, err := team.Load(findTeam(teamDir))
	if err != nil {
		fail("%v", err)
	}
	if !start {
		c, err := server.Discover(t.Dir)
		if err != nil {
			fail("%v · open tubas to start it", err)
		}
		return c
	}
	exe, err := os.Executable()
	if err != nil {
		fail("%v", err)
	}
	c, _, err := server.Ensure(t.Dir, exe)
	if err != nil {
		fail("%v", err)
	}
	return c
}

var stateMark = map[string]string{server.Blocked: "◐", server.Working: "⠹", server.Done: "●", server.Idle: "○"}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	c := client(*teamDir, false)
	st, err := c.Status()
	if err != nil {
		fail("%v", err)
	}
	if *asJSON {
		printJSON(st)
		return
	}
	fmt.Printf("%s  %s · server up %s\n", st.Team, st.Dir, time.Since(st.Started).Round(time.Second))
	fmt.Println("  " + rollup(st.Sessions))
	for _, s := range st.Sessions {
		if s.State == server.Idle && time.Since(s.Updated) > 24*time.Hour {
			continue
		}
		what := s.State
		switch s.State {
		case server.Blocked:
			what = "blocked: " + s.Waiting
		case server.Working:
			what = "working " + time.Since(s.Created).Round(time.Second).String()
		case server.Done, server.Idle:
			what = s.State + " · " + firstLine(orStr(s.Summary, s.Error))
		}
		fmt.Printf("  %s %-4s %-30s %-8s %s\n", stateMark[s.State], s.ID, clipStr(s.Title, 30), s.Agent, clipStr(what, 70))
	}
	for _, p := range st.Panes {
		fmt.Println(paneLine(p))
	}
	for _, r := range st.Routines {
		if r.On && !r.Next.IsZero() {
			fmt.Printf("  routine %s next %s\n", r.Name, r.Next.Format("Mon 15:04"))
		}
	}
	for _, n := range st.Notes {
		fmt.Printf("  note from %s: %s\n", n.Routine, firstLine(n.Text))
	}
}

// rollup is "1 blocked · 2 working · 1 done".
func rollup(sessions []server.Info) string {
	counts := map[string]int{}
	for _, s := range sessions {
		counts[s.State]++
	}
	var parts []string
	for _, st := range []string{server.Blocked, server.Working, server.Done} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[st], st))
		}
	}
	if len(parts) == 0 {
		return "nothing running"
	}
	return strings.Join(parts, " · ")
}

func runStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	fs.Parse(args)
	if err := client(*teamDir, false).StopServer(); err != nil {
		fail("%v", err)
	}
	fmt.Println("stopped")
}

func runSession(args []string) {
	if len(args) == 0 || args[0] != "new" {
		fail(`use: tubas session new -team DIR [-agent A] "question"`)
	}
	fs := flag.NewFlagSet("session new", flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	agentName := fs.String("agent", "", "who answers (empty: the team's routing decides)")
	fs.Parse(args[1:])
	q := strings.Join(fs.Args(), " ")
	if q == "" {
		fail("write the question")
	}
	info, err := client(*teamDir, true).Start(server.StartRequest{Target: strings.TrimPrefix(*agentName, "@"), Question: q})
	if err != nil {
		fail("%v", err)
	}
	printJSON(info)
}

func runWait(args []string) {
	fs := flag.NewFlagSet("wait", flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	until := fs.String("until", "blocked,done,idle", "states to wait for")
	timeout := fs.Duration("timeout", 0, "give up after this long (0: no limit)")
	fs.Parse(reorder(args))
	if fs.NArg() != 1 {
		fail("use: tubas wait -team DIR ID")
	}
	info, err := client(*teamDir, false).Wait(fs.Arg(0), strings.Split(*until, ","), *timeout)
	if err != nil {
		fail("%v", err)
	}
	printJSON(info)
}

// runAnswer answers an approval: tubas answer -team DIR s3-7 yes
func runAnswer(args []string) {
	fs := flag.NewFlagSet("answer", flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	fs.Parse(reorder(args))
	if fs.NArg() != 2 || (fs.Arg(1) != "yes" && fs.Arg(1) != "no") {
		fail("use: tubas answer -team DIR APPROVAL-ID yes|no (tubas status -json lists them)")
	}
	if err := client(*teamDir, false).Answer(fs.Arg(0), fs.Arg(1) == "yes"); err != nil {
		fail("%v", err)
	}
	fmt.Println("answered " + fs.Arg(1))
}

func runRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	last := fs.Bool("last", false, "only the last conclusion")
	fs.Parse(reorder(args))
	if fs.NArg() != 1 {
		fail("use: tubas read -team DIR ID")
	}
	events, info, err := client(*teamDir, false).Events(fs.Arg(0), 0, 0)
	if err != nil {
		fail("%v", err)
	}
	if *last {
		fmt.Printf("%s: %s\n", info.Agent, orStr(info.Summary, info.Error))
		return
	}
	fmt.Printf("%s · %s · %s\n%s\n\n", info.ID, info.Title, info.State, info.Question)
	for _, w := range events {
		switch w.Kind {
		case "turn_started":
			fmt.Printf("%s\n", w.Agent)
		case "tool_called":
			fmt.Printf("  %s\n", w.Call)
		case "tool_result":
			mark := "✓"
			if w.Failed {
				mark = "✗"
			}
			fmt.Printf("  %s %s\n", mark, firstLine(w.Text))
		case "approval":
			fmt.Printf("  ◐ waits for you: %s\n", w.Call)
		case "turn_finished":
			fmt.Printf("  → %s\n", w.Summary)
		case "failed":
			fmt.Printf("✗ %s\n", w.Error)
		}
	}
}

// reorder puts flags first, so "wait s4 -until done" works as well as
// "wait -until done s4" (Go's flag package stops at the first argument).
func reorder(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			name := strings.TrimLeft(args[i], "-")
			isBool := name == "last" || name == "json" || name == "enter" || name == "worktree" || name == "color"
			if !isBool && !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		rest = append(rest, args[i])
	}
	return append(flags, rest...)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func clipStr(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
