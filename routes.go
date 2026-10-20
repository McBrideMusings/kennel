package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const routesHeader = `# agenthome routes — <path> <profile> [YYYY-MM-DD]
#   path    — absolute or ~-prefixed directory; the longest match wins
#   profile — a [profiles.<name>] in config.toml, or the row is ignored
#   date    — last day the row applies; expired rows are deleted on the next cd
`

// A line of the routes file. Comments and blanks keep their text so a rewrite
// leaves them where they were.
type routeLine struct {
	raw     string
	path    string // as written, ~-prefixed or absolute
	profile string
	expires string // YYYY-MM-DD or ""
}

func (l routeLine) isRoute() bool { return l.path != "" }

func (l routeLine) format() string {
	if !l.isRoute() {
		return l.raw
	}
	s := l.path + " " + l.profile
	if l.expires != "" {
		s += " " + l.expires
	}
	return s
}

func today() string { return time.Now().Format("2006-01-02") }

func (l routeLine) expired(day string) bool { return l.expires != "" && l.expires < day }

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
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			lines = append(lines, routeLine{raw: raw})
			continue
		}
		f := strings.Fields(t)
		if len(f) < 2 || len(f) > 3 {
			return nil, fmt.Errorf("%s:%d: want `<path> <profile> [YYYY-MM-DD]`, got %q", routesPath(), i+1, raw)
		}
		l := routeLine{raw: raw, path: f[0], profile: f[1]}
		if len(f) == 3 {
			if _, err := time.Parse("2006-01-02", f[2]); err != nil {
				return nil, fmt.Errorf("%s:%d: %q is not a YYYY-MM-DD date", routesPath(), i+1, f[2])
			}
			l.expires = f[2]
		}
		lines = append(lines, l)
	}
	return lines, nil
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
	day := today()
	var kept, dropped []routeLine
	for _, l := range lines {
		if l.isRoute() && l.expired(day) {
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
	common := strings.TrimSpace(string(out))
	// A submodule's common dir is <super>/.git/modules/<name>; cutting at the
	// first /.git routes it with its superproject.
	if i := strings.Index(common+"/", "/.git/"); i > 0 {
		return common[:i]
	}
	return dir
}

// resolve returns the profile the longest live route gives dir, or "" when no
// route matches.
func resolve(cfg *Config, lines []routeLine, dir string) string {
	// Git reports repo paths with symlinks resolved, so both sides of the
	// comparison are resolved too.
	md := canonical(matchDir(dir))
	day := today()
	best, bestLen := "", -1
	for _, l := range lines {
		if !l.isRoute() || l.expired(day) {
			continue
		}
		if _, ok := cfg.Profiles[l.profile]; !ok {
			continue
		}
		p := canonical(expand(l.path))
		if (md == p || strings.HasPrefix(md, p+"/") || p == "/") && len(p) > bestLen {
			best, bestLen = l.profile, len(p)
		}
	}
	return best
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
	if strings.ContainsAny(abs, " \t") {
		return "", fmt.Errorf("%s contains whitespace; the routes file is whitespace-separated and cannot store it", abs)
	}
	return tilde(abs), nil
}
