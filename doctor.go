package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"tubas/internal/agent"
	"tubas/internal/engine"
	"tubas/internal/mcp"
	"tubas/internal/team"
	"tubas/internal/tools"
)

// tubas doctor checks, one step at a time, how agents reach a model on
// this computer: the keys you saved (/connect), your Claude settings (a
// company gateway and its token script), the gateway's models, each
// provider the agents use with one real answer, the shell agents run
// commands in, and the agent files. Each step says what it found and,
// when it fails, what to do. It never prints a key.
func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	fs.Parse(args)
	t, err := team.Load(findTeam(*teamDir))
	if err != nil {
		fail("%v", err)
	}
	if err := t.Prepare(false); err != nil {
		fail("%v", err)
	}
	fmt.Println("tubas doctor · how agents reach a model on this computer")
	fmt.Println()
	ok, bad, note := "  ✓ ", "  ✗ ", "  · "
	problems := 0
	step := func(n int, name string) { fmt.Printf("%d. %s\n", n, name) }
	p := engine.FindProvider(t.Dir)
	providers := mcp.TeamProviders(t)

	// 1. Keys: where each provider's comes from.
	step(1, "Providers and keys")
	used := map[string][]string{} // provider → the agents on it
	for _, a := range t.Agents {
		name := orDefaultStr(a.Provider, orDefaultStr(t.Provider, engine.Anthropic))
		used[name] = append(used[name], a.Name)
	}
	for _, c := range engine.Connections(p, providers) {
		agents := used[c.Name]
		if len(agents) == 0 && (!c.Ready() || c.State == engine.ConnFree) {
			continue // not used, and nothing connected for it: nothing to say
		}
		who := ""
		if len(agents) > 0 {
			who = " · used by " + strings.Join(agents, ", ")
		}
		if c.Ready() {
			fmt.Printf("%s%s: %s%s\n", ok, c.Name, orDefaultStr(c.Detail, c.State), who)
			continue
		}
		fmt.Printf("%s%s: no key%s\n    in tubas: /connect, or set %s\n", bad, c.Name, who, c.Detail)
		problems++
	}

	// 2. Your Claude settings: a company gateway.
	step(2, "Your Claude settings")
	base := p.Get("ANTHROPIC_BASE_URL")
	if len(p.Sources) == 0 {
		fmt.Println(note + "no settings file (only needed for a company gateway)")
	} else {
		fmt.Println(ok + "read " + strings.Join(p.Sources, ", "))
	}
	switch label := p.Label(); {
	case label == "bedrock" || label == "vertex" || label == "foundry":
		fmt.Println(bad + "they use " + label + ": tubas doesn't reach it · /connect a provider instead")
		problems++
	case base != "":
		fmt.Println(ok + "gateway: " + base)
	}
	for _, alias := range []string{"sonnet", "opus", "haiku"} {
		if v := p.Get("ANTHROPIC_DEFAULT_" + strings.ToUpper(alias) + "_MODEL"); v != "" {
			fmt.Println(ok + alias + " means " + v)
		}
	}
	if h := p.Headers(); len(h) > 0 {
		var names []string
		for k := range h {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Println(ok + "extra headers: " + strings.Join(names, ", "))
	}
	if p.KeyHelper != "" {
		key, took, stderr, err := engine.RunKeyHelper(p.KeyHelper)
		if err != nil {
			fmt.Printf("%sthe token script (apiKeyHelper) failed after %s: %v\n", bad, took.Round(100*time.Millisecond), err)
			if stderr != "" {
				fmt.Println("    it printed: " + lastLinesOf(stderr, 5))
			}
			fmt.Println("    run it in this terminal to see what it needs: " + p.KeyHelper)
			problems++
		} else {
			fmt.Printf("%sthe token script printed a token in %s (%d characters)\n", ok, took.Round(100*time.Millisecond), len(key))
		}
	} else if base != "" && p.Get("ANTHROPIC_AUTH_TOKEN") == "" && p.Get("ANTHROPIC_API_KEY") == "" {
		fmt.Println(bad + "a gateway, but no key: set ANTHROPIC_AUTH_TOKEN or apiKeyHelper in the settings")
		problems++
	}
	if base != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		models, err := engine.GatewayModels(ctx, p)
		cancel()
		if err != nil {
			fmt.Println(note + "the gateway didn't list its models: " + err.Error() + " (step 3 tells whether answers work)")
		} else {
			show := models
			if len(show) > 6 {
				show = append(show[:6:6], "…")
			}
			fmt.Printf("%sthe gateway serves %d models: %s\n", ok, len(models), strings.Join(show, ", "))
		}
	}

	// 3. One real answer from each provider the agents use, with the
	// chat's own engine.
	step(3, "A real answer")
	eng, _, closeEngine := buildEngine(t, "native", "", false)
	defer closeEngine()
	members := map[string]agent.Agent{}
	for _, a := range t.Agents {
		members[a.Name] = a
	}
	tried := map[string]bool{}
	for _, a := range t.Agents {
		key := orDefaultStr(a.Provider, t.Provider) + "/" + a.Model
		if tried[key] {
			continue
		}
		tried[key] = true
		if !askOnce(eng, a, members, ok, bad) {
			problems++
		}
	}

	// 4. The shell agents run commands in, and the search tool.
	step(4, "Tools on this computer")
	if exe, kind := tools.Shell(); kind == "powershell" {
		fmt.Println(note + "commands run in Windows PowerShell: Git for Windows brings bash, which agents know better")
	} else {
		fmt.Println(ok + "commands run in " + exe)
	}
	if rg, err := exec.LookPath("rg"); err == nil {
		fmt.Println(ok + "grep uses ripgrep (" + rg + ")")
	} else {
		fmt.Println(note + "no ripgrep: grep searches on its own, slower in big repositories")
	}

	// 5. Agent files from before: what changed.
	step(5, "The agent files")
	old := 0
	for _, a := range t.Agents {
		if a.Harness != "" && a.Harness != "native" {
			fmt.Printf("%s%s says harness: %s · tubas runs every agent itself now: the line is ignored, and goes the next time you save it\n", note, a.Name, a.Harness)
			old++
		}
	}
	if len(t.Harnesses) > 0 {
		fmt.Println(note + "team.yaml lists harnesses: ignored, tubas runs every agent itself")
		old++
	}
	if old == 0 {
		fmt.Printf("%s%d agents, nothing to change\n", ok, len(t.Agents))
	}

	fmt.Println()
	if problems == 0 {
		fmt.Println("all good: type tubas and ask")
		return
	}
	fmt.Printf("%d problem(s) above. To share them, copy this output: it has no keys.\n", problems)
	os.Exit(1)
}

