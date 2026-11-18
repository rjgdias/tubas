package thread

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"tubas/internal/llm"
)

func thinking(sig string) llm.Block {
	raw, _ := json.Marshal(map[string]string{"type": "thinking", "thinking": "hmm", "signature": sig})
	return llm.Block{Type: "thinking", Text: "hmm", Raw: raw}
}

func open(t *testing.T, dir string) (*Thread, func()) {
	t.Helper()
	th, unlock, err := Open(dir, "s1", "dev")
	if err != nil {
		t.Fatal(err)
	}
	return th, unlock
}

func TestTheHistoryOnlyGrows(t *testing.T) {
	dir := t.TempDir()
	th, unlock := open(t, dir)
	th.Add(llm.UserText("first"), "A")
	th.AddAnswer(llm.Message{Role: "assistant", Blocks: []llm.Block{thinking("S1"), {Type: "text", Text: "one"}}}, "A", 100)
	unlock()
	path, _ := Path(dir, "s1", "dev")
	before, _ := os.ReadFile(path)

	th, unlock = open(t, dir)
	defer unlock()
	if th.Empty() || th.LastInput() != 100 {
		t.Fatalf("read back: empty=%v input=%d", th.Empty(), th.LastInput())
	}
	th.Add(llm.UserText("second"), "A")
	after, _ := os.ReadFile(path)
	if !bytes.HasPrefix(after, before) {
		t.Error("the file was rewritten, not appended to")
	}
	msgs := th.Messages("A")
	if len(msgs) != 3 || msgs[1].Blocks[0].Type != "thinking" || !strings.Contains(string(msgs[1].Blocks[0].Raw), `"S1"`) {
		t.Errorf("the thinking block goes back unchanged: %+v", msgs)
	}
}

func TestAStoppedTurnIsMadeWhole(t *testing.T) {
	dir := t.TempDir()
	th, unlock := open(t, dir)
	th.Add(llm.UserText("go"), "A")
	th.AddAnswer(llm.Message{Role: "assistant", Blocks: []llm.Block{{Type: "tool_use", ID: "t1", Name: "bash", Input: json.RawMessage(`{}`)}}}, "A", 10)
	unlock()
	path, _ := Path(dir, "s1", "dev")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"kind":"message","role":"user","blo`) // a crash in the middle of a line
	f.Close()

	th, unlock = open(t, dir)
	defer unlock()
	th.Add(llm.UserText("again"), "A")
	msgs := th.Messages("A")
	last := msgs[len(msgs)-1]
	if len(msgs) != 3 || last.Role != "user" || last.Blocks[0].Type != "tool_result" || last.Blocks[0].ID != "t1" || last.Blocks[1].Text != "again" {
		t.Errorf("the call gets a result, the new question joins it: %+v", msgs)
	}
}

func TestACompactionStartsTheNextRequests(t *testing.T) {
	th, unlock := open(t, t.TempDir())
	defer unlock()
	th.Add(llm.UserText("old question"), "A")
	th.AddAnswer(llm.Message{Role: "assistant", Blocks: []llm.Block{thinking("S1"), {Type: "text", Text: "old answer"}}}, "A", 200000)
	th.Compact("we found the bug in auth.go")
	if th.LastInput() != 0 {
		t.Errorf("after a compaction the size starts over: %d", th.LastInput())
	}
	th.Add(llm.UserText("new question"), "A")
	msgs := th.Messages("A")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Blocks[0].Text, "we found the bug") || msgs[0].Blocks[1].Text != "new question" {
		t.Errorf("only the summary and what came after: %+v", msgs)
	}
}

func TestThinkingFromAnotherSetupGoesOldestFirst(t *testing.T) {
	th, unlock := open(t, t.TempDir())
	defer unlock()
	th.Add(llm.UserText("q1"), "A")
	th.AddAnswer(llm.Message{Role: "assistant", Blocks: []llm.Block{thinking("S1"), {Type: "text", Text: "a1"}}}, "A", 1)
	th.Add(llm.UserText("q2"), "B") // a new session: another worktree in the prompt
	th.AddAnswer(llm.Message{Role: "assistant", Blocks: []llm.Block{thinking("S2"), {Type: "text", Text: "a2"}}}, "B", 1)
	th.Add(llm.UserText("q3"), "B")

	under := func(frame string) (sigs []string) {
		for _, m := range th.Messages(frame) {
			for _, b := range m.Blocks {
				if b.Type == "thinking" {
					sigs = append(sigs, string(b.Raw))
				}
			}
		}
		return sigs
	}
	if got := under("B"); len(got) != 1 || !strings.Contains(got[0], "S2") {
		t.Errorf("under B only the newer thinking stays: %v", got)
	}
	if got := under("C"); len(got) != 0 {
		t.Errorf("under a new setup none stays: %v", got)
	}
	if n := len(th.Messages("C")); n != 5 {
		t.Errorf("the text stays: %d messages", n)
	}
}

func TestNamesCantLeaveTheFolder(t *testing.T) {
	for _, bad := range [][2]string{{"../x", "dev"}, {"s1", "../../etc"}, {"a/b", "dev"}, {"s1", ""}, {"..", "dev"}} {
		if _, err := Path("dir", bad[0], bad[1]); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}
}
