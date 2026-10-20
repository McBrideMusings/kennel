package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	literalDate = regexp.MustCompile(`^\d+-\d+-\d+$`)
	relative    = regexp.MustCompile(`^(\d+) ?(d|w|m)(ay|eek|onth)?s?$`)
	weekdays    = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}
)

const whenHelp = "today eod | tomorrow | 2d 3 days | 1w 2 weeks | 1m | wed monday | next fri | eow | weekend | eom | 2026-08-15"

// coming returns the next date falling on wd, counting from now, or now
// itself when it already is wd.
func coming(now time.Time, wd time.Weekday) time.Time {
	return now.AddDate(0, 0, (int(wd)-int(now.Weekday())+7)%7)
}

// parseWhen turns an expiry phrase into the last day a route applies.
func parseWhen(phrase string, now time.Time) (string, error) {
	w := strings.Join(strings.Fields(strings.ToLower(phrase)), " ")
	day := func(t time.Time) (string, error) { return t.Format("2006-01-02"), nil }
	if w == "" {
		return "", fmt.Errorf("empty date")
	}
	if literalDate.MatchString(w) {
		t, err := time.Parse("2006-1-2", w)
		if err != nil {
			return "", fmt.Errorf("not a real date: %s", w)
		}
		return day(t)
	}
	if m := relative.FindStringSubmatch(w); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "d":
			return day(now.AddDate(0, 0, n))
		case "w":
			return day(now.AddDate(0, 0, 7*n))
		default:
			return day(now.AddDate(0, n, 0))
		}
	}
	switch w {
	case "today", "eod", "tonight":
		return day(now)
	case "tmw", "tmr", "tmrw", "tom":
		return day(now.AddDate(0, 0, 1))
	case "eow":
		return day(coming(now, time.Friday))
	case "weekend":
		return day(coming(now, time.Sunday))
	case "eom":
		return day(time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()))
	}
	if editDistance(w, "tomorrow") <= 2 {
		return day(now.AddDate(0, 0, 1))
	}

	// `next wed` is the one after the coming Wednesday; `this wed` the coming one.
	weeks := 0
	switch {
	case strings.HasPrefix(w, "next "):
		weeks, w = 1, strings.TrimPrefix(w, "next ")
	case strings.HasPrefix(w, "this "):
		w = strings.TrimPrefix(w, "this ")
	case strings.HasPrefix(w, "on "):
		w = strings.TrimPrefix(w, "on ")
	}

	// Tightest match first: abbreviation, full name, prefix, then typos.
	tests := []func(name string) bool{
		func(name string) bool { return w == name[:3] },
		func(name string) bool { return w == name },
		func(name string) bool { return len(w) >= 3 && strings.HasPrefix(name, w) },
		func(name string) bool { return editDistance(w, name) <= 1 },
		func(name string) bool { return editDistance(w, name) <= 2 },
	}
	for _, test := range tests {
		var hits []time.Weekday
		for _, wd := range weekdays {
			if test(strings.ToLower(wd.String())) {
				hits = append(hits, wd)
			}
		}
		if len(hits) == 1 {
			return day(coming(now, hits[0]).AddDate(0, 0, 7*weeks))
		}
		if len(hits) > 1 {
			names := make([]string, len(hits))
			for i, h := range hits {
				names[i] = strings.ToLower(h.String())
			}
			return "", fmt.Errorf("ambiguous day %q — could be %s", w, strings.Join(names, ", "))
		}
	}
	return "", fmt.Errorf("don't understand %q (try: tomorrow, 3d, 2w, wed, next fri, eow, eom, 2026-08-15)", w)
}

// editDistance counts the insertions, deletions, substitutions and adjacent
// swaps that turn a into b.
func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}
