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

const shareUsage = "usage: kennel share plan|apply|undo|prune"

// pair is one symlink that should exist: path links to target. label names
// whose link it is, for output.
type pair struct {
	label        string
	path, target string
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

// sharePairs lists every link the [shares] table asks for.
func sharePairs(cfg *Config) []pair {
	var out []pair
	seen := map[string]bool{}
	add := func(p pair) {
		if !seen[p.path] {
			seen[p.path] = true
			out = append(out, p)
		}
	}
	for _, name := range cfg.shareNames() {
		s := cfg.Shares[name]
		label := "share " + name
		for _, l := range s.Links {
			add(pair{label: label, path: l, target: s.Source})
		}
		for _, item := range s.Items {
			add(pair{label: label, path: filepath.Join(s.Into, item), target: filepath.Join(s.Source, item)})
		}
	}
	return out
}

// orphanChecks finds the links sharePairs cannot see: a symlink inside a
// folder share's into that points inside its source, under a name no share
// lists. Each comes back as linkOrphan with target set to where it points.
// Real files and links pointing anywhere else are not orphans.
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

// cmdShare plans, applies, undoes or prunes the cross-tool links in
// [shares]. Like link, it never moves or deletes a real file. undo removes
// every link the shares list plus every orphan; prune removes orphans only.
func cmdShare(cfg *Config, action string) int {
	if action != "plan" && action != "apply" && action != "undo" && action != "prune" {
		return failf(shareUsage)
	}
	if len(cfg.Shares) == 0 {
		fmt.Println("nothing to do: no [shares] in the config")
		return 0
	}
	checks := checkPairs(sharePairs(cfg))
	orphans := orphanChecks(cfg)
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
