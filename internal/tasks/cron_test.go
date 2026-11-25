package tasks

import (
	"testing"
	"time"
)

func TestCronMatches(t *testing.T) {
	cases := []struct {
		expr    string
		timeStr string
		matches bool
	}{
		{"0 4 * * 1", "2026-10-19 04:00:00", true}, // Monday 4:00 AM (2026-10-19 is Monday)
		{"0 4 * * 1", "2026-10-19 04:01:00", false},
		{"0 4 * * 1", "2026-10-20 04:00:00", false}, // Tuesday
		{"*/15 * * * *", "2026-10-16 15:30:00", true},
		{"*/15 * * * *", "2026-10-16 15:31:00", false},
		{"0 2,4,6 * * *", "2026-10-16 04:00:00", true},
		{"0 2,4,6 * * *", "2026-10-16 05:00:00", false},
		{"0 9-17 * * 1-5", "2026-10-16 10:00:00", true},  // Friday 10:00 AM (2026-10-16 is Friday)
		{"0 9-17 * * 1-5", "2026-10-18 10:00:00", false}, // Sunday
	}

	for _, c := range cases {
		tm, err := time.ParseInLocation("2006-01-02 15:04:05", c.timeStr, time.Local)
		if err != nil {
			t.Fatalf("time parse error: %v", err)
		}
		sched, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("unexpected parse error for %q: %v", c.expr, err)
		}
		if got := sched.Matches(tm); got != c.matches {
			t.Errorf("expr %q at %s: expected %v, got %v", c.expr, c.timeStr, c.matches, got)
		}
	}
}

func TestCronInvalid(t *testing.T) {
	invalids := []string{
		"",
		"* * * *",     // 4 fields
		"* * * * * *", // 6 fields
		"60 * * * *",  // minute out of bounds
		"* 25 * * *",  // hour out of bounds
		"* * 32 * *",  // day of month out of bounds
		"* * * 13 *",  // month out of bounds
		"* * * * 8",   // day of week out of bounds
		"abc * * * *", // non-numeric
		"*/0 * * * *", // step 0
	}
	for _, inv := range invalids {
		if _, err := ParseCron(inv); err == nil {
			t.Errorf("expected error for invalid cron %q, got nil", inv)
		}
	}
}
