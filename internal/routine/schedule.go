package routine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The shortest interval we allow. Anything faster is a busy loop that
// burns tokens, not a routine.
const minInterval = 5 * time.Minute

type kind int

const (
	kindInterval kind = iota // every 30m, every 2h
	kindDays                 // daily, weekdays, mon,wed at a time of day
	kindCron                 // cron: 0 8 * * 1-5
)

// Schedule says when a routine runs. Build one with Parse.
type Schedule struct {
	kind kind

	// Intervals.
	every  time.Duration
	window *window // only times inside it count; nil = all day

	// Days at a time of day.
	days         [7]bool // indexed by time.Weekday
	hour, minute int

	// Cron.
	cron *cronSpec
}

// window is a span of the day in minutes since midnight. When start is
// after end it crosses midnight (22:00-07:00).
type window struct{ start, end int }

// Parse reads the every and between fields of a routine.
func Parse(every, between string) (Schedule, error) {
	s := strings.ToLower(strings.TrimSpace(every))
	if s == "" {
		return Schedule{}, errors.New("every is empty")
	}

	var sch Schedule
	var err error
	switch {
	case strings.HasPrefix(s, "cron:") || strings.HasPrefix(s, "cron "):
		sch, err = parseCron(strings.TrimSpace(s[len("cron"):]))
	case strings.HasPrefix(s, "every day "):
		sch, err = parseDays("daily", strings.TrimPrefix(s, "every day "))
	default:
		s = strings.TrimSpace(strings.TrimPrefix(s, "every "))
		if d, ok := parseInterval(s); ok {
			sch, err = newInterval(d)
		} else {
			fields := strings.Fields(s)
			if len(fields) < 2 {
				return Schedule{}, fmt.Errorf("can't read every %q: try \"30m\", \"daily 09:30\", \"weekdays 08:00\", \"mon,wed 10:00\" or \"cron: 0 8 * * 1-5\"", every)
			}
			// "mon, wed 10:00" has a space after the comma; glue the
			// day part back together.
			sch, err = parseDays(strings.Join(fields[:len(fields)-1], ""), fields[len(fields)-1])
		}
	}
	if err != nil {
		return Schedule{}, err
	}

	if strings.TrimSpace(between) != "" {
		if sch.kind != kindInterval {
			return Schedule{}, errors.New("between only works with interval schedules like \"30m\"; a daily or cron schedule already says when it runs")
		}
		w, err := parseWindow(between)
		if err != nil {
			return Schedule{}, fmt.Errorf("between: %w", err)
		}
		sch.window = &w
	}
	return sch, nil
}

// parseInterval reads "30m", "1h", "1h30m" and also "hour".
func parseInterval(s string) (time.Duration, bool) {
	if s == "hour" || s == "hourly" {
		return time.Hour, true
	}
	if strings.ContainsAny(s, " :") {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	return d, err == nil
}

func newInterval(d time.Duration) (Schedule, error) {
	switch {
	case d < minInterval:
		return Schedule{}, fmt.Errorf("interval %s is too short: the minimum is 5m", d)
	case d > 24*time.Hour:
		return Schedule{}, fmt.Errorf("interval %s is longer than a day: use \"daily 09:00\" or a day list instead", d)
	case d%time.Minute != 0:
		return Schedule{}, fmt.Errorf("interval %s must be whole minutes", d)
	}
	return Schedule{kind: kindInterval, every: d}, nil
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// parseDays reads the day part ("daily", "weekdays", "mon,wed", "mon-fri")
// and the time of day.
func parseDays(days, clock string) (Schedule, error) {
	h, m, err := parseClock(clock)
	if err != nil {
		return Schedule{}, err
	}
	sch := Schedule{kind: kindDays, hour: h, minute: m}
	switch days {
	case "daily", "everyday":
		for d := range sch.days {
			sch.days[d] = true
		}
		return sch, nil
	case "weekdays":
		for d := time.Monday; d <= time.Friday; d++ {
			sch.days[d] = true
		}
		return sch, nil
	case "weekends":
		sch.days[time.Saturday], sch.days[time.Sunday] = true, true
		return sch, nil
	}
	for _, part := range strings.Split(days, ",") {
		from, to, isRange := strings.Cut(part, "-")
		a, ok := dayNames[from]
		if !ok {
			return Schedule{}, fmt.Errorf("unknown day %q: use mon, tue, wed, thu, fri, sat, sun", from)
		}
		if !isRange {
			sch.days[a] = true
			continue
		}
		b, ok := dayNames[to]
		if !ok {
			return Schedule{}, fmt.Errorf("unknown day %q: use mon, tue, wed, thu, fri, sat, sun", to)
		}
		// fri-mon wraps around the weekend.
		for d := a; ; d = (d + 1) % 7 {
			sch.days[d] = true
			if d == b {
				break
			}
		}
	}
	return sch, nil
}

// parseClock reads "08:00" or "8:00".
func parseClock(s string) (int, int, error) {
	hs, ms, ok := strings.Cut(strings.TrimSpace(s), ":")
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if !ok || err1 != nil || err2 != nil || len(ms) != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("can't read time %q: use HH:MM, like 08:00", s)
	}
	return h, m, nil
}

// parseWindow reads "08:00-20:00". An en dash works too, since that is
// what String prints.
func parseWindow(s string) (window, error) {
	s = strings.ReplaceAll(s, "–", "-")
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return window{}, fmt.Errorf("can't read %q: use HH:MM-HH:MM, like 08:00-20:00", s)
	}
	h1, m1, err := parseClock(a)
	if err != nil {
		return window{}, err
	}
	h2, m2, err := parseClock(b)
	if err != nil {
		return window{}, err
	}
	w := window{start: h1*60 + m1, end: h2*60 + m2}
	if w.start == w.end {
		return window{}, fmt.Errorf("%q starts and ends at the same time", s)
	}
	return w, nil
}

