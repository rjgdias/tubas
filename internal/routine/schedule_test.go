package routine

import (
	"strings"
	"testing"
	"time"
)

// brt is a fixed non-UTC zone, so tests show Next works in after's
// location and not in UTC.
var brt = time.FixedZone("BRT", -3*3600)

func at(loc *time.Location, y int, mo time.Month, d, h, m int) time.Time {
	return time.Date(y, mo, d, h, m, 0, 0, loc)
}

// 2026-11-16 is a Monday.
func TestNext(t *testing.T) {
	u := time.UTC
	tests := []struct {
		every, between string
		after, want    time.Time
	}{
		// Intervals line up with the clock.
		{"1h", "", at(u, 2026, 11, 16, 10, 20), at(u, 2026, 11, 16, 11, 0)},
		{"1h", "", at(u, 2026, 11, 16, 11, 0), at(u, 2026, 11, 16, 12, 0)},
		{"every hour", "", at(u, 2026, 11, 16, 23, 5), at(u, 2026, 11, 17, 0, 0)},
		{"30m", "", at(u, 2026, 11, 16, 10, 20), at(u, 2026, 11, 16, 10, 30)},
		{"every 15m", "", at(brt, 2026, 11, 16, 10, 46), at(brt, 2026, 11, 16, 11, 0)},
		{"2h", "", at(brt, 2026, 11, 16, 9, 0), at(brt, 2026, 11, 16, 10, 0)},
		// 7h doesn't divide a day: 00, 07, 14, 21, then midnight again.
		{"7h", "", at(u, 2026, 11, 16, 15, 0), at(u, 2026, 11, 16, 21, 0)},
		{"7h", "", at(u, 2026, 11, 16, 22, 0), at(u, 2026, 11, 17, 0, 0)},
		// Between: only times in the window, end included.
		{"2h", "08:00-20:00", at(u, 2026, 11, 16, 19, 30), at(u, 2026, 11, 16, 20, 0)},
		{"2h", "08:00-20:00", at(u, 2026, 11, 16, 20, 0), at(u, 2026, 11, 17, 8, 0)},
		{"2h", "08:00-20:00", at(u, 2026, 11, 16, 3, 0), at(u, 2026, 11, 16, 8, 0)},
		// A window that crosses midnight.
		{"30m", "22:00-02:00", at(u, 2026, 11, 16, 21, 10), at(u, 2026, 11, 16, 22, 0)},
		{"30m", "22:00-02:00", at(u, 2026, 11, 16, 23, 45), at(u, 2026, 11, 17, 0, 0)},
		{"30m", "22:00-02:00", at(brt, 2026, 11, 17, 1, 50), at(brt, 2026, 11, 17, 2, 0)},
		{"30m", "22:00-02:00", at(brt, 2026, 11, 17, 2, 0), at(brt, 2026, 11, 17, 22, 0)},

		// Days at a time of day.
		{"daily 09:30", "", at(u, 2026, 11, 16, 9, 29), at(u, 2026, 11, 16, 9, 30)},
		{"daily 09:30", "", at(u, 2026, 11, 16, 9, 30), at(u, 2026, 11, 17, 9, 30)},
		{"every day 9:30", "", at(brt, 2026, 11, 16, 23, 0), at(brt, 2026, 11, 17, 9, 30)},
		{"weekdays 08:00", "", at(u, 2026, 11, 20, 9, 0), at(u, 2026, 11, 23, 8, 0)}, // Fri -> Mon
		{"weekdays 08:00", "", at(u, 2026, 11, 21, 7, 0), at(u, 2026, 11, 23, 8, 0)}, // Sat -> Mon
		{"weekdays 08:00", "", at(u, 2026, 11, 16, 7, 0), at(u, 2026, 11, 16, 8, 0)},
		{"mon,wed 10:00", "", at(u, 2026, 11, 16, 10, 0), at(u, 2026, 11, 18, 10, 0)},
		{"mon, wed 10:00", "", at(u, 2026, 11, 18, 11, 0), at(u, 2026, 11, 23, 10, 0)},
		{"mon-fri 08:00", "", at(brt, 2026, 11, 22, 12, 0), at(brt, 2026, 11, 23, 8, 0)},
		{"sat 09:00", "", at(u, 2026, 11, 16, 12, 0), at(u, 2026, 11, 21, 9, 0)},
		{"fri-mon 07:00", "", at(u, 2026, 11, 16, 8, 0), at(u, 2026, 11, 20, 7, 0)}, // wraps the weekend

		// Cron.
		{"cron: 0 8 * * 1-5", "", at(u, 2026, 11, 20, 8, 0), at(u, 2026, 11, 23, 8, 0)},
		{"cron: 0 8 * * 1-5", "", at(brt, 2026, 11, 16, 7, 59), at(brt, 2026, 11, 16, 8, 0)},
		{"cron: */15 9-17/4 * * *", "", at(u, 2026, 11, 16, 10, 0), at(u, 2026, 11, 16, 13, 0)},
		{"cron: */15 9-17/4 * * *", "", at(u, 2026, 11, 16, 13, 50), at(u, 2026, 11, 16, 17, 0)},
		{"cron: */15 9-17/4 * * *", "", at(u, 2026, 11, 16, 17, 45), at(u, 2026, 11, 17, 9, 0)},
		{"cron: 0,30 12 1,15 * *", "", at(u, 2026, 11, 16, 0, 0), at(u, 2026, 12, 1, 12, 0)},
		{"cron: 0,30 12 1,15 * *", "", at(u, 2026, 12, 1, 12, 0), at(u, 2026, 12, 1, 12, 30)},
		{"cron: 0 9 1 * *", "", at(u, 2026, 11, 16, 0, 0), at(u, 2026, 12, 1, 9, 0)},
		{"cron: 0 0 1 1 *", "", at(u, 2026, 11, 16, 0, 0), at(u, 2027, 1, 1, 0, 0)},
		// Both day fields restricted: the 13th or a Friday.
		{"cron: 0 0 13 * 5", "", at(u, 2026, 11, 16, 0, 0), at(u, 2026, 11, 20, 0, 0)},
		{"cron: 0 0 13 * 5", "", at(u, 2026, 12, 12, 0, 0), at(u, 2026, 12, 13, 0, 0)},
		// 7 is Sunday too.
		{"cron: 0 6 * * 7", "", at(u, 2026, 11, 16, 0, 0), at(u, 2026, 11, 22, 6, 0)},
		{"cron 30 6 * * 0", "", at(brt, 2026, 11, 22, 6, 30), at(brt, 2026, 11, 29, 6, 30)},
	}
	for _, tt := range tests {
		s, err := Parse(tt.every, tt.between)
		if err != nil {
			t.Errorf("Parse(%q, %q): %v", tt.every, tt.between, err)
			continue
		}
		got := s.Next(tt.after)
		if !got.Equal(tt.want) || got.Location() != tt.after.Location() {
			t.Errorf("%q %q: Next(%s) = %s, want %s",
				tt.every, tt.between, tt.after.Format("Mon 2006-01-02 15:04 MST"),
				got.Format("Mon 2006-01-02 15:04 MST"), tt.want.Format("Mon 2006-01-02 15:04 MST"))
		}
	}
}

