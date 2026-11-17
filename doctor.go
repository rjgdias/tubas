package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"tubas/internal/agent"
	"tubas/internal/engine"
	"tubas/internal/team"
)

// tubas doctor checks, one step at a time, how agents reach a model on
// this computer: Claude Code, the Claude settings (a company gateway),
// the token script, the gateway's model list and one real answer, with
// the same engine the chat uses. Each step says what it found and, when
// it fails, what to do. It never prints a key.
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
	ok, bad := "  ✓ ", "  ✗ "
	problems := 0
	step := func(n int, name string) { fmt.Printf("%d. %s\n", n, name) }

	// 1. Claude Code.
	step(1, "Claude Code")
	claude, findErr := engine.FindClaude()
	if findErr != nil {
		fmt.Println(bad + "not found: " + findErr.Error())
		fmt.Println("    install it (https://claude.com/claude-code), or agents run on tubas's own loop through your settings")
		problems++
	} else {
		version := "?"
		if out, err := exec.Command(claude, "--version").Output(); err == nil {
			version = strings.TrimSpace(string(out))
		}
		fmt.Println(ok + claude + " · " + version)
		if missing := engine.MissingFlags(claude); len(missing) > 0 {
			fmt.Println("  ! an older version: it doesn't know " + strings.Join(missing, ", "))
			why := "tubas works around it"
			if slices.Contains(missing, "--setting-sources") {
				why += ", but agents also get your CLAUDE.md and hooks"
			}
			fmt.Println("    " + why + " · claude update brings the new options")
		}
	}

	// 2. Settings: where the model is.
	step(2, "Your Claude settings")
	p := engine.FindProvider(t.Dir)
	if len(p.Sources) == 0 {
		fmt.Println(ok + "no settings file: Claude's own login")
	} else {
		fmt.Println(ok + "read " + strings.Join(p.Sources, ", "))
	}
	base := p.Get("ANTHROPIC_BASE_URL")
	switch {
	case base != "":
		fmt.Println(ok + "gateway: " + base)
	case p.Label() != "":
		fmt.Println(ok + "provider: " + p.Label())
	default:
		if claude != "" {
			loggedIn, plan, detail := engine.ClaudeAuth(claude)
			switch {
			case loggedIn && plan:
				fmt.Println(ok + "Claude's own login, a subscription (" + detail + ")")
			case loggedIn:
				fmt.Println(ok + "Claude's own login (" + detail + ")")
			default:
				fmt.Println(bad + "not signed in: run claude and type /login")
				problems++
			}
		}
	}
	var auth []string
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if p.Get(k) != "" {
			auth = append(auth, k+" set")
		}
	}
	if p.KeyHelper != "" {
		auth = append(auth, "apiKeyHelper: "+p.KeyHelper)
	}
	if len(auth) > 0 {
		fmt.Println(ok + "key: " + strings.Join(auth, " · "))
	} else if base != "" {
		fmt.Println(bad + "a gateway, but no key: set ANTHROPIC_AUTH_TOKEN or apiKeyHelper in the settings")
		problems++
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
		fmt.Println(ok + "extra headers: " + strings.Join(names, ", "))
	}

	// 3. The token script.
	step(3, "The token script (apiKeyHelper)")
	if p.KeyHelper == "" {
		fmt.Println(ok + "none in your settings")
	} else {
		key, took, stderr, err := engine.RunKeyHelper(p.KeyHelper)
		if err != nil {
			fmt.Printf("%sfailed after %s: %v\n", bad, took.Round(100*time.Millisecond), err)
			if stderr != "" {
				fmt.Println("    it printed: " + lastLinesOf(stderr, 5))
			}
			fmt.Println("    run it in this terminal to see what it needs: " + p.KeyHelper)
			problems++
		} else {
			fmt.Printf("%sprinted a token in %s (%d characters)\n", ok, took.Round(100*time.Millisecond), len(key))
		}
	}

	// 4. The gateway answers, with that key.
	step(4, "The gateway")
	if base == "" {
		fmt.Println(ok + "not used")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		models, err := engine.GatewayModels(ctx, p)
		cancel()
		switch {
		case err != nil:
			fmt.Println(bad + "listing its models failed: " + err.Error())
			fmt.Println("    (some gateways don't list models: step 5 tells whether answers work)")
		default:
			show := models
			if len(show) > 6 {
				show = append(show[:6:6], "…")
			}
			fmt.Printf("%sserves %d models: %s\n", ok, len(models), strings.Join(show, ", "))
		}
	}

	// 5. One real answer, with the chat's own engine.
	step(5, "A real answer")
	lead, found := t.Agent(t.Lead)
	if !found {
		fmt.Println(bad + "no lead agent in the team")
		os.Exit(1)
	}
	eng, _, closeEngine := buildEngine(t, "claude", "", false)
	defer closeEngine()
	members := map[string]agent.Agent{}
	for _, a := range t.Agents {
		members[a.Name] = a
	}
	fmt.Printf("    asking @%s (%s, model %s); up to 3 minutes…\n", lead.Name, orDefaultStr(lead.Harness, "claude"), lead.Model)
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
	reply, err := eng.RunTurn(ctx, engine.TurnRequest{Agent: lead, Prompt: "Reply with the single word: ok", Team: members}, events)
	close(events)
	took := time.Since(start).Round(100 * time.Millisecond)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		fmt.Printf("%sno answer after %s: the gateway may be unreachable from here (a VPN, a proxy: HTTPS_PROXY)\n", bad, took)
		problems++
	case err != nil:
		fmt.Printf("%sfailed after %s: %v\n", bad, took, err)
		problems++
	default:
		fmt.Printf("%s%q from %s in %s\n", ok, strings.TrimSpace(firstLineOf(reply.Text)), orDefaultStr(reply.Model, lead.Model), took)
	}
	fmt.Println()
	if problems == 0 {
		fmt.Println("all good: type tubas and ask")
		return
	}
	fmt.Printf("%d problem(s) above. To share them, copy this output: it has no keys.\n", problems)
	os.Exit(1)
}

func lastLinesOf(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " · ")
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
