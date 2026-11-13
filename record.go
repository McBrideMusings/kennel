package main

import (
	"bufio"
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The links record is ~/.local/state/kennel/links: one line per share link
// kennel made, the link's path and its target separated by a tab, both
// absolute and clean, sorted by path. It is how doctor and prune find a file
// share's link once its path has left config.toml. kennel env never reads it.

func recordPath() string { return filepath.Join(home(), ".local", "state", "kennel", "links") }

// readRecord returns the recorded links, path to target. A missing file is an
// empty record with os.ErrNotExist; a line that is not two absolute paths is
// skipped.
func readRecord() (map[string]string, error) { return readRecordAt(recordPath()) }

// readRecordAt reads a record at path: the links record or the vault's.
func readRecordAt(path string) (map[string]string, error) {
	rec := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		path, target, ok := strings.Cut(sc.Text(), "\t")
		if ok && filepath.IsAbs(path) && filepath.IsAbs(target) {
			rec[filepath.Clean(path)] = target
		}
	}
	return rec, sc.Err()
}

func writeRecord(rec map[string]string) error { return writeRecordAt(recordPath(), rec) }

func writeRecordAt(path string, rec map[string]string) error {
	var b strings.Builder
	for _, p := range slices.Sorted(maps.Keys(rec)) {
		fmt.Fprintf(&b, "%s\t%s\n", p, rec[p])
	}
	return writeAtomic(path, []byte(b.String()))
}

// updateRecord reads the record, lets edit change it, and writes it back when
// edit reports a change. A record that exists but cannot be read is left as
// it is, so one bad read never wipes it. A failure is a warning: the links
// themselves are already made or removed.
func updateRecord(edit func(rec map[string]string) bool) { updateRecordAt(recordPath(), edit) }

func updateRecordAt(path string, edit func(rec map[string]string) bool) {
	rec, err := readRecordAt(path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "kennel: links not recorded: %v\n", err)
		return
	}
	if !edit(rec) {
		return
	}
	if err := writeRecordAt(path, rec); err != nil {
		fmt.Fprintf(os.Stderr, "kennel: links not recorded: %v\n", err)
	}
}

// recordLinks records every check whose link is in place. A path or target
// holding a tab or newline cannot be written as one line, so it goes
// unrecorded.
func recordLinks(checks []linkCheck) {
	updateRecord(func(rec map[string]string) bool {
		changed := false
		for _, c := range checks {
			if c.state != linkOK || strings.ContainsAny(c.path+c.target, "\t\n") {
				continue
			}
			if p := filepath.Clean(c.path); rec[p] != c.target {
				rec[p] = c.target
				changed = true
			}
		}
		return changed
	})
}

// forgetLinks drops each path from the record.
func forgetLinks(paths []string) {
	updateRecord(func(rec map[string]string) bool {
		changed := false
		for _, p := range paths {
			if _, ok := rec[filepath.Clean(p)]; ok {
				delete(rec, filepath.Clean(p))
				changed = true
			}
		}
		return changed
	})
}

// recordedOrphans finds the recorded links no share asks for any more: a path
// that is still a symlink to its recorded target, not one of sharePairs'
// paths, and not inside a folder share's into (orphanChecks owns those). Each
// comes back as linkRecorded. stale lists every recorded path that is gone or
// points elsewhere now: prune drops their lines and never touches the path.
func recordedOrphans(cfg *Config, rec map[string]string) (orphans []linkCheck, stale []string) {
	listed := map[string]bool{}
	for _, p := range sharePairs(cfg) {
		listed[p.path] = true
	}
	var intos []string
	for _, name := range cfg.shareNames() {
		if into := cfg.Shares[name].Into; into != "" {
			intos = append(intos, filepath.Clean(into)+string(filepath.Separator))
		}
	}
	for _, path := range slices.Sorted(maps.Keys(rec)) {
		if actual, err := os.Readlink(path); err != nil || actual != rec[path] {
			stale = append(stale, path)
			continue
		}
		if listed[path] || slices.ContainsFunc(intos, func(in string) bool { return strings.HasPrefix(path, in) }) {
			continue
		}
		orphans = append(orphans, linkCheck{pair: pair{label: "recorded", path: path, target: rec[path]}, state: linkRecorded, actual: rec[path]})
	}
	return orphans, stale
}
