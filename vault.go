package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const vaultUsage = `usage:
  kennel vault add <path>                               move a skill folder or snippet .md into the vault and link it back
  kennel vault install <item> <claude|codex|pi> [--project <dir>]   link a vault skill into a harness's skills
  kennel vault rm <item>                                remove an item no link points at
  kennel vault copy <item> <path>                       copy an item into a project and record it in config.toml
  kennel vault update <path>                            bring a recorded copy to the item's last commit
  kennel vault commit --session <id> --harness <h> <file>   commit one vault file an agent edited
  kennel vault log <item>                               the item's commits
  kennel vault show <item>[@<rev>]                      the item's files at a commit`

// A vault holds skills (skills/<name>/) and snippets (snippets/<name>.md) on
// this Mac. Its history is a git repository kept beside it in vault.git, so
// the vault folder holds no .git; every git call names both directories. It
// never has a remote.
type vault struct {
	dir    string // the files
	gitDir string // git's data
}

// vaultLock holds the open lock file so the lock lasts the whole command.
var vaultLock *os.File

// dataDir is kennel's XDG data directory.
func dataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "kennel")
	}
	return filepath.Join(home(), ".local", "share", "kennel")
}

// openVault returns the vault, creating the folder and its repository with a
// first commit on first use.
func openVault() (*vault, error) {
	base := dataDir()
	v := &vault{dir: filepath.Join(base, "vault"), gitDir: filepath.Join(base, "vault.git")}
	if err := os.MkdirAll(v.dir, 0o755); err != nil {
		return nil, err
	}
	// One vault command at a time: agents' hooks can fire together, and two
	// gits on one index fail. The lock is released when the process exits.
	if vaultLock != nil { // a second vault command in this process (tests)
		vaultLock.Close()
	}
	lock, err := os.OpenFile(filepath.Join(base, "vault.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	vaultLock = lock
	if _, err := v.git("rev-parse", "-q", "--verify", "HEAD"); err == nil {
		return v, nil
	}
	if _, err := os.Stat(filepath.Join(v.gitDir, "HEAD")); err != nil {
		if _, err := v.git("init", "-q", "-b", "main"); err != nil {
			return nil, err
		}
	}
	// Finder drops .DS_Store into every folder it opens. A user's
	// init.templateDir may leave out info/.
	if err := os.MkdirAll(filepath.Join(v.gitDir, "info"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(v.gitDir, "info", "exclude"), []byte(".DS_Store\n"), 0o644); err != nil {
		return nil, err
	}
	if _, err := v.git("commit", "-q", "--allow-empty", "-m", "create the vault"); err != nil {
		return nil, err
	}
	logf("vault created: %s", v.dir)
	return v, nil
}

// git runs one git command against the vault. It sets its own identity and
// turns off the user's hooks, excludes file and signing, and drops GIT_*
// variables a calling hook may have set, so only the vault's own state
// decides what happens.
func (v *vault) git(args ...string) (string, error) {
	return runGit(v.dir, []string{
		"--git-dir=" + v.gitDir, "--work-tree=" + v.dir,
		"-c", "user.name=kennel", "-c", "user.email=kennel@localhost",
		"-c", "core.hooksPath=/dev/null", "-c", "core.excludesFile=/dev/null",
		"-c", "commit.gpgsign=false",
	}, args...)
}

// commitPaths stages every change under paths and commits it with msg. It
// reports whether there was anything to commit.
func (v *vault) commitPaths(msg string, paths ...string) (bool, error) {
	if _, err := v.git(append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return false, err
	}
	staged, err := v.git(append([]string{"diff", "--cached", "--name-only", "--"}, paths...)...)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(staged) == "" {
		return false, nil
	}
	if _, err := v.git(append([]string{"commit", "-q", "-m", msg, "--"}, paths...)...); err != nil {
		return false, err
	}
	return true, nil
}

// sweep commits every change made outside kennel since the last commit as
// one commit. Every vault command runs it first.
func (v *vault) sweep() error {
	if _, err := v.git("add", "-A"); err != nil {
		return err
	}
	staged, err := v.git("diff", "--cached", "--name-only", "-z")
	if err != nil {
		return err
	}
	names := strings.Split(strings.TrimSuffix(staged, "\x00"), "\x00")
	if staged == "" {
		return nil
	}
	msg := fmt.Sprintf("sweep: %d files changed outside kennel", len(names))
	if len(names) == 1 {
		msg = "sweep: " + names[0] + " changed outside kennel"
	}
	if _, err := v.git("commit", "-q", "-m", msg); err != nil {
		return err
	}
	logf("vault %s", msg)
	return nil
}

// item turns an argument into a path relative to the vault: a path with a
// slash as written, or a bare name as skills/<name> or snippets/<name>.md,
// whichever exists now.
func (v *vault) item(arg string) (string, error) {
	arg = strings.TrimSuffix(arg, "/")
	if arg == "" || strings.HasPrefix(arg, "/") || arg == ".." || strings.HasPrefix(arg, "../") || strings.Contains(arg, "/../") {
		return "", fmt.Errorf("%q is not a vault item", arg)
	}
	if strings.Contains(arg, "/") {
		rel := filepath.Clean(arg)
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			return "", fmt.Errorf("%q is not a vault item", arg)
		}
		return rel, nil
	}
	for _, rel := range []string{filepath.Join("skills", arg), filepath.Join("snippets", arg+".md")} {
		if _, err := os.Lstat(filepath.Join(v.dir, rel)); err == nil {
			return rel, nil
		}
	}
	return "", fmt.Errorf("no skill or snippet named %s in %s", arg, tilde(v.dir))
}

func cmdVault(args []string) int {
	if len(args) == 0 {
		return failf(vaultUsage)
	}
	v, err := openVault()
	if err != nil {
		return failf("%v", err)
	}
	sub, args := args[0], args[1:]
	if sub == "commit" {
		return vaultCommit(v, args)
	}
	if err := v.sweep(); err != nil {
		return failf("%v", err)
	}
	switch sub {
	case "add":
		if len(args) != 1 {
			return failf("usage: kennel vault add <path>")
		}
		return vaultAdd(v, args[0])
	case "install":
		return vaultInstall(v, args)
	case "rm":
		if len(args) != 1 {
			return failf("usage: kennel vault rm <item>")
		}
		return vaultRm(v, args[0])
	case "copy":
		return vaultCopy(v, args)
	case "update":
		if len(args) != 1 {
			return failf("usage: kennel vault update <path>")
		}
		return vaultUpdate(v, args[0])
	case "log":
		if len(args) != 1 {
			return failf("usage: kennel vault log <item>")
		}
		return vaultLog(v, args[0])
	case "show":
		if len(args) != 1 {
			return failf("usage: kennel vault show <item>[@<rev>]")
		}
		return vaultShow(v, args[0])
	}
	return failf("unknown vault command %q\n%s", sub, vaultUsage)
}

// vaultAdd copies a skill folder or snippet file into the vault, commits it,
// moves the original to the Trash and puts a link to the vault copy in its
// place. It is the one kennel command that moves a user's files.
func vaultAdd(v *vault, arg string) int {
	src, err := filepath.Abs(expand(arg))
	if err != nil {
		return failf("%v", err)
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return failf("%v", err)
	}
	var rel string
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, _ := os.Readlink(src)
		return failf("%s is already a link to %s", tilde(src), tilde(target))
	case fi.IsDir():
		rel = filepath.Join("skills", filepath.Base(src))
	case fi.Mode().IsRegular() && strings.HasSuffix(src, ".md"):
		rel = filepath.Join("snippets", filepath.Base(src))
	default:
		return failf("%s is not a skill folder or a snippet .md file", tilde(src))
	}
	dst := filepath.Join(v.dir, rel)
	// Compare resolved paths: a folder reached through a link into the vault
	// is part of the vault, and a folder holding the vault would copy into
	// itself.
	parent, err := filepath.EvalSymlinks(filepath.Dir(src))
	if err != nil {
		return failf("%v", err)
	}
	real := filepath.Join(parent, filepath.Base(src))
	root, err := filepath.EvalSymlinks(v.dir)
	if err != nil {
		return failf("%v", err)
	}
	if real == root || strings.HasPrefix(real+"/", root+"/") || strings.HasPrefix(root+"/", real+"/") {
		return failf("%s is the vault, inside it or holds it", tilde(src))
	}
	if _, err := os.Lstat(dst); err == nil {
		return failf("the vault already holds %s", rel)
	}
	// The Trash takes the original by rename, which cannot cross volumes;
	// refuse before copying anything.
	if !sameVolume(src, filepath.Join(home(), ".Trash")) {
		return failf("%s is on another volume than ~/.Trash, so it cannot be moved there", tilde(src))
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return failf("copying %s into the vault: %v", tilde(src), err)
	}
	// -f: a .gitignore inside the skill must not keep its files out of the
	// commit, since the original goes to the Trash.
	if _, err := v.git("add", "-A", "-f", "--", rel); err != nil {
		os.RemoveAll(dst)
		return failf("%v", err)
	}
	done, err := v.commitPaths(fmt.Sprintf("add %s from %s", rel, tilde(src)), rel)
	if err != nil || !done {
		v.git("reset", "-q", "--", rel)
		os.RemoveAll(dst)
		if err == nil {
			err = fmt.Errorf("%s holds no files", tilde(src))
		}
		return failf("%v", err)
	}
	logf("vault add: %s -> %s", src, dst)
	trashed, err := trash(src)
	if err != nil {
		return failf("the vault holds %s, but %s stayed in place: %v", rel, tilde(src), err)
	}
	logf("vault add: moved %s to %s", src, trashed)
	if err := os.Symlink(dst, src); err != nil {
		return failf("the vault holds %s and the original is at %s, but linking %s failed: %v", rel, tilde(trashed), tilde(src), err)
	}
	recordVaultLink(src, dst)
	logf("vault add: linked %s -> %s", src, dst)
	fmt.Printf("Added %s\n  %s → %s\n  Original: %s\n", rel, tilde(src), tilde(dst), tilde(trashed))
	return 0
}

// copyTree copies a file or folder, keeping modes and copying symlinks as
// symlinks. A folder holding a .git is its own repository and is refused.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			return fmt.Errorf("%s is a git repository", tilde(filepath.Dir(p)))
		}
		r, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, r)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			// A relative link reaching outside the folder would dangle in the vault.
			if !filepath.IsAbs(t) {
				if r, err := filepath.Rel(src, filepath.Join(filepath.Dir(p), t)); err != nil || r == ".." || strings.HasPrefix(r, "../") {
					return fmt.Errorf("%s links to %s, outside %s", tilde(p), t, tilde(src))
				}
			}
			return os.Symlink(t, out)
		case fi.IsDir():
			return os.MkdirAll(out, fi.Mode().Perm()|0o700)
		case fi.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			return copyFile(p, out, fi.Mode().Perm())
		}
		return fmt.Errorf("%s is not a file, folder or link", tilde(p))
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// trash moves p into ~/.Trash, adding the time to the name when the Trash
// already holds that name, the way Finder does.
// sameVolume reports whether a and b (or b's nearest existing parent) sit on
// one filesystem.
func sameVolume(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Lstat(a, &sa) != nil {
		return false
	}
	for syscall.Stat(b, &sb) != nil {
		if filepath.Dir(b) == b {
			return false
		}
		b = filepath.Dir(b)
	}
	return sa.Dev == sb.Dev
}

