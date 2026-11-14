package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// vaultLinksPath is the vault's links record: every link to a vault item that
// vault add and vault install made, in the links record's format. vault rm
// reads it to find links outside the harness homes it scans.
func vaultLinksPath() string {
	return filepath.Join(home(), ".local", "state", "kennel", "vault-links")
}

func recordVaultLink(path, target string) {
	if strings.ContainsAny(path+target, "\t\n") {
		return
	}
	updateRecordAt(vaultLinksPath(), func(rec map[string]string) bool {
		if rec[path] == target {
			return false
		}
		rec[path] = target
		return true
	})
}

// topItem is item for the commands that act on a whole skill or snippet:
// skills/<name> or snippets/<name>.md, present in the vault now.
func (v *vault) topItem(arg string) (string, error) {
	rel, err := v.item(arg)
	if err != nil {
		return "", err
	}
	dir, name := filepath.Split(rel)
	if (dir != "skills/" && dir != "snippets/") || name == "" {
		return "", fmt.Errorf("%s is not a skill or snippet; name skills/<name>, snippets/<name>.md or a bare name", rel)
	}
	if _, err := os.Lstat(filepath.Join(v.dir, rel)); err != nil {
		return "", fmt.Errorf("the vault holds no %s", rel)
	}
	return rel, nil
}

// lastCommit is the full sha of the last commit that changed rel.
func (v *vault) lastCommit(rel string) (string, error) {
	out, err := v.git("log", "-1", "--format=%H", "--", rel)
	if err != nil {
		return "", err
	}
	if sha := strings.TrimSpace(out); sha != "" {
		return sha, nil
	}
	return "", fmt.Errorf("no commits for %s", rel)
}

// treeEntry is one file of an item at a commit.
type treeEntry struct {
	mode string // git's mode: 100644, 100755 or 120000 for a link
	sha  string
}

// tree lists the files of rel at rev, keyed by their path relative to rel
// ("" for a snippet, which is one file).
func (v *vault) tree(rev, rel string) (map[string]treeEntry, error) {
	out, err := v.git("ls-tree", "-r", "-z", "--full-tree", rev, "--", rel)
	if err != nil {
		return nil, err
	}
	files := map[string]treeEntry{}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		meta, p, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || f[1] != "blob" {
			continue
		}
		r := strings.TrimPrefix(strings.TrimPrefix(p, rel), "/")
		files[r] = treeEntry{mode: f[0], sha: f[2]}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s does not exist at %s", rel, rev)
	}
	return files, nil
}