// has says whether a minute of the day is inside the window, end
// included: "08:00-20:00" lets a 20:00 run through.
func (w window) has(min int) bool {
	if w.start <= w.end {
		return min >= w.start && min <= w.end
	}
	return min >= w.start || min <= w.end
}

// Next is the first run strictly after `after`, in after's location.
// It is the zero time when the schedule never runs (a cron for Feb 30).
func (s Schedule) Next(after time.Time) time.Time {
	switch s.kind {
	case kindInterval:
		return s.nextInterval(after)
	case kindDays:
		return s.nextDays(after)
	case kindCron:
		return s.cron.next(after)
	}
	return time.Time{}
}

// nextInterval keeps runs on the clock: midnight plus a whole number of
// intervals, so 1h runs at :00 and 15m at :00, :15, :30, :45. Intervals
// that don't divide the day start over at each midnight.
func (s Schedule) nextInterval(after time.Time) time.Time {
	step := int(s.every / time.Minute)
	y, mo, d := after.Date()
	loc := after.Location()
	// Two days always hold a run; a third covers a window that ends
	// before the first step of the next day.
	for day := 0; day < 3; day++ {
		for min := 0; min < 24*60; min += step {
			if s.window != nil && !s.window.has(min) {
				continue
			}
			// Build the wall-clock time; time.Date handles DST gaps.
			t := time.Date(y, mo, d+day, 0, min, 0, 0, loc)
			if t.After(after) {
				return t
			}
		}
	}
	return time.Time{}
}

func (s Schedule) nextDays(after time.Time) time.Time {
	y, mo, d := after.Date()
	for day := 0; day <= 7; day++ {
		t := time.Date(y, mo, d+day, s.hour, s.minute, 0, 0, after.Location())
		if s.days[t.Weekday()] && t.After(after) {
			return t
		}
	}
	return time.Time{}
}

// String says the schedule in words, for the screen.
func (s Schedule) String() string {
	switch s.kind {
	case kindInterval:
		text := "every " + durationWords(s.every)
		if s.window != nil {
			text += " · " + clockText(s.window.start) + "–" + clockText(s.window.end)
		}
		return text
	case kindDays:
		at := " at " + clockText(s.hour*60+s.minute)
		var names []string
		count := 0
		for d := range s.days {
			if s.days[d] {
				count++
			}
		}
		weekdays := count == 5 && !s.days[time.Saturday] && !s.days[time.Sunday]
		switch {
		case count == 7:
			return "every day" + at
		case weekdays:
			return "weekdays" + at
		}
		// Monday first, the way people read a week.
		for i := 1; i <= 7; i++ {
			d := time.Weekday(i % 7)
			if s.days[d] {
				names = append(names, strings.ToLower(d.String()[:3]))
			}
		}
		return strings.Join(names, ", ") + at
	case kindCron:
		return "cron " + s.cron.text
	}
	return ""
}

func durationWords(d time.Duration) string {
	if d%time.Hour == 0 {
		if h := int(d / time.Hour); h > 1 {
			return fmt.Sprintf("%d hours", h)
		}
		return "hour"
	}
	return fmt.Sprintf("%d min", int(d/time.Minute))
}

func clockText(min int) string { return fmt.Sprintf("%02d:%02d", min/60, min%60) }

// InQuiet says whether t falls in the quiet hours. "" is never quiet.
// The end is not quiet: with "22:00-07:00" a 07:00 run goes ahead.
func InQuiet(quiet string, t time.Time) bool {
	if strings.TrimSpace(quiet) == "" {
		return false
	}
	w, err := parseWindow(quiet)
	if err != nil {
		return false // Load already refused a bad value
	}
	return inQuiet(w, t)
}

func inQuiet(w window, t time.Time) bool {
	min := t.Hour()*60 + t.Minute()
	if w.start < w.end {
		return min >= w.start && min < w.end
	}
	return min >= w.start || min < w.end
}

