package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// vaultItem is one skill or snippet in the vault.
type vaultItem struct {
	Name  string   `json:"name"`
	Item  string   `json:"item"`            // skills/<name> or snippets/<name>.md
	Path  string   `json:"path"`            // the item in the vault folder
	Files []string `json:"files,omitempty"` // a skill's files, relative to Path
}

// vaultCopyState is one [copied] record and whether the vault moved ahead of it.
type vaultCopyState struct {
	Path   string `json:"path"`
	Item   string `json:"item"`
	Rev    string `json:"rev"`  // the commit the copy was made from
	Head   string `json:"head"` // the item's last commit; empty when the vault no longer holds it
	Behind bool   `json:"behind"`
}

type vaultList struct {
	Dir      string           `json:"dir"`
	Skills   []vaultItem      `json:"skills"`
	Snippets []vaultItem      `json:"snippets"`
	Copies   []vaultCopyState `json:"copies"`
}

// vaultLs lists the vault's skills and snippets, and each recorded copy with
// whether its item has a newer commit than the one it was copied from.
func vaultLs(v *vault, args []string) int {
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			return failf("usage: kennel vault ls [--json]")
		}
		asJSON = true
	}
	cfg, err := loadConfig()
	if err != nil {
		return failf("%v", err)
	}
	l := vaultList{Dir: v.dir, Skills: []vaultItem{}, Snippets: []vaultItem{}, Copies: []vaultCopyState{}}
	for _, kind := range []string{"skills", "snippets"} {
		ents, err := os.ReadDir(filepath.Join(v.dir, kind))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return failf("%v", err)
		}
		for _, e := range ents {
			p := filepath.Join(v.dir, kind, e.Name())
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			it := vaultItem{Name: e.Name(), Item: kind + "/" + e.Name(), Path: p}
			if kind == "skills" {
				if !e.IsDir() {
					continue
				}
				// one unreadable skill still lists, with no files, so the
				// rest of the vault shows
				it.Files, _ = listFiles(p)
				l.Skills = append(l.Skills, it)
			} else if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".md") {
				it.Name = strings.TrimSuffix(e.Name(), ".md")
				l.Snippets = append(l.Snippets, it)
			}
		}
	}
	for path, o := range cfg.Copied {
		c := vaultCopyState{Path: path, Item: o.Item, Rev: o.Rev}
		if _, err := os.Lstat(filepath.Join(v.dir, o.Item)); err == nil {
			c.Head, _ = v.lastCommit(o.Item)
		}
		c.Behind = c.Head != "" && c.Head != c.Rev
		l.Copies = append(l.Copies, c)
	}
	slices.SortFunc(l.Copies, func(a, b vaultCopyState) int { return strings.Compare(a.Path, b.Path) })
	if asJSON {
		return printJSON(l)
	}
	for _, it := range append(l.Skills, l.Snippets...) {
		fmt.Println(it.Item)
	}
	for _, c := range l.Copies {
		state := "up to date"
		switch {
		case c.Head == "":
			state = "the vault no longer holds it"
		case c.Behind:
			state = "vault has " + c.Head[:min(7, len(c.Head))] + "; kennel vault update " + tilde(c.Path)
		}
		fmt.Printf("copied    %s  %s@%s  %s\n", tilde(c.Path), c.Item, c.Rev[:min(7, len(c.Rev))], state)
	}
	return 0
}

// vaultUnlink replaces a link to a vault skill or snippet with the item's
// files at its last commit, and records the copy in config.toml's [copied]
// table. Removing the link deletes no file: the vault keeps the item.
func vaultUnlink(v *vault, arg string) int {
	dst, err := filepath.Abs(expand(arg))
	if err != nil {
		return failf("%v", err)
	}
	fi, err := os.Lstat(dst)
	if err != nil {
		return failf("%v", err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		return failf("%s is not a link", tilde(dst))
	}
	rel, err := v.relPath(dst)
	if err != nil {
		return failf("%v", err)
	}
	if rel, err = v.topItem(rel); err != nil {
		return failf("%v", err)
	}
	cfg, err := loadConfig()
	if err != nil {
		return failf("%v", err)
	}
	rev, err := v.lastCommit(rel)
	if err != nil {
		return failf("%v", err)
	}
	origin := copyOrigin{Item: rel, Rev: rev}
	src, err := os.ReadFile(cfg.Path)
	if err != nil {
		return failf("%v", err)
	}
	edited, err := setCopied(src, tilde(dst), origin.String())
	if err != nil {
		return failf("%s: %v", cfg.Path, err)
	}
	// The files are written beside the link and swapped in, so a failure
	// leaves the link as it was. The temp name is kennel's own.
	target, err := os.Readlink(dst)
	if err != nil {
		return failf("%v", err)
	}
	fresh := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".kennel-new")
	os.RemoveAll(fresh)
	if err := v.writeItem(rev, rel, fresh); err != nil {
		os.RemoveAll(fresh)
		return failf("copying %s: %v", rel, err)
	}
	if err := os.Remove(dst); err != nil {
		os.RemoveAll(fresh)
		return failf("%v", err)
	}
	if err := os.Rename(fresh, dst); err != nil {
		os.RemoveAll(fresh)
		if lerr := os.Symlink(target, dst); lerr != nil {
			return failf("%v; putting the link back failed too: %v", err, lerr)
		}
		return failf("%v", err)
	}
	// The record goes with the files: without it the copy could never update,
	// so a failed write puts the link back.
	if _, err := rewriteConfig(cfg, src, edited); err != nil {
		os.RemoveAll(dst)
		if lerr := os.Symlink(target, dst); lerr != nil {
			return failf("%v; putting the link back failed too: %v", err, lerr)
		}
		return failf("%v", err)
	}
	logf("vault unlink: %s -> files of %s (%s)", dst, rel, origin)
	fmt.Printf("Unlinked %s\n  now a copy of %s at %s\n  recorded in %s [copied]\n", tilde(dst), rel, rev, tilde(cfg.Path))
	return 0
}