// askOnce asks an agent for one word and says how it went.
func askOnce(eng engine.Engine, a agent.Agent, members map[string]agent.Agent, ok, bad string) bool {
	fmt.Printf("    asking @%s (%s, model %s); up to 3 minutes…\n", a.Name, orDefaultStr(a.Provider, "anthropic"), orDefaultStr(a.Model, "default"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	events := make(chan engine.Event, 64)
	go func() {
		for e := range events {
			if r, isRetry := e.(engine.Retrying); isRetry {
				fmt.Printf("    ⟳ %s (%s), waiting %s\n", r, r.Reason, r.Delay.Round(100*time.Millisecond))
			}
		}
	}()
	start := time.Now()
	reply, err := eng.RunTurn(ctx, engine.TurnRequest{Agent: a, Prompt: "Reply with the single word: ok", Team: members}, events)
	close(events)
	took := time.Since(start).Round(100 * time.Millisecond)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		fmt.Printf("%sno answer after %s: the provider may be unreachable from here (a VPN, a proxy: HTTPS_PROXY)\n", bad, took)
		return false
	case err != nil:
		fmt.Printf("%sfailed after %s: %v\n", bad, took, err)
		return false
	}
	fmt.Printf("%s%q from %s in %s\n", ok, strings.TrimSpace(firstLineOf(reply.Text)), orDefaultStr(reply.Model, a.Model), took)
	return true
}

func lastLinesOf(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func firstLineOf(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return first
}

func orDefaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
