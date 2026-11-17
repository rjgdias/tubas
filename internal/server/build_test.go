package server

import (
	"testing"

	"tubas/internal/pane"
)

func TestAnOlderServerIsReplacedWhenIdle(t *testing.T) {
	idle := Status{Sessions: []Info{{State: Done}, {State: Idle}}}
	working := Status{Sessions: []Info{{State: Working}}}
	waiting := Status{Sessions: []Info{{State: Blocked}}}
	withPane := Status{Panes: []pane.Info{{ID: "p1"}}}
	cases := []struct {
		name string
		f    File
		st   Status
		want serverAction
	}{
		{"same tubas", File{Build: "b2"}, working, useIt},
		{"older, idle", File{Build: "b1"}, idle, replaceIt},
		{"from before builds were recorded", File{}, idle, replaceIt},
		{"older, an answer coming", File{Build: "b1"}, working, keepOutdated},
		{"older, waiting for you", File{Build: "b1"}, waiting, keepOutdated},
		{"older, a terminal open", File{Build: "b1"}, withPane, keepOutdated},
	}
	for _, c := range cases {
		if got := whatToDo(c.f, c.st, "b2"); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	if Build() == "" {
		t.Error("a test binary has a build too")
	}
}
