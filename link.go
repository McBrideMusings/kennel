package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
