package data

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func seeded(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tubas.db")
	if err := Seed(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// number runs a query that returns one number.
func number(t *testing.T, path, q string) float64 {
	t.Helper()
	out, err := Query(path, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	v, err := strconv.ParseFloat(lines[1], 64)
	if err != nil {
		t.Fatalf("not a number: %q", out)
	}
	return v
}

// The story hidden in the data must be there for agents to find.
func TestTheStoryIsInTheData(t *testing.T) {
	path := seeded(t)
	perDay := func(region, month string) float64 {
		return number(t, path, `SELECT COUNT(*) * 1.0 / COUNT(DISTINCT dia) FROM pedidos
			WHERE regiao LIKE '`+region+`' AND mes = `+month)
	}
	south := perDay("sul", "3")/perDay("sul", "2") - 1
	if south > -0.12 || south < -0.25 {
		t.Errorf("south should drop 12-25%% per day in March, got %.1f%%", south*100)
	}
	others := perDay("norte", "3")/perDay("norte", "2") - 1
	if others < -0.05 {
		t.Errorf("other regions should stay flat, north moved %.1f%%", others*100)
	}
	total := number(t, path, `SELECT (SELECT COUNT(*) FROM pedidos WHERE mes = 3) * 1.0 / (SELECT COUNT(*) FROM pedidos WHERE mes = 2) - 1`)
	if total >= 0 {
		t.Errorf("March total should be below February (same number of days), got %+.1f%%", total*100)
	}
	couriers := number(t, path, `SELECT COUNT(*) FROM entregadores WHERE regiao = 'sul' AND ativo_ate IS NOT NULL`)
	if couriers != 14 {
		t.Errorf("14 south couriers should leave, got %v", couriers)
	}
	failed := number(t, path, `SELECT COUNT(*) FROM cargas WHERE status <> 'ok'`)
	if failed != 0 {
		t.Errorf("the pipeline should be healthy, got %v failed loads", failed)
	}
}

func TestQueryIsReadOnly(t *testing.T) {
	path := seeded(t)
	for _, q := range []string{
		"DELETE FROM pedidos",
		"SELECT 1; DROP TABLE pedidos",
		"UPDATE pedidos SET valor = 0",
	} {
		if _, err := Query(path, q, 10); err == nil {
			t.Errorf("%q should be refused", q)
		}
	}
	// Even a WITH that tries to sneak in a write fails: the file is opened read-only.
	if _, err := Query(path, "WITH x AS (SELECT 1) DELETE FROM pedidos", 10); err == nil {
		t.Error("a write hidden in WITH should fail")
	}
}

func TestQueryFormat(t *testing.T) {
	path := seeded(t)
	out, err := Query(path, "SELECT regiao, COUNT(*) AS n FROM pedidos GROUP BY regiao ORDER BY regiao", 2)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if lines[0] != "5 rows · regiao, n (showing 2: aggregate or filter to see the rest)" {
		t.Errorf("header = %q", lines[0])
	}
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "centro | ") {
		t.Errorf("rows = %q", lines[1:])
	}
}

func TestDescribeRejectsInjection(t *testing.T) {
	path := seeded(t)
	if _, err := Describe(path, "pedidos'); DROP TABLE pedidos; --"); err == nil {
		t.Error("a weird table name should be refused")
	}
	out, err := Describe(path, "pedidos")
	if err != nil || !strings.Contains(out, "faixa_horaria") {
		t.Errorf("describe pedidos = %q, %v", out, err)
	}
}

// Big results are cut by characters too, so one query never floods the
// context that every later step resends.
func TestQueryCapsCharacters(t *testing.T) {
	path := seeded(t)
	out, err := Query(path, "SELECT * FROM pedidos", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > maxResultChars || !strings.Contains(out, "aggregate or filter") {
		t.Errorf("result has %d chars; header %q", len(out), strings.SplitN(out, "\n", 2)[0])
	}
}

func TestSchemaListsEveryTable(t *testing.T) {
	schema, err := Schema(seeded(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pedidos(id INTEGER", "faixa_horaria TEXT", "entregadores(", "cargas(", "cupons("} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema should contain %q:\n%s", want, schema)
		}
	}
}
