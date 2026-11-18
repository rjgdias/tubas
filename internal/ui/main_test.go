package ui

import (
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMain(m *testing.M) {
	// Tests run the commands they get, and a notice's timer would make
	// each wait seconds: notices stay until the next key here.
	noticeFor = 0
	os.Exit(m.Run())
}

func TestANoticeGoesAfterAMoment(t *testing.T) {
	noticeFor = 10 * time.Millisecond
	defer func() { noticeFor = 0 }()
	m, _ := personalModel(t)
	next, cmd := m.Update(noticeMsg("✓ done"))
	m = next.(Model)
	if m.notice == "" || cmd == nil {
		t.Fatal("a notice shows, with a timer")
	}
	// The command is the timer (with panes, in a batch): when it fires,
	// its message says which notice goes.
	expired := runFor(cmd, time.Second)
	if batch, ok := expired.(tea.BatchMsg); ok {
		expired = nil
		for _, c := range batch {
			if msg, ok := runFor(c, 300*time.Millisecond).(noticeExpiredMsg); ok {
				expired = msg
			}
		}
	}
	if _, ok := expired.(noticeExpiredMsg); !ok {
		t.Fatalf("the timer says when it goes, got %T", expired)
	}
	m = feed(m, expired)
	if m.notice != "" {
		t.Error("and then it is gone: the hints are back")
	}
	// One that asks (y deletes…) stays until you answer.
	m.deleting = "s1"
	m = feed(m, noticeMsg("delete it? y deletes it for good"))
	m = feed(m, noticeExpiredMsg(m.notice))
	if m.notice == "" {
		t.Error("a question stays")
	}
}

// runFor runs a command, giving up after d (one that waits for an event
// would block the test for good).
func runFor(cmd tea.Cmd, d time.Duration) tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(d):
		return nil
	}
}
