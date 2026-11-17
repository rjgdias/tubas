// Package routine reads a team's routines: agents working on their own,
// on a schedule. They live in routines.yaml in the team folder.
//
//	quiet_hours: "22:00-07:00"   # nothing runs in this window
//	retry: true                  # a failed run is tried once more 5 minutes later
//	routines:
//	  - name: databricks-daily
//	    agent: leo
//	    every: "weekdays 08:00"
//	    prompt: "Check the nightly_orders job..."
//	    deliver: chat             # chat (default) or report
//
// Each time a routine runs it leaves a Run behind (see runs.go), so you
// can see what happened while you were away.
package routine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the routines file inside a team folder.
const FileName = "routines.yaml"

// File is everything in routines.yaml.
type File struct {
	QuietHours string    `yaml:"quiet_hours,omitempty"` // "22:00-07:00"; may cross midnight
	Retry      bool      `yaml:"retry,omitempty"`       // try a failed run once more, 5 minutes later
	Routines   []Routine `yaml:"routines"`
}

// Routine is one agent job on a schedule.
type Routine struct {
	Name    string `yaml:"name"`              // lowercase, a-z0-9 and -
	Agent   string `yaml:"agent"`             // who does the work
	Every   string `yaml:"every"`             // "weekdays 08:00", "30m", "cron: 0 8 * * 1-5"
	Between string `yaml:"between,omitempty"` // only for intervals: "08:00-20:00"
	Prompt  string `yaml:"prompt"`            // what the agent is asked each time
	Deliver string `yaml:"deliver,omitempty"` // "chat" (default) or "report"
	Enabled *bool  `yaml:"enabled,omitempty"` // nil = on; a pointer so a missing key means true
}

// On says whether the routine is enabled. A missing enabled key means on.
func (r Routine) On() bool { return r.Enabled == nil || *r.Enabled }

// Find returns the routine with this name and its index, or -1.
func (f File) Find(name string) (Routine, int) {
	for i, r := range f.Routines {
		if r.Name == name {
			return r, i
		}
	}
	return Routine{}, -1
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Load reads routines.yaml from a team folder. No file just means no
// routines yet.
func Load(teamDir string) (File, error) {
	raw, err := os.ReadFile(filepath.Join(teamDir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	// A typo like "evrey:" should be an error, not a routine that
	// silently never runs.
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return File{}, fmt.Errorf("%s: %w", FileName, err)
	}
	if err := f.validate(); err != nil {
		return File{}, fmt.Errorf("%s: %w", FileName, err)
	}
	return f, nil
}

// Save writes routines.yaml. It checks the file first, and writes to a
// temporary file then renames it, so a crash never leaves half a file.
func Save(teamDir string, f File) error {
	if err := f.validate(); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(teamDir, FileName), buf.Bytes())
}

// writeAtomic writes next to the target and renames over it.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// validate checks everything Load and Save promise: good names, no
// duplicates, schedules that parse and a known deliver value.
func (f File) validate() error {
	if strings.TrimSpace(f.QuietHours) != "" {
		if _, err := parseWindow(f.QuietHours); err != nil {
			return fmt.Errorf("quiet_hours: %w", err)
		}
	}
	seen := map[string]bool{}
	for i, r := range f.Routines {
		if r.Name == "" {
			return fmt.Errorf("routine %d has no name", i+1)
		}
		if !nameRe.MatchString(r.Name) {
			return fmt.Errorf("routine %q: name must be lowercase letters, digits and -", r.Name)
		}
		if seen[r.Name] {
			return fmt.Errorf("routine %q appears twice: names must be unique", r.Name)
		}
		seen[r.Name] = true
		if strings.TrimSpace(r.Agent) == "" {
			return fmt.Errorf("routine %q: agent is missing", r.Name)
		}
		if strings.TrimSpace(r.Prompt) == "" {
			return fmt.Errorf("routine %q: prompt is missing", r.Name)
		}
		if _, err := Parse(r.Every, r.Between); err != nil {
			return fmt.Errorf("routine %q: %w", r.Name, err)
		}
		switch r.Deliver {
		case "", "chat", "report":
		default:
			return fmt.Errorf("routine %q: deliver %q: use chat or report", r.Name, r.Deliver)
		}
	}
	return nil
}