func trash(p string) (string, error) {
	dir := filepath.Join(home(), ".Trash")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Base(p)
	dst := filepath.Join(dir, name)
	for i := 0; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			break
		} else if err != nil {
			return "", err
		}
		ext := filepath.Ext(name)
		stamp := time.Now().Format("15.04.05")
		if i > 0 {
			stamp = fmt.Sprintf("%s %d", stamp, i)
		}
		dst = filepath.Join(dir, strings.TrimSuffix(name, ext)+" "+stamp+ext)
	}
	return dst, os.Rename(p, dst)
}

// vaultCommit commits one file an agent edited, naming the harness and
// session, then sweeps every other change.
func vaultCommit(v *vault, args []string) int {
	var session, harness, file string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case (a == "--session" || a == "--harness") && i+1 < len(args):
			if a == "--session" {
				session = args[i+1]
			} else {
				harness = args[i+1]
			}
			i++
		case file == "" && !strings.HasPrefix(a, "--"):
			file = a
		default:
			return failf("usage: kennel vault commit --session <id> --harness <h> <file>")
		}
	}
	if session == "" || harness == "" || file == "" {
		return failf("usage: kennel vault commit --session <id> --harness <h> <file>")
	}
	rel, err := v.relPath(file)
	if err != nil {
		return failf("%v", err)
	}
	// A file neither on disk nor tracked has nothing to commit; the sweep
	// still runs.
	done, cerr := v.commitPaths(fmt.Sprintf("edit %s (%s session %s)", rel, harness, session), rel)
	if done {
		logf("vault commit: %s (%s session %s)", rel, harness, session)
	}
	if err := v.sweep(); err != nil {
		return failf("%v", err)
	}
	if cerr != nil {
		return failf("%v", cerr)
	}
	return 0
}

