package routine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Run is what happened one time a routine ran. Runs are kept as JSON
// files under <team>/.tubas/runs/<routine>/, one per run.
type Run struct {
	Routine  string    `json:"routine"`
	Agent    string    `json:"agent"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Outcome  string    `json:"outcome"` // "ok", "failed", "waiting" (stopped waiting for approval), "skipped"
	Summary  string    `json:"summary"` // the agent's conclusion
	Error    string    `json:"error,omitempty"`
	Tokens   int       `json:"tokens,omitempty"`
	Cost     float64   `json:"cost,omitempty"`
	Retry    bool      `json:"retry,omitempty"` // this run was the retry of a failed one
}

// The file name is the start time in UTC, so names sort in time order
// even if the computer changes time zone or DST flips.
const runStamp = "20060102-150405"

func runsDir(teamDir, name string) string {
	return filepath.Join(teamDir, ".tubas", "runs", name)
}

// SaveRun stores a run, creating the folders it needs.
func SaveRun(teamDir string, r Run) error {
	// The name becomes a folder; checking it keeps "../" out.
	if !nameRe.MatchString(r.Routine) {
		return fmt.Errorf("run: bad routine name %q", r.Routine)
	}
	dir := runsDir(teamDir, r.Routine)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	// Two runs in the same second (rare, but a manual run could do it)
	// get -2, -3... instead of overwriting each other.
	stem := r.Started.UTC().Format(runStamp)
	path := filepath.Join(dir, stem+".json")
	for i := 2; fileExists(path); i++ {
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.json", stem, i))
	}
	return writeAtomic(path, data)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// LastRuns returns up to n runs of a routine, newest first (n <= 0 means
// all). A routine that never ran has no folder: that is nil, nil.
func LastRuns(teamDir, name string, n int) ([]Run, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("runs: bad routine name %q", name)
	}
	entries, err := os.ReadDir(runsDir(teamDir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stems []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		stems = append(stems, strings.TrimSuffix(e.Name(), ".json"))
	}
	// Compare without ".json" so "…-150405-2" sorts after "…-150405".
	sort.Sort(sort.Reverse(sort.StringSlice(stems)))

	var runs []Run
	for _, stem := range stems {
		if n > 0 && len(runs) == n {
			break
		}
		data, err := os.ReadFile(filepath.Join(runsDir(teamDir, name), stem+".json"))
		if err != nil {
			continue
		}
		var r Run
		// One damaged file shouldn't hide the rest of the history.
		if json.Unmarshal(data, &r) != nil {
			continue
		}
		runs = append(runs, r)
	}
	return runs, nil
}

// LastRun is the most recent run of a routine, if it ever ran.
func LastRun(teamDir, name string) (Run, bool) {
	runs, err := LastRuns(teamDir, name, 1)
	if err != nil || len(runs) == 0 {
		return Run{}, false
	}
	return runs[0], true
}
