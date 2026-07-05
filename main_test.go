package main

import (
	"testing"
	"time"
)

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestStartOfPeriod(t *testing.T) {
	cases := []struct {
		in, period, want string
	}{
		// Monday-based weeks (ISO 8601).
		{"2026-07-05", "week", "2026-06-29"}, // Sunday -> back to Monday
		{"2026-06-29", "week", "2026-06-29"}, // Monday -> itself
		{"2026-07-01", "week", "2026-06-29"}, // Wednesday
		// Week straddling the year boundary: 2026-01-01 is a Thursday.
		{"2026-01-01", "week", "2025-12-29"},
		{"2026-07-05", "month", "2026-07-01"},
		{"2026-07-05", "year", "2026-01-01"},
		{"2026-07-05", "day", "2026-07-05"},
	}
	for _, c := range cases {
		got := startOfPeriod(date(c.in), c.period).Format("2006-01-02")
		if got != c.want {
			t.Errorf("startOfPeriod(%s, %s) = %s, want %s", c.in, c.period, got, c.want)
		}
	}
}

func TestPrevPeriodStart(t *testing.T) {
	cases := []struct {
		in, period, want string
	}{
		{"2026-06-29", "week", "2026-06-22"},
		{"2026-01-01", "month", "2025-12-01"}, // month rolls across year
		{"2026-03-01", "month", "2026-02-01"},
		{"2026-01-01", "year", "2025-01-01"},
	}
	for _, c := range cases {
		got := prevPeriodStart(date(c.in), c.period).Format("2006-01-02")
		if got != c.want {
			t.Errorf("prevPeriodStart(%s, %s) = %s, want %s", c.in, c.period, got, c.want)
		}
	}
}

func TestPeriodKey(t *testing.T) {
	// The Monday of the week containing 2026-01-01 (Thursday) is 2025-12-29,
	// but ISO numbering puts it in week 1 of 2026.
	if got := periodKey(date("2025-12-29"), "week"); got != "2026-W01" {
		t.Errorf("periodKey week = %s, want 2026-W01", got)
	}
	if got := periodKey(date("2026-07-05"), "week"); got != "2026-W27" {
		t.Errorf("periodKey week = %s, want 2026-W27", got)
	}
	if got := periodKey(date("2026-07-05"), "month"); got != "2026-07" {
		t.Errorf("periodKey month = %s, want 2026-07", got)
	}
	if got := periodKey(date("2026-07-05"), "year"); got != "2026" {
		t.Errorf("periodKey year = %s, want 2026", got)
	}
}

func TestPeriodStreak(t *testing.T) {
	now := date("2026-07-05") // a Sunday; its week is 2026-W27, month 2026-07
	target := 3

	// Helper to build a sums map from period keys.
	sums := func(pairs map[string]int) map[string]int { return pairs }

	// Current week met plus the two prior weeks met -> streak 3.
	weekly := sums(map[string]int{
		"2026-W27": 3, // current
		"2026-W26": 4, // over-achieved still counts
		"2026-W25": 3,
		// 2026-W24 missing -> breaks
	})
	if got := periodStreak(weekly, now, "week", target); got != 3 {
		t.Errorf("weekly streak = %d, want 3", got)
	}

	// Grace: current week not yet met, but prior two met -> streak 2 (run survives).
	grace := sums(map[string]int{
		"2026-W27": 1, // current below target
		"2026-W26": 3,
		"2026-W25": 5,
	})
	if got := periodStreak(grace, now, "week", target); got != 2 {
		t.Errorf("grace streak = %d, want 2", got)
	}

	// Current met but immediately-prior missed -> streak 1 (broken run).
	broken := sums(map[string]int{
		"2026-W27": 3,
		// 2026-W26 missing
		"2026-W25": 3,
	})
	if got := periodStreak(broken, now, "week", target); got != 1 {
		t.Errorf("broken streak = %d, want 1", got)
	}

	// Empty history -> 0.
	if got := periodStreak(map[string]int{}, now, "week", target); got != 0 {
		t.Errorf("empty streak = %d, want 0", got)
	}

	// Monthly: current + prior month met -> 2.
	monthly := sums(map[string]int{
		"2026-07": 3,
		"2026-06": 3,
		// 2026-05 missing
	})
	if got := periodStreak(monthly, now, "month", target); got != 2 {
		t.Errorf("monthly streak = %d, want 2", got)
	}
}
