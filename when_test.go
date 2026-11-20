package main

import (
	"testing"
	"time"
)

// An expiry read wrong keeps a route on the wrong account or drops it early,
// so this pins every unit, both readings of m, a last day, and each refusal.
func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"1d":         now.AddDate(0, 0, 1),
		"3h30m":      now.Add(3*time.Hour + 30*time.Minute),
		"5m":         now.Add(5 * time.Minute), // a lone m is the smaller unit
		"90s":        now.Add(90 * time.Second),
		"2m30s":      now.Add(2*time.Minute + 30*time.Second),
		"1w2d":       now.AddDate(0, 0, 9),
		"1y2m":       time.Date(2028, 1, 20, 12, 0, 0, 0, time.UTC),
		"2m1d":       time.Date(2027, 1, 21, 12, 0, 0, 0, time.UTC),
		"1m1m":       time.Date(2026, 12, 20, 12, 1, 0, 0, time.UTC),
		"1y2m3w4d5h": time.Date(2028, 2, 14, 17, 0, 0, 0, time.UTC),
		"1D 2H":      now.Add(26 * time.Hour),
		"2026-11-25": time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC),
		"2026-11-20": time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC),
	} {
		got, err := parseWhen(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseWhen(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "eow", "tomorrow", "next fri", "1h1d", "1d1d", "0d", "3", "d", "1x", "2 weeks", "2026-02-30", "2026-11-19"} {
		if got, err := parseWhen(in, now); err == nil {
			t.Errorf("parseWhen(%q) = %v, want an error", in, got)
		}
	}
}
