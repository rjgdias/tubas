package routine

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingFile(t *testing.T) {
	f, err := Load(t.TempDir())
	if err != nil || len(f.Routines) != 0 {
		t.Fatalf("Load of an empty folder = %+v, %v; want empty, nil", f, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	off := false
	want := File{
		QuietHours: "22:00-07:00",
		Retry:      true,
		Routines: []Routine{
			{Name: "databricks-daily", Agent: "leo", Every: "weekdays 08:00", Prompt: "Check the nightly_orders job.", Deliver: "chat"},
			{Name: "inbox-30m", Agent: "ana", Every: "30m", Between: "08:00-20:00", Prompt: "Sort the inbox.", Deliver: "report", Enabled: &off},
		},
	}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
	if !got.Routines[0].On() || got.Routines[1].On() {
		t.Error("On(): first should be on (enabled missing), second off")
	}
	if r, i := got.Find("inbox-30m"); i != 1 || r.Agent != "ana" {
		t.Errorf("Find = %+v, %d", r, i)
	}
	if _, i := got.Find("nope"); i != -1 {
		t.Errorf("Find(missing) index = %d, want -1", i)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("folder has %d entries, want only %s", len(entries), FileName)
	}
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `
routines:
  - name: a
    agent: leo
    every: "1h"
    prompt: hi
`)
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := f.Routines[0]
	if !r.On() || r.Deliver != "" || f.QuietHours != "" || f.Retry {
		t.Errorf("defaults: %+v %+v", f, r)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"duplicate", `
routines:
  - {name: daily, agent: leo, every: "daily 09:00", prompt: x}
  - {name: daily, agent: ana, every: "1h", prompt: y}
`, `routine "daily" appears twice`},
		{"bad schedule", `
routines:
  - {name: weird, agent: leo, every: "sometimes", prompt: x}
`, `routine "weird": can't read every`},
		{"between on daily", `
routines:
  - {name: morning, agent: leo, every: "daily 09:00", between: "08:00-20:00", prompt: x}
`, `routine "morning": between only works with interval`},
		{"bad deliver", `
routines:
  - {name: mail, agent: leo, every: "1h", prompt: x, deliver: email}
`, `routine "mail": deliver "email"`},
		{"bad name", `
routines:
  - {name: Leo Daily, agent: leo, every: "1h", prompt: x}
`, `routine "Leo Daily": name must be lowercase`},
		{"no name", `
routines:
  - {agent: leo, every: "1h", prompt: x}
`, `routine 1 has no name`},
		{"no agent", `
routines:
  - {name: a, every: "1h", prompt: x}
`, `routine "a": agent is missing`},
		{"no prompt", `
routines:
  - {name: a, agent: leo, every: "1h"}
`, `routine "a": prompt is missing`},
		{"bad quiet", `
quiet_hours: "late"
routines: []
`, `quiet_hours`},
		{"typo", `
routines:
  - {name: a, agent: leo, evrey: "1h", prompt: x}
`, `evrey`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, tt.yaml)
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestSaveValidates(t *testing.T) {
	dir := t.TempDir()
	bad := File{Routines: []Routine{{Name: "a", Agent: "leo", Every: "daily 09:00", Between: "08:00-20:00", Prompt: "x"}}}
	if err := Save(dir, bad); err == nil {
		t.Fatal("Save accepted between on a daily schedule")
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Error("Save wrote a file it refused")
	}
}

func write(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRuns(t *testing.T) {
	dir := t.TempDir()

	runs, err := LastRuns(dir, "daily", 5)
	if runs != nil || err != nil {
		t.Fatalf("no runs yet: got %v, %v", runs, err)
	}
	if _, ok := LastRun(dir, "daily"); ok {
		t.Fatal("LastRun found a run that never happened")
	}

	base := time.Date(2026, 11, 16, 8, 0, 0, 0, brt)
	// Saved out of order on purpose.
	for _, h := range []int{2, 0, 1} {
		r := Run{Routine: "daily", Agent: "leo", Started: base.Add(time.Duration(h) * 24 * time.Hour), Outcome: "ok", Summary: "day " + string(rune('0'+h))}
		r.Finished = r.Started.Add(time.Minute)
		if err := SaveRun(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	// Same second as the newest: a manual run must not overwrite it.
	dup := Run{Routine: "daily", Agent: "leo", Started: base.Add(48 * time.Hour), Outcome: "failed", Error: "boom", Retry: true}
	if err := SaveRun(dir, dup); err != nil {
		t.Fatal(err)
	}

	runs, err = LastRuns(dir, "daily", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range runs {
		got = append(got, r.Summary+r.Error)
	}
	want := []string{"boom", "day 2", "day 1", "day 0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LastRuns order = %v, want %v", got, want)
	}
	if !runs[1].Started.Equal(base.Add(48 * time.Hour)) {
		t.Errorf("Started = %s", runs[1].Started)
	}

	two, _ := LastRuns(dir, "daily", 2)
	if len(two) != 2 || two[0].Error != "boom" {
		t.Errorf("LastRuns(2) = %+v", two)
	}
	last, ok := LastRun(dir, "daily")
	if !ok || !last.Retry || last.Outcome != "failed" {
		t.Errorf("LastRun = %+v, %v", last, ok)
	}

	// Files are where other code expects them.
	if _, err := os.Stat(filepath.Join(dir, ".tubas", "runs", "daily", "20261116-110000.json")); err != nil {
		t.Errorf("run file: %v", err)
	}

	if err := SaveRun(dir, Run{Routine: "../escape"}); err == nil {
		t.Error("SaveRun accepted a name with ../")
	}
}