// NextOutsideQuiet is the next run that is not inside the quiet hours.
// It is the zero time when it finds none (quiet hours that swallow every
// run of the schedule).
func NextOutsideQuiet(s Schedule, quiet string, after time.Time) time.Time {
	var w *window
	if strings.TrimSpace(quiet) != "" {
		if q, err := parseWindow(quiet); err == nil {
			w = &q
		}
	}
	t := after
	for i := 0; i < 1000; i++ {
		t = s.Next(t)
		if t.IsZero() || w == nil || !inQuiet(*w, t) {
			return t
		}
		// Jump to just before the quiet hours end instead of stepping
		// through every 5 minutes of the night.
		t = quietEnd(*w, t).Add(-time.Second)
	}
	return time.Time{}
}

// quietEnd is when the quiet window that holds t is over.
func quietEnd(w window, t time.Time) time.Time {
	y, mo, d := t.Date()
	end := time.Date(y, mo, d, 0, w.end, 0, 0, t.Location())
	if w.start > w.end && t.Hour()*60+t.Minute() >= w.start {
		end = time.Date(y, mo, d+1, 0, w.end, 0, 0, t.Location())
	}
	return end
}

// cronSpec is a classic five-field cron line. Each field is a bit set of
// the values that match.
type cronSpec struct {
	text                     string // the fields as written, one space apart
	minute, hour, dom, month uint64
	dow                      uint64 // 0 = Sunday; a 7 is folded into 0
	domAny, dowAny           bool   // the field starts with "*"
}

// parseCron reads "0 8 * * 1-5": minute, hour, day of month, month, day
// of week.
func parseCron(s string) (Schedule, error) {
	s = strings.TrimSpace(strings.TrimPrefix(s, ":"))
	f := strings.Fields(s)
	if len(f) != 5 {
		return Schedule{}, fmt.Errorf("cron %q needs 5 fields: minute hour day-of-month month day-of-week", s)
	}
	c := &cronSpec{text: strings.Join(f, " ")}
	var err error
	fields := []struct {
		name   string
		lo, hi int
		text   string
		set    *uint64
	}{
		{"minute", 0, 59, f[0], &c.minute},
		{"hour", 0, 23, f[1], &c.hour},
		{"day of month", 1, 31, f[2], &c.dom},
		{"month", 1, 12, f[3], &c.month},
		{"day of week", 0, 7, f[4], &c.dow},
	}
	for _, fl := range fields {
		if *fl.set, err = parseCronField(fl.text, fl.lo, fl.hi); err != nil {
			return Schedule{}, fmt.Errorf("cron %s: %w", fl.name, err)
		}
	}
	if c.dow&(1<<7) != 0 {
		c.dow = c.dow&^(1<<7) | 1
	}
	c.domAny = strings.HasPrefix(f[2], "*")
	c.dowAny = strings.HasPrefix(f[4], "*")

	// Catch lines like "0 0 30 2 *" now, not at 3 a.m. some day.
	if c.next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)).IsZero() {
		return Schedule{}, fmt.Errorf("cron %q never runs", s)
	}
	return Schedule{kind: kindCron, cron: c}, nil
}

// parseCronField reads "*", "5", "1,15", "1-5", "*/15" and "8-18/2".
func parseCronField(s string, lo, hi int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(s, ",") {
		rng, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("bad step in %q", part)
			}
			step = n
		}
		var a, b int
		switch {
		case rng == "*":
			a, b = lo, hi
		case strings.Contains(rng, "-"):
			as, bs, _ := strings.Cut(rng, "-")
			var err1, err2 error
			a, err1 = strconv.Atoi(as)
			b, err2 = strconv.Atoi(bs)
			if err1 != nil || err2 != nil {
				return 0, fmt.Errorf("bad range %q", part)
			}
		default:
			n, err := strconv.Atoi(rng)
			if err != nil {
				return 0, fmt.Errorf("bad value %q", part)
			}
			a, b = n, n
			if hasStep { // "5/15" means from 5 to the end, every 15
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

func has(set uint64, v int) bool { return set&(1<<uint(v)) != 0 }

// dayMatches follows the usual cron rule: when both day fields are
// restricted, either one may match.
func (c *cronSpec) dayMatches(t time.Time) bool {
	dom, dow := has(c.dom, t.Day()), has(c.dow, int(t.Weekday()))
	if c.domAny || c.dowAny {
		return dom && dow
	}
	return dom || dow
}

// next walks forward from the minute after `after`, skipping whole
// months, days and hours that can't match so it stays quick.
func (c *cronSpec) next(after time.Time) time.Time {
	loc := after.Location()
	// Truncate and Add work on absolute time, so a DST change can never
	// send us backwards.
	t := after.Truncate(time.Minute).Add(time.Minute).In(loc)
	limit := after.Year() + 5
	for t.Year() <= limit {
		y, mo, d := t.Date()
		switch {
		case !has(c.month, int(mo)):
			t = time.Date(y, mo+1, 1, 0, 0, 0, 0, loc)
		case !c.dayMatches(t):
			t = time.Date(y, mo, d+1, 0, 0, 0, 0, loc)
		case !has(c.hour, t.Hour()):
			t = time.Date(y, mo, d, t.Hour()+1, 0, 0, 0, loc)
		case !has(c.minute, t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
