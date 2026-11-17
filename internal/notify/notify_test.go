package notify

import (
	"strings"
	"testing"
)

func TestScriptQuotes(t *testing.T) {
	s := Script("tubas · dados", "leo's deploy\nis waiting")
	if !strings.Contains(s, "'leo''s deploy is waiting'") || !strings.Contains(s, "ShowBalloonTip(8000, 'tubas · dados'") {
		t.Errorf("quotes and newlines must be safe in PowerShell:\n%s", s)
	}
}
