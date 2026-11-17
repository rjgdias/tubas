package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTurnEffort(t *testing.T) {
	cases := []struct {
		agent string
		kind  TurnKind
		want  string
	}{
		{"", KindAsk, ""},          // the model's default
		{"", KindWorker, "low"},    // parallel workers think little...
		{"", KindTeam, "low"},      // ...and so do agents that may pass
		{"high", KindTeam, "high"}, // unless their file says otherwise
	}
	for _, c := range cases {
		if got := turnEffort(c.agent, c.kind); got != c.want {
			t.Errorf("turnEffort(%q, %s) = %q, want %q", c.agent, c.kind, got, c.want)
		}
	}
}

func TestBriefCutsLongAnswers(t *testing.T) {
	long := strings.Repeat("palavra ", 300)
	got := brief("## Conclusão\n\n" + long)
	if n := len([]rune(got)); n > briefLimit+10 || !strings.HasSuffix(got, "(cut)") || strings.Contains(got, "\n") {
		t.Errorf("brief gave %d runes: %q…", n, got[:40])
	}
	if brief("curto.") != "curto." {
		t.Error("short answers stay as they are")
	}
}

// The recorded stream carries the cost and the cache split.
func TestParseReadsCostAndCache(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join("testdata", "text_only.jsonl"))
	_, reply, err := collectStream(t, string(data), 0)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Cost != 0.000205 || reply.Cached != 0 {
		t.Errorf("cost %v, cached %d", reply.Cost, reply.Cached)
	}
	_, reply, _ = collectStream(t, toolStream, 0)
	if reply.Cached != 30 {
		t.Errorf("cached = %d, want 30", reply.Cached)
	}
}

func TestSystemPromptAsksForFewSteps(t *testing.T) {
	if p := SystemPrompt(demoTeam["iris"], demoTeam); !strings.Contains(p, "call those tools together in the same step") {
		t.Error("the system prompt should ask for batched tool calls")
	}
}
