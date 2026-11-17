package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func plain(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return strings.Join(out, "\n")
}

func TestRenderMarkdown(t *testing.T) {
	md := "## Conclusão\n\n\nA queda é **real**, veja `pedidos`.\n- sul −18%\n```sql\nSELECT 1\n```\n"
	got := plain(renderMarkdown(md, 80))
	want := "Conclusão\n\nA queda é real, veja pedidos.\n• sul −18%\n  SELECT 1"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPlainSummary(t *testing.T) {
	cases := map[string]string{
		"## Conclusão\n\nA queda está no **jantar do sul**.\nMais texto.": "Conclusão: A queda está no jantar do sul.",
		"**Conclusão: aprovado.** Publicado.":                             "Conclusão: aprovado. Publicado.",
		"queda de 18%.":                                                   "queda de 18%.",
		"Resposta direta para a @iris:\n\nA pipeline está ok.":            "Resposta direta para a @iris: A pipeline está ok.",
	}
	for in, want := range cases {
		if got := plainSummary(in); got != want {
			t.Errorf("plainSummary(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUsageText(t *testing.T) {
	if got := usageText(57800, 38800, 0.0066); got != "57.8k tok · 67% cached · $0.007" {
		t.Errorf("got %q", got)
	}
	if got := usageText(4200, 0, 0); got != "4.2k tok" {
		t.Errorf("without cost or cache: %q", got)
	}
}
