package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// cmdShareEdit adds entries to one share's items (a folder share) or links (a
// file share, with --link) and creates their links, or removes their links
// and then the entries. config.toml is edited in place, line by line, and the
// links go through cmdShare's scoped apply and undo, so add and rm keep its
// rules: a real file or a foreign link is reported and left alone, and rm
// removes only a link that points at the share's source.
func cmdShareEdit(cfg *Config, action string, args []string) int {
	if len(args) < 2 {
		return failf(shareUsage)
	}
	s, ok := cfg.Shares[args[0]]
	if !ok {
		return failf("no [shares.%s] in %s", args[0], cfg.Path)
	}
	key, names := "items", args[1:]
	if names[0] == "--link" {
		key, names = "links", names[1:]
	}
	if len(names) == 0 {
		return failf(shareUsage)
	}
	switch {
	case key == "items" && s.Into == "":
		return failf("[shares.%s] is a file share: name its links with --link <path>", s.Name)
	case key == "links" && s.Into != "":
		return failf("[shares.%s] is a folder share: name items, not --link", s.Name)
	}

	// vals are the entries as written to config.toml: an item name, or a
	// link path made absolute and shown with ~.
	var vals []string
	seen := map[string]bool{}
	for _, n := range names {
		v := n
		if key == "items" {
			if !validName(n) {
				return failf("item %q must be one name with no /", n)
			}
		} else {
			abs, err := filepath.Abs(expand(n))
			if err != nil {
				return failf("%s: %v", n, err)
			}
			v = tilde(abs)
		}
		if !seen[v] {
			seen[v] = true
			vals = append(vals, v)
		}
	}
	listed := func(v string) bool {
		if key == "items" {
			return slices.Contains(s.Items, v)
		}
		return slices.Contains(s.Links, expand(v))
	}

	src, err := os.ReadFile(cfg.Path)
	if err != nil {
		return failf("%v", err)
	}
	edited, orig := src, cfg
	var changes []string
	if s.All {
		// items = "*" links every entry: add and rm edit exclude instead.
		var undo []string
		if edited, changes, undo, err = editExclude(s, action, vals, src); err != nil {
			return failf("%s: %v", cfg.Path, err)
		}
		key = "exclude"
		for _, v := range undo {
			if code := cmdShare(cfg, "undo", []string{s.Name, v}); code != 0 {
				return code
			}
		}
	} else if action == "add" {
		for _, v := range vals {
			if listed(v) {
				fmt.Printf("listed    %s is already in [shares.%s] %s\n", v, s.Name, key)
				continue
			}
			if key == "items" {
				if _, err := os.Lstat(filepath.Join(s.Source, v)); err != nil {
					return failf("%s does not exist, so %s has nothing to link to", tilde(filepath.Join(s.Source, v)), v)
				}
			}
			arr, err := findShareArray(edited, s.Name, key)
			if err != nil {
				return failf("%s: %v", cfg.Path, err)
			}
			edited = arr.appended(v)
			changes = append(changes, "added "+v+" to")
		}
	} else {
		have := len(s.Items) + len(s.Links)
		for _, v := range vals {
			if !listed(v) {
				return failf("[shares.%s] %s has no %s", s.Name, key, v)
			}
		}
		if len(vals) == have {
			return failf("rm would leave [shares.%s] with no %s; run `kennel share undo %s` and delete the table by hand", s.Name, key, s.Name)
		}
		// Every entry is found in the text before any link goes, so a
		// layout the editor cannot read leaves links and config as they were.
		for _, v := range vals {
			arr, err := findShareArray(edited, s.Name, key)
			if err != nil {
				return failf("%s: %v", cfg.Path, err)
			}
			i := slices.IndexFunc(arr.elems, func(e tomlElem) bool {
				return e.value == v || key == "links" && expand(e.value) == expand(v)
			})
			if i < 0 {
				return failf("%s: [shares.%s] %s has no %s; edit it by hand", cfg.Path, s.Name, key, v)
			}
			edited = arr.removed(i)
			changes = append(changes, "removed "+v+" from")
		}
		for _, v := range vals {
			if code := cmdShare(cfg, "undo", []string{s.Name, v}); code != 0 {
				return code
			}
		}
	}
	if len(changes) > 0 {
		if cfg, err = rewriteConfig(cfg, src, edited); err != nil {
			return failf("%v", err)
		}
		for _, c := range changes {
			logf("share: %s [shares.%s] %s in %s", c, s.Name, key, cfg.Path)
			fmt.Printf("%-9s %s [shares.%s] %s\n", "edited", c, s.Name, key)
		}
	}
	if action == "rm" {
		return leftBehind(orig, s.Name, vals)
	}
	code := 0
	for _, v := range vals {
		code |= cmdShare(cfg, "apply", []string{s.Name, v})
	}
	return code
}

