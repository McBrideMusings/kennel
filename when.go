package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	literalDate   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	durationToken = regexp.MustCompile(`(\d+)([a-z])`)
	durationShape = regexp.MustCompile(`^(\d+[a-z])+$`)
)

const whenHelp = "a duration, units y m w d h m s largest first (1d, 3h30m, 1w2d, 1y6m; a lone m is minutes), or a last day, 2026-08-15"

// durationOrder ranks the units largest first; "mo" and "min" are the two
// readings of m.
var durationOrder = map[string]int{"y": 0, "mo": 1, "w": 2, "d": 3, "h": 4, "min": 5, "s": 6}

// parseWhen turns a duration or a YYYY-MM-DD last day into the moment a
// route stops applying.
func parseWhen(phrase string, now time.Time) (time.Time, error) {
	w := strings.Join(strings.Fields(strings.ToLower(phrase)), "")
	if w == "" {
		return time.Time{}, fmt.Errorf("empty duration")
	}
	if literalDate.MatchString(w) {
		return parseLastDay(w, now)
	}
	return parseDuration(w, now)
}

// parseLastDay reads YYYY-MM-DD as the end of that day in local time.
func parseLastDay(w string, now time.Time) (time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", w, now.Location())
	if err != nil {
		return time.Time{}, fmt.Errorf("not a real date: %s", w)
	}
	end := d.AddDate(0, 0, 1)
	if !end.After(now) {
		return time.Time{}, fmt.Errorf("%s has already ended", w)
	}
	return end, nil
}

// parseDuration reads NyNmNwNdNhNmNs with any unit left out. An m is months
// when a w, d, h or another m follows it, or when it ends the duration right
// after a y; otherwise it is minutes, so a lone 5m is five minutes.
func parseDuration(w string, now time.Time) (time.Time, error) {
	bad := fmt.Errorf("don't understand %q: want %s", w, whenHelp)
	if !durationShape.MatchString(w) {
		return time.Time{}, bad
	}
	toks := durationToken.FindAllStringSubmatch(w, -1)
	units := make([]string, len(toks))
	for i, t := range toks {
		units[i] = t[2]
	}
	var y, mo, d int
	var clock time.Duration
	last := -1
	for i, t := range toks {
		n, err := strconv.Atoi(t[1])
		if err != nil || n > 1_000_000 {
			return time.Time{}, bad
		}
		u := units[i]
		if u == "m" {
			next, prev := "", ""
			if i+1 < len(units) {
				next = units[i+1]
			}
			if i > 0 {
				prev = units[i-1]
			}
			if strings.Contains("wdhm", next) && next != "" || next == "" && prev == "y" {
				u = "mo"
			} else {
				u = "min"
			}
		}
		rank, ok := durationOrder[u]
		if !ok {
			return time.Time{}, fmt.Errorf("unknown unit %q in %q: want %s", t[2], w, whenHelp)
		}
		if rank <= last {
			return time.Time{}, fmt.Errorf("%q: give each unit once, largest first (y m w d h m s)", w)
		}
		last = rank
		switch u {
		case "y":
			y = n
		case "mo":
			mo = n
		case "w":
			d += 7 * n
		case "d":
			d += n
		case "h":
			clock += time.Duration(n) * time.Hour
		case "min":
			clock += time.Duration(n) * time.Minute
		case "s":
			clock += time.Duration(n) * time.Second
		}
	}
	end := now.AddDate(y, mo, d).Add(clock)
	if end.UTC().Year() > 9999 {
		return time.Time{}, fmt.Errorf("%q ends after the year 9999", w)
	}
	if !end.After(now) {
		return time.Time{}, fmt.Errorf("%q is no time at all", w)
	}
	return end, nil
}
