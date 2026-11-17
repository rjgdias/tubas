package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"tubas/internal/pane"
)

// tubas pane: drive terminal panes from scripts and other agents.
//
//	tubas pane new  [-repo api [-worktree]] [-title T] [--] claude --model sonnet
//	tubas pane list [-json]
//	tubas pane read p2
//	tubas pane send p2 "npm test" -enter      (or -key ctrl+c)
//	tubas pane wait p2 -until blocked,idle,done [-timeout 10m]
//	tubas pane close p2
func runPane(args []string) {
	if len(args) == 0 {
		fail("use: tubas pane new|list|read|send|wait|close (see tubas pane new -h)")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("pane "+sub, flag.ExitOnError)
	teamDir := fs.String("team", ".", "the team folder")
	switch sub {
	case "new":
		repoName := fs.String("repo", "", "a repository connector from team.yaml")
		worktree := fs.Bool("worktree", false, "with -repo: a new worktree on its own branch")
		title := fs.String("title", "", "shown on the pane")
		w := fs.Int("w", 120, "columns, until a window shows it")
		h := fs.Int("h", 32, "rows, until a window shows it")
		fs.Parse(args) // everything after the flags (or after --) is the command
		if *worktree && *repoName == "" {
			fail("-worktree needs -repo")
		}
		spec := pane.Spec{Title: *title, Repo: *repoName, Worktree: *worktree}
		if *repoName == "" {
			spec.Dir, _ = os.Getwd() // like a new terminal: where you are
		}
		if words := fs.Args(); len(words) > 0 {
			spec.Command, spec.Args = words[0], words[1:]
		}
		info, err := client(*teamDir, true).Panes().Open(spec, *w, *h)
		if err != nil {
			fail("%v", err)
		}
		printJSON(info)
	case "list":
		asJSON := fs.Bool("json", false, "print JSON")
		fs.Parse(args)
		list, err := client(*teamDir, false).Panes().List()
		if err != nil {
			fail("%v", err)
		}
		if *asJSON {
			printJSON(list)
			return
		}
		if len(list) == 0 {
			fmt.Println("no panes · tubas pane new opens one")
		}
		for _, p := range list {
			fmt.Println(paneLine(p))
		}
	case "read":
		fs.Parse(reorder(args))
		if fs.NArg() != 1 {
			fail("use: tubas pane read ID")
		}
		text, err := client(*teamDir, false).PaneText(fs.Arg(0))
		if err != nil {
			fail("%v", err)
		}
		fmt.Println(text)
	case "send":
		enter := fs.Bool("enter", false, "press enter after the text")
		key := fs.String("key", "", "a key instead of text: enter, ctrl+c, up, esc…")
		fs.Parse(reorder(args))
		if fs.NArg() < 1 {
			fail(`use: tubas pane send ID "text" [-enter] | tubas pane send ID -key ctrl+c`)
		}
		var keys []pane.Key
		if text := strings.Join(fs.Args()[1:], " "); text != "" {
			keys = append(keys, pane.Key{Text: text})
		}
		if *key != "" {
			keys = append(keys, pane.Key{Name: *key})
		}
		if *enter {
			keys = append(keys, pane.Key{Name: "enter"})
		}
		if err := client(*teamDir, false).Panes().Send(fs.Arg(0), keys); err != nil {
			fail("%v", err)
		}
	case "wait":
		until := fs.String("until", "blocked,idle,done", "states to wait for")
		timeout := fs.Duration("timeout", 0, "give up after this long (0: no limit)")
		fs.Parse(reorder(args))
		if fs.NArg() != 1 {
			fail("use: tubas pane wait ID [-until blocked,idle,done]")
		}
		host, id := client(*teamDir, false).Panes(), fs.Arg(0)
		want := strings.Split(*until, ",")
		deadline := time.Now().Add(*timeout)
		time.Sleep(time.Second) // let what you just sent start
		for since := -1; ; {
			scr, err := host.Screen(id, since, 2*time.Second)
			if err != nil {
				fail("%v", err)
			}
			if slices.Contains(want, scr.State) {
				printJSON(map[string]string{"pane": id, "state": scr.State})
				return
			}
			if *timeout > 0 && time.Now().After(deadline) {
				fail("timeout: %s is %s", id, scr.State)
			}
			since = scr.Version
		}
	case "close":
		fs.Parse(reorder(args))
		if fs.NArg() != 1 {
			fail("use: tubas pane close ID")
		}
		if err := client(*teamDir, false).Panes().Close(fs.Arg(0)); err != nil {
			fail("%v", err)
		}
		fmt.Println("closed " + fs.Arg(0))
	default:
		fail("unknown: tubas pane %s (new, list, read, send, wait, close)", sub)
	}
}

func paneLine(p pane.Info) string {
	where := p.Dir
	if p.Worktree != nil {
		where = p.Worktree.Branch
	} else if p.Repo != "" {
		where = p.Repo
	}
	state := p.State
	if p.State == pane.Done {
		state = p.Exit
	}
	return fmt.Sprintf("  %s %-4s %-24s %-30s %s", stateMark[p.State], p.ID, clipStr(p.Title, 24), clipStr(where, 30), state)
}
