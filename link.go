package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type linkState int

const (
	linkOK        linkState = iota
	linkMissing             // nothing at the path yet
	linkElsewhere           // a symlink to some other target
	linkBlocked             // a real file or directory sits where the link goes
	baseMissing             // the base home has no such item to link to
	linkOrphan              // a symlink into a folder share's source whose name is not in its items
)

const shareUsage = `usage: kennel share plan|apply|undo|prune [share [item]]
       kennel share add|rm <share> <item>...
       kennel share add|rm <share> --link <path>...`

// pair is one symlink that should exist: path links to target. label names
// whose link it is, for output. unreadable marks the stand-in pair of an
// items = "*" share whose source cannot be listed: path is <into>/*.
type pair struct {
	label        string
	path, target string
	unreadable   bool
}

type linkCheck struct {
	pair
	state  linkState
	actual string // where a linkElsewhere symlink points
}

func (c linkCheck) describe() string {
	switch c.state {
	case linkOK:
		return fmt.Sprintf("ok        %s → %s", tilde(c.path), tilde(c.target))
	case linkMissing:
		return fmt.Sprintf("missing   %s (should link to %s)", tilde(c.path), tilde(c.target))
	case linkElsewhere:
		return fmt.Sprintf("wrong     %s → %s (should link to %s)", tilde(c.path), c.actual, tilde(c.target))
	case linkBlocked:
		return fmt.Sprintf("blocked   %s is a real file or directory, not a link to %s", tilde(c.path), tilde(c.target))
	case linkOrphan:
		return fmt.Sprintf("orphan    %s → %s (not in the share's items; `kennel share prune` removes it)", tilde(c.path), tilde(c.target))
	case baseMissing:
		if c.unreadable {
			return fmt.Sprintf("no source %s is not a readable folder, so %s links nothing", tilde(c.target), tilde(c.path))
		}
		fallthrough
	default:
		return fmt.Sprintf("no source %s does not exist, so %s has nothing to link to", tilde(c.target), tilde(c.path))
	}
}

// profilePairs lists every shared item of every profile home (or of one
// profile when only is set) with the base item it should link to.
func profilePairs(cfg *Config, only string) []pair {
	var out []pair
	for _, prof := range cfg.profileNames() {
		if only != "" && prof != only {
			continue
		}
		for _, tool := range cfg.toolNames() {
			h, ok := cfg.Profiles[prof][tool]
			if !ok {
				continue
			}
			t := cfg.Tools[tool]
			for _, item := range t.Shared {
				out = append(out, pair{label: prof + "/" + tool, path: filepath.Join(h, item), target: filepath.Join(t.Base, item)})
			}
		}
	}
	return out
}

// sharePairs lists every link the [shares] table asks for. loadConfig has
// already rejected a path claimed twice, so each path appears once.
func sharePairs(cfg *Config) []pair {
	var out []pair
	for _, name := range cfg.shareNames() {
		s := cfg.Shares[name]
		label := "share " + name
		for _, l := range s.Links {
			out = append(out, pair{label: label, path: l, target: s.Source})
		}
		items, err := s.entries()
		if err != nil {
			out = append(out, pair{label: label, path: filepath.Join(s.Into, "*"), target: s.Source, unreadable: true})
		}
		for _, item := range items {
			out = append(out, pair{label: label, path: filepath.Join(s.Into, item), target: filepath.Join(s.Source, item)})
		}
	}
	return out
}

