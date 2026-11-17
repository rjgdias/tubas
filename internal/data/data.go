// Package data holds the synthetic delivery database the agents query.
//
// The data is made up but shaped like a real delivery app: orders,
// deliveries, couriers, daily loads and coupons, from 2026-02-01 to
// 2026-03-28. It hides one story on purpose: in March the south region
// ("sul") lost about a quarter of its couriers, and dinner orders there
// dropped. Agents can find it with SQL, the way an analyst would.
//
// Table and column names are in Portuguese because they are the team's
// data, like the agents' answers; the code around them is in English.
package data

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; pure Go, no C compiler needed
)

const schema = `
CREATE TABLE pedidos (
  id            INTEGER PRIMARY KEY,
  criado_em     TEXT NOT NULL,  -- 2026-03-14 20:31
  dia           TEXT NOT NULL,  -- 2026-03-14
  mes           INTEGER NOT NULL,
  regiao        TEXT NOT NULL,  -- centro, leste, norte, oeste, sul
  faixa_horaria TEXT NOT NULL,  -- cafe, almoco, tarde, jantar, madrugada
  categoria     TEXT NOT NULL,
  valor         REAL NOT NULL,
  cupom         TEXT,           -- NULL when no coupon
  status        TEXT NOT NULL   -- entregue, cancelado
);
CREATE TABLE entregadores (
  id          INTEGER PRIMARY KEY,
  regiao      TEXT NOT NULL,
  ativo_desde TEXT NOT NULL,
  ativo_ate   TEXT             -- NULL while still active
);
CREATE TABLE entregas (
  pedido_id     INTEGER NOT NULL REFERENCES pedidos(id),
  entregador_id INTEGER NOT NULL REFERENCES entregadores(id),
  minutos       INTEGER NOT NULL
);
CREATE TABLE cargas (
  tabela       TEXT NOT NULL,
  dia          TEXT NOT NULL,
  status       TEXT NOT NULL,   -- ok, falha
  linhas       INTEGER NOT NULL,
  concluida_em TEXT NOT NULL
);
CREATE TABLE cupons (
  codigo   TEXT PRIMARY KEY,
  desconto REAL NOT NULL,
  inicio   TEXT NOT NULL,
  fim      TEXT NOT NULL
);
CREATE INDEX pedidos_dia ON pedidos(dia);
CREATE INDEX entregas_pedido ON entregas(pedido_id);
`

type region struct {
	name     string
	perDay   float64 // orders on a normal weekday
	couriers int
}

type slot struct {
	name       string
	share      float64
	start, end int // hours
}

var (
	regions = []region{
		{"centro", 100, 70}, {"leste", 80, 55}, {"norte", 70, 50}, {"oeste", 60, 42}, {"sul", 140, 60},
	}
	slots = []slot{
		{"cafe", .08, 7, 10}, {"almoco", .32, 11, 14}, {"tarde", .12, 15, 17},
		{"jantar", .40, 18, 22}, {"madrugada", .08, 23, 24},
	}
	categories = []struct {
		name  string
		price float64
	}{
		{"pizza", 62}, {"japonesa", 78}, {"lanches", 41}, {"brasileira", 38}, {"saudavel", 45}, {"mercado", 95},
	}
	coupons = []string{"BEMVINDO10", "FRETEGRATIS", "JANTAR15"}

	firstDay = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	lastDay  = time.Date(2026, 3, 28, 0, 0, 0, 0, time.UTC)
	march    = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
)

type courier struct {
	id         int
	region     string
	activeFrom time.Time
	activeTo   *time.Time
}

