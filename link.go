package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type linkState int

const (
	linkOK        linkState = iota
	linkMissing             // nothing at the path yet
	linkElsewhere           // a symlink to some other target
	linkBlocked             // a real file or directory sits where the link goes
	baseMissing             // the base home has no such item to link to
)

type linkCheck struct {
	profile, tool string
	path, target  string
	state         linkState
	actual        string // where a linkElsewhere symlink points
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
	default:
		return fmt.Sprintf("no base   %s does not exist, so %s has nothing to link to", tilde(c.target), tilde(c.path))
	}
}

// checkLinks inspects every shared item of every profile home without
// changing anything.
func checkLinks(cfg *Config, only string) []linkCheck {
	var out []linkCheck
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
				c := linkCheck{profile: prof, tool: tool, path: filepath.Join(h, item), target: filepath.Join(t.Base, item)}
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
		}
	}
	return out
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
	bad := 0
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
	for _, c := range checkLinks(cfg, only) {
		if c.state == linkMissing {
			if err := os.Symlink(c.target, c.path); err != nil {
				return failf("link %s: %v", c.path, err)
			}
			logf("link: %s -> %s", c.path, c.target)
			fmt.Printf("linked    %s → %s\n", tilde(c.path), tilde(c.target))
			continue
		}
		if c.state != linkOK {
			bad++
			fmt.Println(c.describe())
		}
	}
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "agenthome: %d item(s) need a hand fix; nothing was moved or deleted\n", bad)
		return 1
	}
	return 0
}