// orphanChecks finds the links sharePairs cannot see: a symlink inside a
// folder share's into that points inside its source, under a name no share
// lists. Each comes back as linkOrphan with target set to where it points.
// Real files and links pointing anywhere else are not orphans. With
// items = "*" an excluded entry's link, or one whose entry left source, is an
// orphan; a "*" share whose source cannot be listed is skipped, so a source
// that is briefly gone does not turn every link into one prune removes.
func orphanChecks(cfg *Config) []linkCheck {
	listed := map[string]bool{}
	for _, p := range sharePairs(cfg) {
		listed[p.path] = true
	}
	var out []linkCheck
	for _, name := range cfg.shareNames() {
		s := cfg.Shares[name]
		if s.Into == "" {
			continue
		}
		if _, err := s.entries(); err != nil {
			continue
		}
		entries, err := os.ReadDir(s.Into)
		if err != nil {
			continue
		}
		src := filepath.Clean(s.Source) + string(filepath.Separator)
		for _, e := range entries {
			path := filepath.Join(s.Into, e.Name())
			if listed[path] || e.Type()&os.ModeSymlink == 0 {
				continue
			}
			actual, err := os.Readlink(path)
			if err != nil {
				continue
			}
			if !filepath.IsAbs(actual) {
				actual = filepath.Join(s.Into, actual)
			}
			actual = filepath.Clean(actual)
			if !strings.HasPrefix(actual, src) {
				continue
			}
			listed[path] = true
			out = append(out, linkCheck{pair: pair{label: "share " + name, path: path, target: actual}, state: linkOrphan, actual: actual})
		}
	}
	return out
}

// removeLinks deletes each check's symlink and logs it as verb.
func removeLinks(checks []linkCheck, verb string) error {
	for _, c := range checks {
		if err := os.Remove(c.path); err != nil {
			return fmt.Errorf("remove %s: %v", c.path, err)
		}
		logf("share: %s %s -> %s", verb, c.path, c.target)
		fmt.Printf("%-9s %s\n", verb, tilde(c.path))
	}
	return nil
}

// checkPairs inspects every pair without changing anything.
func checkPairs(pairs []pair) []linkCheck {
	var out []linkCheck
	for _, p := range pairs {
		c := linkCheck{pair: p}
		switch fi, err := os.Lstat(c.path); {
		case p.unreadable:
			c.state = baseMissing
		case errors.Is(err, os.ErrNotExist):
			c.state = linkMissing
		case err != nil:
			c.state = linkBlocked
		case fi.Mode()&os.ModeSymlink == 0:
			c.state = linkBlocked
		default:
			c.actual, _ = os.Readlink(c.path)
			if c.actual == c.target {
				c.state = linkOK
			} else {
				c.state = linkElsewhere
			}
		}
		if c.state == linkMissing {
			if _, err := os.Lstat(c.target); err != nil {
				c.state = baseMissing
			}
		}
		out = append(out, c)
	}
	return out
}

// createLinks makes each missing symlink, and its parent directory when that
// is missing too. It never moves or deletes anything: any other state is
// printed and counted in bad. kind prefixes the log line.
func createLinks(checks []linkCheck, kind string) (bad int, err error) {
	for _, c := range checks {
		if c.state == linkMissing {
			if dir := filepath.Dir(c.path); !isDir(dir) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return bad, fmt.Errorf("create %s: %v", dir, err)
				}
				logf("%s: created directory %s", kind, dir)
				fmt.Printf("created   %s\n", tilde(dir))
			}
			if err := os.Symlink(c.target, c.path); err != nil {
				return bad, fmt.Errorf("link %s: %v", c.path, err)
			}
			logf("%s: %s -> %s", kind, c.path, c.target)
			fmt.Printf("linked    %s → %s\n", tilde(c.path), tilde(c.target))
			continue
		}
		if c.state != linkOK {
			bad++
			fmt.Println(c.describe())
		}
	}
	return bad, nil
}