// Seed creates the database at path, replacing any old one. The same
// seed always produces the same data, so answers are reproducible.
func Seed(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_ = os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}

	rng := rand.New(rand.NewPCG(42, 2026))
	tx, err := db.Begin() // one transaction: thousands of inserts in a blink
	if err != nil {
		return err
	}
	defer tx.Rollback()

	insCourier, _ := tx.Prepare(`INSERT INTO entregadores VALUES (?, ?, ?, ?)`)
	insOrder, _ := tx.Prepare(`INSERT INTO pedidos VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	insDelivery, _ := tx.Prepare(`INSERT INTO entregas VALUES (?, ?, ?)`)
	insLoad, _ := tx.Prepare(`INSERT INTO cargas VALUES (?, ?, ?, ?, ?)`)

	// Couriers. In the south, 14 of 60 leave between Feb 24 and Mar 3.
	var all []courier
	id := 0
	for _, r := range regions {
		for i := 0; i < r.couriers; i++ {
			id++
			c := courier{id: id, region: r.name, activeFrom: firstDay.AddDate(0, -rng.IntN(18)-1, -rng.IntN(28))}
			if r.name == "sul" && i < 14 {
				to := time.Date(2026, 2, 24+rng.IntN(8), 0, 0, 0, 0, time.UTC)
				c.activeTo = &to
			}
			var to any
			if c.activeTo != nil {
				to = c.activeTo.Format("2006-01-02")
			}
			if _, err := insCourier.Exec(c.id, c.region, c.activeFrom.Format("2006-01-02"), to); err != nil {
				return err
			}
			all = append(all, c)
		}
	}

	if _, err := tx.Exec(`INSERT INTO cupons VALUES
		('BEMVINDO10', 10, '2026-01-01', '2026-12-31'),
		('FRETEGRATIS', 7.9, '2026-01-01', '2026-12-31'),
		('JANTAR15', 15, '2026-01-01', '2026-12-31')`); err != nil {
		return err
	}

	orderID := 0
	for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
		dayText := day.Format("2006-01-02")
		weekday := map[time.Weekday]float64{time.Friday: 1.25, time.Saturday: 1.3, time.Sunday: 1.15}[day.Weekday()]
		if weekday == 0 {
			weekday = 1
		}
		ordersToday, deliveriesToday := 0, 0

		for _, r := range regions {
			active := activeCouriers(all, r.name, day)
			total := r.perDay * weekday * (0.93 + rng.Float64()*0.14)
			for _, s := range slots {
				effect := 1.0
				if r.name == "sul" && !day.Before(march) {
					effect = 0.93 // fewer couriers: slower everywhere...
					if s.name == "jantar" {
						effect = 0.68 // ...and dinner, the peak, suffers most
					}
				}
				count := int(math.Round(total * s.share * effect))
				for k := 0; k < count; k++ {
					orderID++
					hour := s.start + rng.IntN(s.end-s.start)
					created := day.Add(time.Duration(hour)*time.Hour + time.Duration(rng.IntN(60))*time.Minute)
					cat := categories[rng.IntN(len(categories))]
					value := math.Round(cat.price*(0.8+rng.Float64()*0.6)*100) / 100
					var coupon any
					if rng.Float64() < 0.18 {
						coupon = coupons[rng.IntN(len(coupons))]
					}
					crowded := r.name == "sul" && s.name == "jantar" && !day.Before(march)
					status, cancelChance := "entregue", 0.04
					if crowded {
						cancelChance = 0.09
					}
					if rng.Float64() < cancelChance {
						status = "cancelado"
					}
					if _, err := insOrder.Exec(orderID, created.Format("2006-01-02 15:04"), dayText, int(day.Month()),
						r.name, s.name, cat.name, value, coupon, status); err != nil {
						return err
					}
					ordersToday++
					if status == "entregue" && len(active) > 0 {
						minutes := 26 + rng.IntN(15)
						if crowded {
							minutes += 12
						}
						if _, err := insDelivery.Exec(orderID, active[rng.IntN(len(active))].id, minutes); err != nil {
							return err
						}
						deliveriesToday++
					}
				}
			}
		}

		done := day.AddDate(0, 0, 1).Add(3*time.Hour + time.Duration(rng.IntN(40))*time.Minute).Format("2006-01-02 15:04")
		for table, rows := range map[string]int{"pedidos": ordersToday, "entregas": deliveriesToday, "entregadores": len(all)} {
			if _, err := insLoad.Exec(table, dayText, "ok", rows, done); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func activeCouriers(all []courier, region string, day time.Time) []courier {
	var out []courier
	for _, c := range all {
		if c.region == region && !c.activeFrom.After(day) && (c.activeTo == nil || day.Before(*c.activeTo)) {
			out = append(out, c)
		}
	}
	return out
}

// ── reading ─────────────────────────────────────────────────────────────

// open opens the database read-only: even a clever query cannot change it.
func open(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("database not found at %s (run: tubas seed)", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite", "file:"+filepath.ToSlash(abs)+"?mode=ro&_pragma=query_only(1)")
}

// Query runs one read-only SQL statement and formats the result as text:
// a first line like "12 rows · regiao, pedidos", then one row per line.
func Query(path, query string, maxRows int) (string, error) {
	q := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), ";"))
	lower := strings.ToLower(q)
	if !strings.HasPrefix(lower, "select") && !strings.HasPrefix(lower, "with") {
		return "", errors.New("only SELECT (or WITH ... SELECT) queries are allowed")
	}
	if strings.Contains(q, ";") {
		return "", errors.New("send one statement at a time")
	}

	db, err := open(path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	rows, err := db.Query(q)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	var lines []string
	total := 0
	values := make([]any, len(cols))
	pointers := make([]any, len(cols))
	for i := range values {
		pointers[i] = &values[i]
	}
	for rows.Next() {
		total++
		if total > maxRows {
			continue // keep counting, stop printing
		}
		if err := rows.Scan(pointers...); err != nil {
			return "", err
		}
		cells := make([]string, len(cols))
		for i, v := range values {
			cells[i] = formatValue(v)
		}
		lines = append(lines, strings.Join(cells, " | "))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	head := fmt.Sprintf("%d rows · %s", total, strings.Join(cols, ", "))
	if total == 1 {
		head = "1 row · " + strings.Join(cols, ", ")
	}
	// Every step of an agent resends earlier results, so big ones are
	// costly twice: cap the rows and the characters, and say so.
	shown := len(lines)
	for size := len(head); shown > 0; shown-- {
		size = len(head)
		for _, l := range lines[:shown] {
			size += len(l) + 1
		}
		if size <= maxResultChars {
			break
		}
	}
	if shown < total {
		head += fmt.Sprintf(" (showing %d: aggregate or filter to see the rest)", shown)
	}
	return strings.Join(append([]string{head}, lines[:shown]...), "\n"), nil
}

// maxResultChars caps what one query sends back to the model.
const maxResultChars = 4000

// Schema describes every table in one compact line each, like
// "pedidos(id INTEGER, dia TEXT, ...)". It goes in the run_sql tool's
// description, so agents skip the discovery steps.
func Schema(path string) (string, error) {
	db, err := open(path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT m.name, p.name, p.type FROM sqlite_master m
		JOIN pragma_table_info(m.name) p WHERE m.type = 'table' ORDER BY m.name, p.cid`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var tables []string
	cols := map[string][]string{}
	for rows.Next() {
		var table, col, typ string
		if err := rows.Scan(&table, &col, &typ); err != nil {
			return "", err
		}
		if _, ok := cols[table]; !ok {
			tables = append(tables, table)
		}
		cols[table] = append(cols[table], col+" "+typ)
	}
	lines := make([]string, len(tables))
	for i, t := range tables {
		lines[i] = t + "(" + strings.Join(cols[t], ", ") + ")"
	}
	return strings.Join(lines, "\n"), rows.Err()
}

func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return fmt.Sprintf("%.0f", x)
		}
		return fmt.Sprintf("%.2f", x)
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}

// Tables lists the tables.
func Tables(path string) (string, error) {
	return Query(path, `SELECT name AS tabela FROM sqlite_master WHERE type = 'table' ORDER BY name`, 100)
}

// Describe lists a table's columns.
func Describe(path, table string) (string, error) {
	for _, r := range table {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return "", fmt.Errorf("invalid table name %q", table)
		}
	}
	return Query(path, fmt.Sprintf(`SELECT name AS coluna, type AS tipo FROM pragma_table_info('%s')`, table), 100)
}