// editExclude is share add and rm for an items = "*" share: rm adds each
// entry of source to exclude, add takes it back out. It returns the edited
// text, one change line per edit, and the entries rm newly excluded, whose
// links go next. A share with no exclude line gets one after items.
func editExclude(s *Share, action string, vals []string, src []byte) (edited []byte, changes, excluded []string, err error) {
	edited = src
	have := slices.Clone(s.Exclude)
	for _, v := range vals {
		if strings.HasPrefix(v, ".") {
			return nil, nil, nil, fmt.Errorf("[shares.%s] items = \"*\" never links a dotfile such as %s", s.Name, v)
		}
		switch in := slices.Contains(have, v); {
		case action == "add" && !in:
			fmt.Printf("listed    %s is already linked by [shares.%s] items = \"*\"\n", v, s.Name)
			continue
		case action == "rm" && in:
			fmt.Printf("listed    %s is already in [shares.%s] exclude\n", v, s.Name)
			continue
		}
		if _, err := os.Lstat(filepath.Join(s.Source, v)); err != nil {
			if action == "add" {
				return nil, nil, nil, fmt.Errorf("%s does not exist, so %s has nothing to link to", tilde(filepath.Join(s.Source, v)), v)
			}
			return nil, nil, nil, fmt.Errorf("[shares.%s] links no %s: %s does not exist (`kennel share prune %s` removes a link left behind)", s.Name, v, tilde(filepath.Join(s.Source, v)), s.Name)
		}
		if action == "add" {
			arr, err := findShareArray(edited, s.Name, "exclude")
			if err != nil {
				return nil, nil, nil, err
			}
			i := slices.IndexFunc(arr.elems, func(e tomlElem) bool { return e.value == v })
			if i < 0 {
				return nil, nil, nil, fmt.Errorf("[shares.%s] exclude has no %s; edit it by hand", s.Name, v)
			}
			edited = arr.removed(i)
			have = slices.DeleteFunc(have, func(e string) bool { return e == v })
			changes = append(changes, "removed "+v+" from")
			continue
		}
		if _, _, err := findShareLine(edited, s.Name, "exclude"); err != nil {
			edited, err = insertShareArray(edited, s.Name, "items", "exclude", v)
			if err != nil {
				return nil, nil, nil, err
			}
		} else {
			arr, err := findShareArray(edited, s.Name, "exclude")
			if err != nil {
				return nil, nil, nil, err
			}
			edited = arr.appended(v)
		}
		have = append(have, v)
		excluded = append(excluded, v)
		changes = append(changes, "added "+v+" to")
	}
	return edited, changes, excluded, nil
}

// leftBehind reports what share rm left at each removed entry's path: a real
// file, or a symlink that does not point at the share's source. rm leaves
// them alone, and exits 1 so the user knows a hand look is due.
func leftBehind(cfg *Config, name string, vals []string) int {
	s := cfg.Shares[name] // the old config: it still lists vals
	paths := map[string]bool{}
	for _, v := range vals {
		if s.Into != "" {
			paths[filepath.Join(s.Into, v)] = true
		} else {
			paths[expand(v)] = true
		}
	}
	bad := 0
	for _, c := range checkPairs(sharePairs(cfg)) {
		if c.label == "share "+name && paths[c.path] && (c.state == linkElsewhere || c.state == linkBlocked) {
			bad++
			fmt.Println(c.describe())
		}
	}
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "kennel: left %d path(s) that are not links to [shares.%s]'s source; nothing was moved or deleted\n", bad, name)
		return 1
	}
	return 0
}

// rewriteConfig replaces config.toml with edited through writeAtomic, so a
// crash never leaves a half-written config, and loads it again. If the edit
// leaves a config that does not load, the old bytes go back.
func rewriteConfig(cfg *Config, old, edited []byte) (*Config, error) {
	path := cfg.Path
	if err := writeAtomic(path, edited); err != nil {
		return nil, err
	}
	next, err := loadConfig()
	if err != nil {
		if werr := writeAtomic(path, old); werr != nil {
			return nil, fmt.Errorf("%v; restoring the old config failed too: %v", err, werr)
		}
		return nil, fmt.Errorf("the edit made a config that does not load, so it was put back: %v", err)
	}
	return next, nil
}
