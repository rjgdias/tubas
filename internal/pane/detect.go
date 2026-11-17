package pane

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ── reading an agent's screen ───────────────────────────────────────────
//
// Each agent CLI shows its state its own way: claude prints "esc to
// interrupt" while it works and "Do you want to proceed?" when it needs
// you; codex asks "Would you like to run the following command?". A
// Detector holds those phrases for one agent. tubas picks it by the
// program a pane runs, or by what is on screen when you started the
// agent from a shell.
//
// The built-in ones can be changed or extended with YAML files, one per
// agent, in ~/.tubas/detect/ or the team's detect/ folder:
//
//	name: claude
//	commands: [claude]
//	signature: ["? for shortcuts"]
//	blocked: ["do you want to proceed"]
//	working: ["esc to interrupt"]
//	resume: ["--continue"]

// Detector reads one agent CLI's screen. Phrases are matched without
// case against the last rows of the screen.
type Detector struct {
	Name      string   `yaml:"name"`
	Commands  []string `yaml:"commands"`  // program names that run it
	Signature []string `yaml:"signature"` // phrases only it prints, to spot it started from a shell (keep them distinctive)
	Blocked   []string `yaml:"blocked"`   // it asks you something
	Working   []string `yaml:"working"`   // it is working
	Resume    []string `yaml:"resume"`    // arguments that continue its last conversation, after a restart
}

// Detectors are the agents tubas knows out of the box.
var Detectors = []Detector{
	{Name: "claude", Commands: []string{"claude"},
		Signature: []string{"? for shortcuts", "claude code v"},
		Blocked: []string{"do you want to proceed", "do you want to make this edit", "do you want to create",
			"do you want to allow", "yes, and don't ask again", "❯ 1. yes", "yes, i trust this folder", "enter to confirm"},
		Working: []string{"esc to interrupt"},
		Resume:  []string{"--continue"}},
	{Name: "codex", Commands: []string{"codex"},
		Signature: []string{"openai codex"},
		Blocked: []string{"would you like to run the following command", "would you like to make the following edits",
			"allow command?", "yes, proceed", "approve this"},
		Working: []string{"esc to interrupt"},
		Resume:  []string{"resume", "--last"}},
	{Name: "gemini", Commands: []string{"gemini"},
		Signature: []string{"gemini cli"},
		Blocked:   []string{"allow execution", "apply this change", "yes, allow once", "allow once"},
		Working:   []string{"esc to cancel"}},
	{Name: "qwen", Commands: []string{"qwen"},
		Signature: []string{"qwen code"},
		Blocked:   []string{"allow execution", "apply this change", "yes, allow once"},
		Working:   []string{"esc to cancel"}},
	{Name: "copilot", Commands: []string{"copilot"},
		Signature: []string{"github copilot"},
		Blocked:   []string{"do you want to run this command", "do you want to edit", "yes, and approve"},
		Working:   []string{"esc to cancel"},
		Resume:    []string{"--continue"}},
	{Name: "opencode", Commands: []string{"opencode"},
		Blocked: []string{"permission required", "allow once", "allow always"},
		Working: []string{"esc interrupt", "esc to interrupt"},
		Resume:  []string{"--continue"}},
	{Name: "crush", Commands: []string{"crush"},
		Blocked: []string{"permission required", "allow for session"},
		Working: []string{"esc to cancel"}},
	{Name: "aider", Commands: []string{"aider"},
		Signature: []string{"aider v"},
		Blocked:   []string{"(y)es/(n)o", "[yes]:"},
		Resume:    []string{"--restore-chat-history"}},
	{Name: "goose", Commands: []string{"goose"},
		Signature: []string{"goose is running"},
		Blocked:   []string{"allow?", "(y/n)"}},
	{Name: "cursor", Commands: []string{"cursor-agent"},
		Signature: []string{"cursor agent"},
		Blocked:   []string{"run this command?", "(y) run", "allow"},
		Working:   []string{"ctrl+c to stop"}},
	{Name: "amp", Commands: []string{"amp"},
		Blocked: []string{"approve", "allow this"},
		Working: []string{"esc to cancel"}},
	{Name: "kimi", Commands: []string{"kimi"},
		Blocked: []string{"approve", "allow"},
		Working: []string{"esc to interrupt"}},
}

// LoadDetectors returns the built-in detectors with the YAML files in
// dirs applied on top: a file with a built-in's name replaces it.
func LoadDetectors(dirs ...string) []Detector {
	out := append([]Detector(nil), Detectors...)
	for _, dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var d Detector
			if yaml.Unmarshal(data, &d) != nil || d.Name == "" {
				continue
			}
			replaced := false
			for i := range out {
				if out[i].Name == d.Name {
					out[i], replaced = d, true
				}
			}
			if !replaced {
				out = append(out, d)
			}
		}
	}
	return out
}

// byCommand finds the detector for a program ("claude", "C:\...\codex.exe").
func byCommand(dets []Detector, command string) *Detector {
	base := strings.ToLower(filepath.Base(command))
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd")
	for i := range dets {
		for _, c := range dets[i].Commands {
			if strings.EqualFold(c, base) {
				return &dets[i]
			}
		}
	}
	return nil
}

// bySignature finds the detector whose signature is on screen.
func bySignature(dets []Detector, lines []string) *Detector {
	for i := range dets {
		if matches(lines, dets[i].Signature) {
			return &dets[i]
		}
	}
	return nil
}

// matches says whether any line holds any phrase.
func matches(lines, phrases []string) bool {
	for _, l := range lines {
		l = strings.ToLower(l)
		for _, p := range phrases {
			if p != "" && strings.Contains(l, strings.ToLower(p)) {
				return true
			}
		}
	}
	return false
}

// ResumeArgs adds the agent's resume arguments to a command, for a pane
// that comes back after a restart.
func ResumeArgs(dets []Detector, command string, args []string) []string {
	d := byCommand(dets, command)
	if d == nil || len(d.Resume) == 0 {
		return args
	}
	for _, a := range args {
		if a == d.Resume[0] {
			return args // already resuming
		}
	}
	return append(append([]string(nil), d.Resume...), args...)
}