// cmdLink creates each profile home and the symlinks its shared items need.
// It never moves or deletes anything: a blocked or wrong item is reported and
// left for the user, and the exit status says so.
func cmdLink(cfg *Config, only string) int {
	if only != "" {
		if _, ok := cfg.Profiles[only]; !ok {
			return failf("no [profiles.%s] in %s", only, cfg.Path)
		}
	}
	for _, prof := range cfg.profileNames() {
		if only != "" && prof != only {
			continue
		}
		for _, tool := range cfg.toolNames() {
			h, ok := cfg.Profiles[prof][tool]
			if !ok {
				continue
			}
			if _, err := os.Stat(h); errors.Is(err, os.ErrNotExist) {
				if err := os.MkdirAll(h, 0o755); err != nil {
					return failf("create %s: %v", h, err)
				}
				logf("link: created home %s for %s/%s", h, prof, tool)
				fmt.Printf("created   %s\n", tilde(h))
			}
		}
	}
	bad, err := createLinks(checkPairs(profilePairs(cfg, only)), "link")
	if err != nil {
		return failf("%v", err)
	}
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "kennel: %d item(s) need a hand fix; nothing was moved or deleted\n", bad)
		return 1
	}
	return 0
}

// shareScope is the part of [shares] one share command acts on: every share
// (name empty), one share, or one link path of one share (path set).
type shareScope struct{ name, path string }

func (sc shareScope) filter(checks []linkCheck) []linkCheck {
	var out []linkCheck
	for _, c := range checks {
		if sc.name == "" || c.label == "share "+sc.name && (sc.path == "" || c.path == sc.path) {
			out = append(out, c)
		}
	}
	return out
}

// scopeShares resolves the optional [share [item]] arguments against every
// check and orphan. The item is a folder share's item name (an orphan's name
// included) or a file share's link path.
func scopeShares(cfg *Config, args []string, all []linkCheck) (shareScope, error) {
	if len(args) == 0 {
		return shareScope{}, nil
	}
	s, ok := cfg.Shares[args[0]]
	if !ok {
		return shareScope{}, fmt.Errorf("no [shares.%s] in %s", args[0], cfg.Path)
	}
	if len(args) == 1 {
		return shareScope{name: s.Name}, nil
	}
	sc := shareScope{name: s.Name}
	if s.Into != "" {
		sc.path = filepath.Join(s.Into, args[1])
	} else if abs, err := filepath.Abs(expand(args[1])); err == nil {
		sc.path = abs
	}
	if len(sc.filter(all)) == 0 {
		return shareScope{}, fmt.Errorf("[shares.%s] has no item or link %s", s.Name, args[1])
	}
	return sc, nil
}

// cmdShare plans, applies, undoes or prunes the cross-tool links in
// [shares], or only those of one share or one item when args names it. Like
// link, it never moves or deletes a real file. undo removes every link in
// scope plus every orphan in scope; prune removes orphans only.
func cmdShare(cfg *Config, action string, args []string) int {
	if action != "plan" && action != "apply" && action != "undo" && action != "prune" || len(args) > 2 {
		return failf(shareUsage)
	}
	if len(cfg.Shares) == 0 && len(args) == 0 {
		fmt.Println("nothing to do: no [shares] in the config")
		return 0
	}
	checks := checkPairs(sharePairs(cfg))
	orphans := orphanChecks(cfg)
	scope, err := scopeShares(cfg, args, append(checks, orphans...))
	if err != nil {
		return failf("%v", err)
	}
	checks, orphans = scope.filter(checks), scope.filter(orphans)
	switch action {
	case "plan":
		for _, c := range append(checks, orphans...) {
			fmt.Println(c.describe())
		}
	case "apply":
		bad, err := createLinks(checks, "share")
		if err != nil {
			return failf("%v", err)
		}
		if bad > 0 {
			fmt.Fprintf(os.Stderr, "kennel: %d share link(s) need a hand fix; nothing was moved or deleted\n", bad)
			return 1
		}
	case "undo":
		var ours []linkCheck
		for _, c := range checks {
			if c.state == linkOK {
				ours = append(ours, c)
			}
		}
		if err := removeLinks(append(ours, orphans...), "removed"); err != nil {
			return failf("%v", err)
		}
	case "prune":
		if len(orphans) == 0 {
			fmt.Println("no orphaned links")
		}
		if err := removeLinks(orphans, "pruned"); err != nil {
			return failf("%v", err)
		}
	}
	return 0
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
