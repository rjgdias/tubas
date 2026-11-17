package engine

import (
	"strings"
	"testing"
)

func TestOlderClaudesGetOnlyOptionsTheyKnow(t *testing.T) {
	args := []string{"-p", "--output-format", "stream-json", "--model", "sonnet",
		"--tools", "", "--permission-prompts", "none", "--setting-sources", "", "--disable-slash-commands",
		"--strict-mcp-config", "--settings", `{"apiKeyHelper":"x"}`}
	// What an older claude lists in --help.
	old := map[string]bool{"--print": true, "--output-format": true, "--model": true, "--disallowedTools": true,
		"--strict-mcp-config": true}
	kept, dropped := compatArgs(args, old)
	got := strings.Join(kept, " ")
	want := "-p --output-format stream-json --model sonnet --disallowedTools " + builtinTools + " --strict-mcp-config"
	if got != want {
		t.Errorf("kept:\n%s\nwant:\n%s", got, want)
	}
	if strings.Join(dropped, " ") != "--tools --permission-prompts --setting-sources --disable-slash-commands --settings" {
		t.Errorf("dropped = %v", dropped)
	}
	// A value that looks like an option is still a value: kept with its flag.
	kept, _ = compatArgs([]string{"--model", "sonnet", "--system-prompt", "--tools is a word"},
		map[string]bool{"--model": true, "--system-prompt": true})
	if len(kept) != 4 {
		t.Errorf("values stay with their option: %q", kept)
	}
	// When --help can't be read, nothing is left out.
	if kept, dropped := compatArgs(args, nil); len(kept) != len(args) || dropped != nil {
		t.Error("unknown flags: keep everything")
	}
}