// writeItem writes rel as it was at rev to dst, which must not exist: the
// files git holds, so a file the vault folder has but git ignores stays out.
func (v *vault) writeItem(rev, rel, dst string) error {
	files, err := v.tree(rev, rel)
	if err != nil {
		return err
	}
	for r, e := range files {
		out := filepath.Join(dst, r)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		body, err := v.git("cat-file", "blob", e.sha)
		if err != nil {
			return err
		}
		switch e.mode {
		case "120000":
			err = os.Symlink(body, out)
		case "100755":
			err = os.WriteFile(out, []byte(body), 0o755)
		default:
			err = os.WriteFile(out, []byte(body), 0o644)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// placeCopy puts rel's files at rev in place of dst (a link into the vault,
// an earlier copy, or nothing) and records dst in config.toml's [copied]
// table. vault copy, unlink and update all go through it. An existing dst
// is replaced only when replace is set: the caller has checked it is a link
// into the vault or a copy that matches its recorded commit. The files are
// written beside dst and swapped in, then the record is written; when either
// step fails, dst is put back as it was. The two temp names are kennel's
// own, so a crash's leftovers are cleared first.
func (v *vault) placeCopy(cfg *Config, rel, rev, dst string, replace bool) error {
	origin := copyOrigin{Item: rel, Rev: rev}
	src, err := os.ReadFile(cfg.Path)
	if err != nil {
		return err
	}
	edited, err := setCopied(src, tilde(dst), origin.String())
	if err != nil {
		return fmt.Errorf("%s: %v", cfg.Path, err)
	}
	dir, base := filepath.Dir(dst), filepath.Base(dst)
	fresh, old := filepath.Join(dir, "."+base+".kennel-new"), filepath.Join(dir, "."+base+".kennel-old")
	_, lerr := os.Lstat(dst)
	had := lerr == nil
	if had && !replace {
		return fmt.Errorf("%s already exists; nothing was copied", tilde(dst))
	}
	os.RemoveAll(fresh)
	os.RemoveAll(old)
	if err := v.writeItem(rev, rel, fresh); err != nil {
		os.RemoveAll(fresh)
		return fmt.Errorf("copying %s: %v", rel, err)
	}
	restore := func(err error) error {
		if !had {
			return err
		}
		if rerr := os.Rename(old, dst); rerr != nil {
			return fmt.Errorf("%v; putting %s back failed too, it is at %s: %v", err, tilde(dst), tilde(old), rerr)
		}
		return err
	}
	if had {
		if err := os.Rename(dst, old); err != nil {
			os.RemoveAll(fresh)
			return err
		}
	}
	if err := os.Rename(fresh, dst); err != nil {
		os.RemoveAll(fresh)
		return restore(err)
	}
	// The record goes with the files: without it a copy could never update.
	if _, err := rewriteConfig(cfg, src, edited); err != nil {
		os.RemoveAll(dst)
		return restore(err)
	}
	// old is the replaced link or a copy that matched a vault commit, so
	// removing it loses nothing; a link goes, never what it points at.
	os.RemoveAll(old)
	return nil
}

// changedFiles lists the files of the copy at dst that differ from rel at
// rev: changed, added or gone. Finder's .DS_Store does not count.
func (v *vault) changedFiles(rev, rel, dst string) ([]string, error) {
	files, err := v.tree(rev, rel)
	if err != nil {
		return nil, err
	}
	var changed []string
	seen := map[string]bool{}
	err = filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == ".DS_Store" {
			return nil
		}
		r, _ := filepath.Rel(dst, p)
		if r == "." {
			r = ""
		}
		seen[r] = true
		e, ok := files[r]
		if !ok {
			changed = append(changed, p)
			return nil
		}
		var have string
		if d.Type()&fs.ModeSymlink != 0 {
			have, err = os.Readlink(p)
		} else {
			var b []byte
			b, err = os.ReadFile(p)
			have = string(b)
		}
		if err != nil {
			return err
		}
		if want, err := v.git("cat-file", "blob", e.sha); err != nil || want != have || modeChanged(d, e) {
			changed = append(changed, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for r := range files {
		if !seen[r] {
			changed = append(changed, filepath.Join(dst, r))
		}
	}
	slices.Sort(changed)
	return changed, nil
}

// modeChanged reports whether a regular file's executable bit differs from
// the mode git recorded.
func modeChanged(d fs.DirEntry, e treeEntry) bool {
	if !d.Type().IsRegular() {
		return false
	}
	fi, err := d.Info()
	return err != nil || (fi.Mode()&0o111 != 0) != (e.mode == "100755")
}

// vaultInstall links a vault skill into a harness's skills folder: its global
// one, or the project's with --project.
func vaultInstall(v *vault, args []string) int {
	const use = "usage: kennel vault install <item> <claude|codex|pi> [--project <dir>]"
	var pos []string
	scope := "global"
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--project" && i+1 < len(args):
			scope = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--"):
			return failf(use)
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) != 2 {
		return failf(use)
	}
	harness := pos[1]
	if !slices.Contains(harnessNames, harness) {
		return failf("unknown harness %q (claude, codex, pi)", harness)
	}
	rel, err := v.topItem(pos[0])
	if err != nil {
		return failf("%v", err)
	}
	if !strings.HasPrefix(rel, "skills/") {
		return failf("%s is a snippet; install links skills", rel)
	}
	cfg, err := loadConfig()
	if err != nil {
		return failf("%v", err)
	}
	sc, err := resolveScope(cfg, scope, harness)
	if err != nil {
		return failf("%v", err)
	}
	dir := sc.skillDirs(sc.Kind)[0]
	dst, target := filepath.Join(dir, filepath.Base(rel)), filepath.Join(v.dir, rel)
	if _, err := os.Lstat(dst); err == nil {
		if got, _ := os.Readlink(dst); got == target {
			recordVaultLink(dst, target)
			fmt.Printf("Already linked: %s → %s\n", tilde(dst), tilde(target))
			return 0
		}
		return failf("%s already exists; nothing was moved", tilde(dst))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return failf("%v", err)
	}
	if err := os.Symlink(target, dst); err != nil {
		return failf("%v", err)
	}
	recordVaultLink(dst, target)
	logf("vault install: linked %s -> %s", dst, target)
	fmt.Printf("Installed %s for %s\n  %s → %s\n", rel, harness, tilde(dst), tilde(target))
	return 0
}

// vaultLinks finds every link that resolves to rel or into it: the recorded
// ones, and every entry of each harness's skills folders in the base home and
// each profile's home. Recorded links that are gone or point elsewhere leave
// the record.
func vaultLinks(v *vault, cfg *Config, rel string) ([]string, error) {
	item, err := filepath.EvalSymlinks(filepath.Join(v.dir, rel))
	if err != nil {
		return nil, err
	}
	rec, err := readRecordAt(vaultLinksPath())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	candidates := map[string]bool{}
	for p := range rec {
		candidates[p] = true
	}
	for _, h := range harnessNames {
		homes := []string{toolBase(cfg, h)}
		for _, prof := range cfg.profileNames() {
			if hm := cfg.Profiles[prof][h]; hm != "" {
				homes = append(homes, hm)
			}
		}
		for _, hm := range homes {
			sc := &harnessScope{Kind: "global", Home: hm, cfg: cfg, harness: h}
			for _, dir := range sc.skillDirs("global") {
				ents, _ := os.ReadDir(dir)
				for _, e := range ents {
					candidates[filepath.Join(dir, e.Name())] = true
				}
			}
		}
	}
	var linked, stale []string
	for p := range candidates {
		// Only a path that is gone or no longer a link leaves the record: a
		// folder on an unmounted volume still holds its link.
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			if rec[p] != "" && (os.IsNotExist(err) || err == nil) {
				stale = append(stale, p)
			}
			continue
		}
		if real, err := filepath.EvalSymlinks(p); err == nil && (real == item || strings.HasPrefix(real, item+"/")) {
			linked = append(linked, p)
		}
	}
	if len(stale) > 0 {
		updateRecordAt(vaultLinksPath(), func(rec map[string]string) bool {
			for _, p := range stale {
				delete(rec, p)
			}
			return true
		})
	}
	slices.Sort(linked)
	return linked, nil
}

// vaultRm moves an item to the Trash and commits its removal, once no link
// points at it. Its history stays in the vault.
func vaultRm(v *vault, arg string) int {
	rel, err := v.topItem(arg)
	if err != nil {
		return failf("%v", err)
	}
	cfg, err := loadConfig()
	if err != nil {
		return failf("%v", err)
	}
	linked, err := vaultLinks(v, cfg, rel)
	if err != nil {
		return failf("%v", err)
	}
	if len(linked) > 0 {
		for _, p := range linked {
			fmt.Printf("linked    %s\n", tilde(p))
		}
		return failf("%s is still linked from %d place(s); remove those links first", rel, len(linked))
	}
	// To the Trash, not deleted: a file the item's own .gitignore hides has
	// no copy in the vault's history.
	trashed, err := trash(filepath.Join(v.dir, rel))
	if err != nil {
		return failf("%v", err)
	}
	if _, err := v.commitPaths("rm "+rel, rel); err != nil {
		return failf("%v", err)
	}
	logf("vault rm: %s, moved to %s", rel, trashed)
	fmt.Printf("Removed %s\n  Files: %s\n  kennel vault log %s lists its commits\n", rel, tilde(trashed), rel)
	return 0
}

// vaultCopy writes a vault item into a project as plain files and records
// where it came from in config.toml's [copied] table.
func vaultCopy(v *vault, args []string) int {
	if len(args) != 2 {
		return failf("usage: kennel vault copy <item> <path>")
	}
	rel, err := v.topItem(args[0])
	if err != nil {
		return failf("%v", err)
	}
	dst, err := filepath.Abs(expand(args[1]))
	if err != nil {
		return failf("%v", err)
	}
	if isDir(dst) {
		dst = filepath.Join(dst, filepath.Base(rel))
	}
	if _, err := os.Lstat(dst); err == nil {
		return failf("%s already exists; nothing was copied", tilde(dst))
	}
	if inVault(v, dst) {
		return failf("%s is inside the vault", tilde(dst))
	}
	cfg, err := loadConfig()
	if err != nil {
		return failf("%v", err)
	}
	rev, err := v.lastCommit(rel)
	if err != nil {
		return failf("%v", err)
	}
	if err := v.placeCopy(cfg, rel, rev, dst, false); err != nil {
		return failf("%v", err)
	}
	origin := copyOrigin{Item: rel, Rev: rev}
	logf("vault copy: %s -> %s (%s)", rel, dst, origin)
	fmt.Printf("Copied %s at %s\n  to %s\n  recorded in %s [copied]\n", rel, rev, tilde(dst), tilde(cfg.Path))
	return 0
}

// inVault reports whether p, or the nearest folder above it that exists,
// resolves into the vault.
func inVault(v *vault, p string) bool {
	root, err := filepath.EvalSymlinks(v.dir)
	if err != nil {
		return false
	}
	for q := p; ; q = filepath.Dir(q) {
		if real, err := filepath.EvalSymlinks(q); err == nil {
			real = filepath.Join(real, strings.TrimPrefix(p, q))
			return real == root || strings.HasPrefix(real, root+"/")
		}
		if filepath.Dir(q) == q {
			return false
		}
	}
}

// vaultUpdate brings a recorded copy to its item's last commit. A copy whose
// files differ from the commit it records holds edits of its own, so it is
// left as it is.
func vaultUpdate(v *vault, arg string) int {
	dst, err := filepath.Abs(expand(arg))
	if err != nil {
		return failf("%v", err)
	}
	cfg, err := loadConfig()
	if err != nil {
		return failf("%v", err)
	}
	origin, ok := cfg.Copied[dst]
	if !ok {
		return failf("%s has no [copied] entry in %s", tilde(dst), tilde(cfg.Path))
	}
	if _, err := os.Lstat(filepath.Join(v.dir, origin.Item)); err != nil {
		return failf("the vault no longer holds %s", origin.Item)
	}
	rev, err := v.lastCommit(origin.Item)
	if err != nil {
		return failf("%v", err)
	}
	if rev == origin.Rev {
		fmt.Printf("Up to date: %s matches %s at %s\n", tilde(dst), origin.Item, rev)
		return 0
	}
	if _, err := os.Lstat(dst); err != nil {
		return failf("%v", err)
	}
	changed, err := v.changedFiles(origin.Rev, origin.Item, dst)
	if err != nil {
		return failf("%v", err)
	}
	if len(changed) > 0 {
		for _, p := range changed {
			fmt.Printf("edited    %s\n", tilde(p))
		}
		return failf("%s differs from %s at %s in %d file(s); nothing was updated", tilde(dst), origin.Item, origin.Rev, len(changed))
	}
	if err := v.placeCopy(cfg, origin.Item, rev, dst, true); err != nil {
		return failf("%v", err)
	}
	next := copyOrigin{Item: origin.Item, Rev: rev}
	logf("vault update: %s %s -> %s", dst, origin, next)
	fmt.Printf("Updated %s\n  %s → %s\n", tilde(dst), origin, next)
	return 0
}