func TestNextSkipsSeconds(t *testing.T) {
	s, _ := Parse("30m", "")
	after := time.Date(2026, 11, 16, 10, 29, 59, 500, time.UTC)
	if got, want := s.Next(after), at(time.UTC, 2026, 11, 16, 10, 30); !got.Equal(want) {
		t.Errorf("Next = %s, want %s", got, want)
	}
	c, _ := Parse("cron: * * * * *", "")
	if got, want := c.Next(after), at(time.UTC, 2026, 11, 16, 10, 30); !got.Equal(want) {
		t.Errorf("cron Next = %s, want %s", got, want)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		every, between, want string
	}{
		{"", "", "empty"},
		{"3m", "", "too short"},
		{"25h", "", "longer than a day"},
		{"90s", "", "too short"},
		{"5m30s", "", "whole minutes"},
		{"daily", "", "can't read every"},
		{"daily 25:00", "", "can't read time"},
		{"daily 9", "", "can't read time"},
		{"funday 10:00", "", "unknown day"},
		{"mon-xyz 10:00", "", "unknown day"},
		{"cron: 0 8 * *", "", "5 fields"},
		{"cron: 60 * * * *", "", "minute"},
		{"cron: 0 8 * 13 *", "", "month"},
		{"cron: */0 * * * *", "", "bad step"},
		{"cron: 0 0 30 2 *", "", "never runs"},
		{"daily 09:00", "08:00-20:00", "between only works with interval"},
		{"cron: 0 8 * * *", "08:00-20:00", "between only works with interval"},
		{"30m", "08:00", "between"},
		{"30m", "08:00-08:00", "same time"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.every, tt.between)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Parse(%q, %q) error = %v, want it to mention %q", tt.every, tt.between, err, tt.want)
		}
	}
}

