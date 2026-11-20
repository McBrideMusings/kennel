package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const routesHeader = `# kennel routes — <path> <profile>[:tool,tool] [expiry]
#   path    — absolute or ~-prefixed directory; the longest match wins, tool by tool,
#             and of rows at one path the last
#   profile — a [profiles.<name>] in config.toml, or the row is ignored
#   tools   — the tools the row routes; none named routes every tool
#   expiry  — UTC time the row stops (2026-08-15T18:00:00Z), or a last day (2026-08-15);
#             expired rows are deleted on the next cd
`

// A line of the routes file. Comments and blanks keep their text so a rewrite
// leaves them where they were.
type routeLine struct {
	raw     string
	path    string // as written, ~-prefixed or absolute
	profile string
	tools   []string // the tools the row routes; nil routes every tool
	expires string   // RFC 3339 UTC, a hand-written YYYY-MM-DD, or ""
	bad     string // why the row could not be read; it is kept as written and ignored
}

func (l routeLine) isRoute() bool { return l.path != "" }

func (l routeLine) format() string {
	if !l.isRoute() {
		return l.raw
	}
	s := l.path + " " + l.profile
	if len(l.tools) > 0 {
		s += ":" + strings.Join(l.tools, ",")
	}
	if l.expires != "" {
		s += " " + l.expires
	}
	return s
}

// routes reports whether the row routes tool.
func (l routeLine) routes(tool string) bool {
	return len(l.tools) == 0 || slices.Contains(l.tools, tool)
}

// effectiveTools is the configured tools the row routes.
func (l routeLine) effectiveTools(cfg *Config) []string {
	if len(l.tools) == 0 {
		return cfg.toolNames()
	}
	return l.tools
}

// expiryTime reads a row's expiry: an RFC 3339 time, or a YYYY-MM-DD last
// day, which ends at local midnight after it.
func expiryTime(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if d, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return d.AddDate(0, 0, 1), true
	}
	return time.Time{}, false
}

func (l routeLine) expired(now time.Time) bool {
	if l.expires == "" {
		return false
	}
	t, _ := expiryTime(l.expires)
	return !now.Before(t)
}

func readRoutes() ([]routeLine, error) {
	data, err := os.ReadFile(routesPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []routeLine
	for i, raw := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		lines = append(lines, parseRouteLine(raw, i+1))
	}
	return lines, nil
}

// parseRouteLine reads a row from its right end — an optional expiry, then the
// profile and its tools — so everything left of them is the path, spaces included. A row
// it cannot read keeps its text and carries the reason in bad.
func parseRouteLine(raw string, n int) routeLine {
	t := strings.TrimSpace(raw)
	if t == "" || strings.HasPrefix(t, "#") {
		return routeLine{raw: raw}
	}
	l := routeLine{raw: raw}
	rest, last := cutLast(t)
	if _, ok := expiryTime(last); ok {
		l.expires = last
		rest, last = cutLast(rest)
	}
	l.path = rest
	profile, tools, hasTools := strings.Cut(last, ":")
	l.profile = profile
	if hasTools {
		l.tools = strings.Split(tools, ",")
	}
	if l.path == "" || l.profile == "" || hasTools && slices.Contains(l.tools, "") {
		return routeLine{raw: raw, bad: fmt.Sprintf("line %d: want `<path> <profile>[:tool,tool] [expiry]`, got %q", n, raw)}
	}
	return l
}

// cutLast splits s at its last run of whitespace.
func cutLast(s string) (rest, last string) {
	i := strings.LastIndexAny(s, " \t")
	if i < 0 {
		return "", s
	}
	return strings.TrimRight(s[:i], " \t"), s[i+1:]
}

func writeRoutes(lines []routeLine) error {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.format())
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(configDir(), ".routes-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	return os.Rename(tmp.Name(), routesPath())
}

// dropExpired deletes every route past its date and returns what it deleted.
// It rewrites the file only when something went, so the usual cd is a read.
func dropExpired() ([]routeLine, error) {
	lines, err := readRoutes()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var kept, dropped []routeLine
	for _, l := range lines {
		if l.isRoute() && l.expired(now) {
			dropped = append(dropped, l)
			continue
		}
		kept = append(kept, l)
	}
	if len(dropped) == 0 {
		return nil, nil
	}
	if err := writeRoutes(kept); err != nil {
		return nil, err
	}
	for _, l := range dropped {
		logf("route expired and dropped: %s", l.format())
	}
	return dropped, nil
}

// matchDir is the directory routes match against: the main repo's root when
// dir is inside a git repo or linked worktree, so a worktree anywhere on disk
// follows its repo; otherwise dir itself.
func matchDir(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return dir
	}
	return matchFromCommon(strings.TrimSpace(string(out)), dir)
}

// matchFromCommon turns git's absolute common dir for dir into matchDir's
// answer.
func matchFromCommon(common, dir string) string {
	// A submodule's common dir is <super>/.git/modules/<name>; cutting at the
	// first /.git routes it with its superproject.
	if i := strings.Index(common+"/", "/.git/"); i > 0 {
		return common[:i]
	}
	return dir
}

// resolve maps each configured tool to the profile the longest live route
// for that tool gives dir, the last such row in the file when several share a
// path; a tool no route covers is left out.
func resolve(cfg *Config, lines []routeLine, dir string) map[string]string {
	return resolveMatch(cfg, lines, matchDir(dir), cfg.toolNames()...)
}

// resolveMatch is resolve for a directory already turned into matchDir's
// answer, for the tools named.
func resolveMatch(cfg *Config, lines []routeLine, md string, tools ...string) map[string]string {
	// Git reports repo paths with symlinks resolved, so both sides of the
	// comparison are resolved too.
	md = canonical(md)
	now := time.Now()
	got := map[string]string{}
	bestLen := map[string]int{}
	for _, l := range lines {
		if !l.isRoute() || l.expired(now) {
			continue
		}
		if _, ok := cfg.Profiles[l.profile]; !ok {
			continue
		}
		p := canonical(expand(l.path))
		if md != p && !strings.HasPrefix(md, p+"/") && p != "/" {
			continue
		}
		for _, tool := range tools {
			// >= lets a later row at the same path win: addRoute appends.
			if best, seen := bestLen[tool]; l.routes(tool) && (!seen || len(p) >= best) {
				got[tool], bestLen[tool] = l.profile, len(p)
			}
		}
	}
	return got
}

// canonical resolves symlinks in p, or returns p unchanged when it does not
// exist (a route may name a directory not created yet).
func canonical(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// normalizeRoutePath turns a user-typed path into the form the routes file
// stores, so `.`, a relative path and a trailing slash all name one row.
func normalizeRoutePath(p string) (string, error) {
	abs, err := filepath.Abs(expand(p))
	if err != nil {
		return "", err
	}
	return tilde(abs), nil
}

// samePath reports whether two route paths name one directory, whichever
// spelling each was written in.
func samePath(a, b string) bool {
	return a == b || canonical(expand(a)) == canonical(expand(b))
}
