package pane

import (
	"runtime"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// echoCommand prints text and exits, on any system.
func echoCommand(text string) Spec {
	if runtime.GOOS == "windows" {
		return Spec{Command: "cmd.exe", Args: []string{"/c", "echo " + text}}
	}
	return Spec{Command: "/bin/sh", Args: []string{"-c", "echo " + text}}
}

// askCommand asks a y/n question and prints the answer.
func askCommand() Spec {
	if runtime.GOOS == "windows" {
		return Spec{Command: "powershell.exe", Args: []string{"-NoLogo", "-NoProfile", "-Command",
			"$a = Read-Host 'Continue (y/n)'; Write-Output \"answer=$a\"; Start-Sleep -Seconds 5"}}
	}
	return Spec{Command: "/bin/sh", Args: []string{"-c", "printf 'Continue (y/n)? '; read a; echo answer=$a; sleep 5"}}
}

// waitFor polls the screen until it contains text.
func waitFor(t *testing.T, m *Manager, id, text string) Screen {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	since := -1
	for {
		s, err := m.Screen(id, since, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		plain := ansi.Strip(strings.Join(s.Lines, "\n"))
		if strings.Contains(plain, text) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("the screen never showed %q:\n%s", text, plain)
		}
		since = s.Version
	}
}

func TestPaneRunsAProgram(t *testing.T) {
	m := NewManager(t.TempDir())
	defer m.CloseAll()
	info, err := m.Open(echoCommand("hello-from-pane"), 60, 10)
	if err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, m, info.ID, "hello-from-pane")
	if len(s.Lines) != 10 {
		t.Errorf("the screen has %d rows, want 10", len(s.Lines))
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		list, _ := m.List()
		if list[0].State == Done {
			if !strings.HasPrefix(list[0].Exit, "exited") {
				t.Errorf("exit = %q", list[0].Exit)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("an exited program should be done: %+v", list[0])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPaneAsksAndTakesAnAnswer(t *testing.T) {
	m := NewManager(t.TempDir())
	defer m.CloseAll()
	info, err := m.Open(askCommand(), 60, 10)
	if err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, m, info.ID, "(y/n)")
	if s.State != Blocked {
		t.Errorf("a question on screen should block, got %s", s.State)
	}
	if err := m.Send(info.ID, []Key{{Text: "y"}, {Name: "enter"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, info.ID, "answer=y")
	if err := m.Resize(info.ID, 40, 8); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Screen(info.ID, -1, 0); len(s.Lines) != 8 {
		t.Errorf("after a resize the screen has %d rows", len(s.Lines))
	}
	if err := m.Close(info.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.List(); len(list) != 0 {
		t.Error("a closed pane is forgotten")
	}
}

func TestParseKey(t *testing.T) {
	cases := map[string]uv.KeyPressEvent{
		"enter":     {Code: uv.KeyEnter},
		"ctrl+c":    {Code: 'c', Mod: uv.ModCtrl},
		"alt+up":    {Code: uv.KeyUp, Mod: uv.ModAlt},
		"shift+tab": {Code: uv.KeyTab, Mod: uv.ModShift},
		"a":         {Code: 'a', Text: "a"},
		"ç":         {Code: 'ç', Text: "ç"},
	}
	for name, want := range cases {
		got, ok := ParseKey(name)
		if !ok || got.Code != want.Code || got.Mod != want.Mod || got.Text != want.Text {
			t.Errorf("ParseKey(%q) = %+v, want %+v", name, got, want)
		}
	}
	if _, ok := ParseKey("ctrl+nonsense"); ok {
		t.Error("unknown keys are refused")
	}
}

func TestAsks(t *testing.T) {
	if !asks([]string{" Do you want to proceed?", "❯ 1. Yes", "  2. No"}) {
		t.Error("claude's permission prompt is a question")
	}
	if asks([]string{"PS C:\\work>", "> fix the timeout"}) {
		t.Error("a shell prompt is not a question")
	}
}

func TestSplitCommand(t *testing.T) {
	got := SplitCommand(`claude --model sonnet "fix the timeout" 'a b'`)
	want := []string{"claude", "--model", "sonnet", "fix the timeout", "a b"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("SplitCommand = %q", got)
	}
}

// script runs a little program: PowerShell on Windows, sh elsewhere.
func script(win, unix string) Spec {
	if runtime.GOOS == "windows" {
		return Spec{Command: "powershell.exe", Args: []string{"-NoLogo", "-NoProfile", "-Command", win}}
	}
	return Spec{Command: "/bin/sh", Args: []string{"-c", unix}}
}

func waitState(t *testing.T, m *Manager, id, want string, within time.Duration) Info {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		list, _ := m.List()
		for _, i := range list {
			if i.ID == id && i.State == want {
				return i
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never became %s: %+v", id, want, list)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestHistoryKeepsWhatScrolledAway(t *testing.T) {
	m := NewManager(t.TempDir())
	defer m.CloseAll()
	info, err := m.Open(script(`1..60 | ForEach-Object { "line-$_" }; Start-Sleep 20`,
		`for i in $(seq 1 60); do echo line-$i; done; sleep 20`), 40, 8)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, info.ID, "line-60")
	_, plain, err := m.History(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(plain, "\n")
	if !strings.Contains(all, "line-1\n") || !strings.Contains(all, "line-60") {
		t.Errorf("history should hold every line:\n%s", all)
	}
}

func TestPanesComeBackAfterARestart(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	spec := script(`Write-Output pane-back; Start-Sleep 60`, `echo pane-back; sleep 60`)
	spec.Title = "keeper"
	info, err := m.Open(spec, 50, 8)
	if err != nil {
		t.Fatal(err)
	}
	m.SetLayout(`{"pane":"chat"}`)
	waitFor(t, m, info.ID, "pane-back")
	m.CloseAll() // the server goes down

	again := NewManager(dir)
	n, errs := again.Restore()
	if n != 1 || len(errs) > 0 {
		t.Fatalf("the pane should come back: %d, %v", n, errs)
	}
	list, _ := again.List()
	if list[0].ID != info.ID || list[0].Title != "keeper" {
		t.Errorf("same id and title: %+v", list[0])
	}
	if l, _ := again.Layout(); l != `{"pane":"chat"}` {
		t.Errorf("the layout comes back too: %q", l)
	}
	waitFor(t, again, info.ID, "pane-back")
	next, _ := again.Open(echoCommand("x"), 20, 5)
	if next.ID == info.ID {
		t.Error("new ids continue after the restored ones")
	}
	waitState(t, again, next.ID, Done, 10*time.Second) // ended on its own
	again.Close(info.ID)                               // closed by you: gone for good
	again.CloseAll()
	third := NewManager(dir)
	if n, _ := third.Restore(); n != 0 {
		t.Errorf("closed and exited panes stay gone, %d came back", n)
	}
	third.CloseAll()
}

func TestPanesStartClean(t *testing.T) {
	got := cleanEnv([]string{"PATH=x", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=a", "CLAUDE_CODE_MESSAGING_TOKEN=s",
		"CLAUDE_CODE_USE_BEDROCK=1", "ANTHROPIC_BASE_URL=u"})
	if strings.Join(got, ",") != "PATH=x,CLAUDE_CODE_USE_BEDROCK=1,ANTHROPIC_BASE_URL=u" {
		t.Errorf("a pane keeps the provider and drops the session markers: %v", got)
	}
}