func TestString(t *testing.T) {
	tests := []struct{ every, between, want string }{
		{"1h", "", "every hour"},
		{"every 2h", "", "every 2 hours"},
		{"30m", "08:00-20:00", "every 30 min · 08:00–20:00"},
		{"90m", "", "every 90 min"},
		{"weekdays 08:00", "", "weekdays at 08:00"},
		{"mon-fri 8:00", "", "weekdays at 08:00"},
		{"daily 09:30", "", "every day at 09:30"},
		{"every day 09:30", "", "every day at 09:30"},
		{"wed,mon 10:00", "", "mon, wed at 10:00"},
		{"sun,sat 09:00", "", "sat, sun at 09:00"},
		{"cron:  0 8 * * 1-5", "", "cron 0 8 * * 1-5"},
	}
	for _, tt := range tests {
		s, err := Parse(tt.every, tt.between)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.every, err)
			continue
		}
		if got := s.String(); got != tt.want {
			t.Errorf("%q %q: String() = %q, want %q", tt.every, tt.between, got, tt.want)
		}
	}
}

func TestInQuiet(t *testing.T) {
	u := time.UTC
	tests := []struct {
		quiet string
		t     time.Time
		want  bool
	}{
		{"", at(u, 2026, 11, 16, 3, 0), false},
		{"22:00-07:00", at(u, 2026, 11, 16, 21, 59), false},
		{"22:00-07:00", at(u, 2026, 11, 16, 22, 0), true},
		{"22:00-07:00", at(u, 2026, 11, 16, 23, 30), true},
		{"22:00-07:00", at(brt, 2026, 11, 17, 3, 0), true},
		{"22:00-07:00", at(u, 2026, 11, 17, 6, 59), true},
		{"22:00-07:00", at(u, 2026, 11, 17, 7, 0), false}, // the end is not quiet
		{"12:00-13:00", at(u, 2026, 11, 16, 12, 30), true},
		{"12:00-13:00", at(u, 2026, 11, 16, 13, 0), false},
		{"nonsense", at(u, 2026, 11, 16, 12, 30), false},
	}
	for _, tt := range tests {
		if got := InQuiet(tt.quiet, tt.t); got != tt.want {
			t.Errorf("InQuiet(%q, %s) = %v, want %v", tt.quiet, tt.t.Format("15:04"), got, tt.want)
		}
	}
}

func TestNextOutsideQuiet(t *testing.T) {
	u := time.UTC
	tests := []struct {
		every, quiet string
		after, want  time.Time
	}{
		{"30m", "22:00-07:00", at(u, 2026, 11, 16, 21, 15), at(u, 2026, 11, 16, 21, 30)},
		{"30m", "22:00-07:00", at(u, 2026, 11, 16, 21, 45), at(u, 2026, 11, 17, 7, 0)},
		{"1h", "00:00-06:00", at(brt, 2026, 11, 16, 23, 30), at(brt, 2026, 11, 17, 6, 0)},
		{"5m", "22:00-07:00", at(u, 2026, 11, 17, 2, 0), at(u, 2026, 11, 17, 7, 0)},
		{"weekdays 08:00", "", at(u, 2026, 11, 20, 9, 0), at(u, 2026, 11, 23, 8, 0)},
		// Every run falls in quiet hours: no time at all.
		{"daily 23:00", "22:00-07:00", at(u, 2026, 11, 16, 0, 0), time.Time{}},
	}
	for _, tt := range tests {
		s, err := Parse(tt.every, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := NextOutsideQuiet(s, tt.quiet, tt.after); !got.Equal(tt.want) {
			t.Errorf("%q quiet %q after %s: got %s, want %s", tt.every, tt.quiet, tt.after, got, tt.want)
		}
	}
}