// relPath turns a file path, which may run through a link into the vault,
// into its path relative to the vault folder.
func (v *vault) relPath(file string) (string, error) {
	abs, err := filepath.Abs(expand(file))
	if err != nil {
		return "", err
	}
	// A snippet's link is the file itself; a gone file resolves through its
	// folder.
	p, err := filepath.EvalSymlinks(abs)
	if err != nil {
		dir, derr := filepath.EvalSymlinks(filepath.Dir(abs))
		if derr != nil {
			return "", derr
		}
		p = filepath.Join(dir, filepath.Base(abs))
	}
	root, err := filepath.EvalSymlinks(v.dir)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(p, root+"/") {
		return "", fmt.Errorf("%s is not in the vault (%s)", tilde(file), tilde(v.dir))
	}
	return p[len(root)+1:], nil
}

func vaultLog(v *vault, arg string) int {
	rel, err := v.item(arg)
	if err != nil {
		return failf("%v", err)
	}
	out, err := v.git("log", "--format=%h%x09%ad%x09%s", "--date=format:%Y-%m-%d %H:%M", "--", rel)
	if err != nil {
		return failf("%v", err)
	}
	if out == "" {
		return failf("no commits for %s", rel)
	}
	fmt.Print(out)
	return 0
}

// vaultShow prints an item as it was at a commit: a file's text, or each file
// of a folder under a "==> path <==" header.
func vaultShow(v *vault, arg string) int {
	name, rev := arg, "HEAD"
	if i := strings.LastIndex(arg, "@"); i > 0 {
		name, rev = arg[:i], arg[i+1:]
	}
	if rev == "" || strings.HasPrefix(rev, "-") {
		return failf("%q is not a revision", rev)
	}
	rel, err := v.item(name)
	if err != nil {
		return failf("%v", err)
	}
	kind, err := v.git("cat-file", "-t", rev+":"+rel)
	if err != nil {
		return failf("%s does not exist at %s", rel, rev)
	}
	if strings.TrimSpace(kind) == "blob" {
		body, err := v.git("cat-file", "blob", rev+":"+rel)
		if err != nil {
			return failf("%v", err)
		}
		fmt.Print(body)
		return 0
	}
	files, err := v.git("ls-tree", "-r", "-z", "--name-only", rev, "--", rel)
	if err != nil {
		return failf("%v", err)
	}
	for i, f := range strings.Split(strings.TrimSuffix(files, "\x00"), "\x00") {
		body, err := v.git("cat-file", "blob", rev+":"+f)
		if err != nil {
			return failf("%v", err)
		}
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("==> %s <==\n%s", f, body)
	}
	return 0
}
