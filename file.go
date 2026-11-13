package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const fileUsage = `usage:
  kennel file log <path> [--json]      the commits that touched a file, newest first
  kennel file show <path>[@<rev>]      a file's text now, or at a commit`

// runGit runs one git subcommand in dir, with global options pre before it,
// and with the caller's GIT_* variables dropped, so a calling hook's GIT_DIR
// or GIT_INDEX_FILE cannot point it at another repository.
func runGit(dir string, pre []string, args ...string) (string, error) {
	full := append(append([]string{"-c", "core.quotepath=false"}, pre...), args...)
	c := exec.Command("git", full...)
	c.Dir = dir
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			c.Env = append(c.Env, e)
		}
	}
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// heldFile is a file and the repository that holds it.
type heldFile struct {
	path string                               // the file, links resolved
	rel  string                               // its path inside the repository
	git  func(args ...string) (string, error) // runs git in that repository; nil when none holds it
}

// holdingRepo finds the repository that holds a file: the vault's when the
// file resolves into the vault, else the one git finds from the file's
// folder. A file in no repository comes back with a nil git.
func holdingRepo(arg string) (heldFile, error) {
	abs, err := filepath.Abs(expand(arg))
	if err != nil {
		return heldFile{}, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// A file deleted since a commit still has history: resolve its folder.
		dir, derr := filepath.EvalSymlinks(filepath.Dir(abs))
		if derr != nil {
			return heldFile{}, fmt.Errorf("%s: no such file or folder", tilde(abs))
		}
		real = filepath.Join(dir, filepath.Base(abs))
	}
	f := heldFile{path: real}
	v := &vault{dir: filepath.Join(dataDir(), "vault"), gitDir: filepath.Join(dataDir(), "vault.git")}
	if root, err := filepath.EvalSymlinks(v.dir); err == nil && strings.HasPrefix(real, root+"/") {
		if _, err := os.Stat(filepath.Join(v.gitDir, "HEAD")); err == nil {
			f.rel, f.git = real[len(root)+1:], v.git
			return f, nil
		}
	}
	top, err := runGit(filepath.Dir(real), nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return f, nil
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil || !strings.HasPrefix(real, root+"/") {
		return f, nil
	}
	f.rel = real[len(root)+1:]
	f.git = func(args ...string) (string, error) { return runGit(root, nil, args...) }
	return f, nil
}

// fileCommit is one commit that touched a file.
type fileCommit struct {
	Commit  string `json:"commit"`
	Short   string `json:"short"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

// fileLog lists the commits that touched a file, newest first; a file no
// repository holds, one git does not track, or one in a repository with no
// commits yet has none.
func fileLog(arg string) ([]fileCommit, error) {
	f, err := holdingRepo(arg)
	if err != nil || f.git == nil {
		return []fileCommit{}, err
	}
	out, err := f.git("log", "--format=%H%x09%h%x09%aI%x09%s", "--", f.rel)
	if err != nil {
		if unborn(f.git) {
			return []fileCommit{}, nil
		}
		return nil, err
	}
	commits := []fileCommit{}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		p := strings.SplitN(line, "\t", 4)
		if len(p) == 4 {
			commits = append(commits, fileCommit{Commit: p[0], Short: p[1], Date: p[2], Subject: p[3]})
		}
	}
	return commits, nil
}

// unborn reports whether HEAD names a branch that has no commit yet.
func unborn(git func(args ...string) (string, error)) bool {
	ref, err := git("symbolic-ref", "-q", "HEAD")
	if err != nil {
		return false
	}
	_, err = git("show-ref", "--verify", "-q", strings.TrimSpace(ref))
	return err != nil
}

func cmdFile(args []string) int {
	if len(args) < 2 {
		return failf(fileUsage)
	}
	switch args[0] {
	case "log":
		asJSON := false
		var pos []string
		for _, a := range args[1:] {
			if a == "--json" {
				asJSON = true
			} else {
				pos = append(pos, a)
			}
		}
		if len(pos) != 1 {
			return failf(fileUsage)
		}
		commits, err := fileLog(pos[0])
		if err != nil {
			return failf("%v", err)
		}
		if asJSON {
			return printJSON(commits)
		}
		for _, c := range commits {
			date := c.Date
			if t, err := time.Parse(time.RFC3339, c.Date); err == nil {
				date = t.Format("2006-01-02 15:04")
			}
			fmt.Printf("%s\t%s\t%s\n", c.Short, date, c.Subject)
		}
		return 0
	case "show":
		if len(args) != 2 {
			return failf(fileUsage)
		}
		return fileShow(args[1])
	}
	return failf(fileUsage)
}

// splitRev splits path@rev at the last @, so a revision may hold a /
// (origin/main). The whole argument stays a path when it names something and
// the part before the @ does not: it exists, or, for a rev holding a /, its
// folder exists (a file deleted from a folder named x@y).
func splitRev(arg string) (path, rev string) {
	i := strings.LastIndex(arg, "@")
	if i <= 0 || i == len(arg)-1 {
		return arg, ""
	}
	whole := expand(arg)
	_, err := os.Lstat(whole)
	if err != nil && strings.Contains(arg[i+1:], "/") {
		_, err = os.Lstat(filepath.Dir(whole))
	}
	if err == nil {
		if _, err := os.Lstat(expand(arg[:i])); err != nil {
			return arg, ""
		}
	}
	return arg[:i], arg[i+1:]
}

// fileShow prints a file's text now, or at a revision of the repository
// that holds it.
func fileShow(arg string) int {
	path, rev := splitRev(arg)
	if rev == "" {
		b, err := os.ReadFile(expand(path))
		if err != nil {
			return failf("%v", err)
		}
		os.Stdout.Write(b)
		return 0
	}
	if strings.HasPrefix(rev, "-") {
		return failf("%q is not a revision", rev)
	}
	f, err := holdingRepo(path)
	if err != nil {
		return failf("%v", err)
	}
	if f.git == nil {
		return failf("%s is in no git repository", tilde(f.path))
	}
	if _, err := f.git("rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}"); err != nil {
		return failf("%s: unknown revision", rev)
	}
	body, err := f.git("cat-file", "blob", rev+":"+f.rel)
	if err != nil {
		if _, e := f.git("cat-file", "-e", rev+":"+f.rel); e != nil {
			return failf("%s does not exist at %s", tilde(f.path), rev)
		}
		return failf("%v", err)
	}
	fmt.Print(body)
	return 0
}

// printJSON writes v as indented JSON with no HTML escaping.
func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return failf("%v", err)
	}
	return 0
}
