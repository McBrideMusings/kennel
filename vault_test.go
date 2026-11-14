package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vaultSandbox(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("KENNEL_CONFIG_DIR", filepath.Join(h, ".cfg"))
	t.Setenv("XDG_DATA_HOME", "")
	return h
}

func vaultRun(t *testing.T, args ...string) (string, int) {
	t.Helper()
	return quiet(t, func() int { return run(append([]string{"vault"}, args...)) })
}

func vaultLogLines(t *testing.T, item string) []string {
	t.Helper()
	out, code := vaultRun(t, "log", item)
	if code != 0 {
		t.Fatalf("vault log %s exited %d", item, code)
	}
	return strings.Split(strings.TrimSpace(out), "\n")
}

// The acceptance path: add links the original to the vault and trashes it
// with one commit; an outside edit becomes one sweep commit on the next
// command; log lists both; show at the add prints the original text; the
// vault folder holds no .git.
func TestVaultAddSweepLogShow(t *testing.T) {
	h := vaultSandbox(t)
	skill := filepath.Join(h, ".claude", "skills", "recomp")
	os.MkdirAll(filepath.Join(skill, "refs"), 0o755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("original\n"), 0o644)
	os.WriteFile(filepath.Join(skill, "refs", "a.md"), []byte("ref\n"), 0o644)

	if out, code := vaultRun(t, "add", "~/.claude/skills/recomp"); code != 0 {
		t.Fatalf("vault add exited %d: %s", code, out)
	}
	vdir := filepath.Join(h, ".local", "share", "kennel", "vault")
	item := filepath.Join(vdir, "skills", "recomp")
	if got, _ := os.Readlink(skill); got != item {
		t.Fatalf("original path links to %q, want %q", got, item)
	}
	if b, _ := os.ReadFile(filepath.Join(h, ".Trash", "recomp", "SKILL.md")); string(b) != "original\n" {
		t.Fatalf("Trash copy holds %q", b)
	}
	if _, err := os.Lstat(filepath.Join(vdir, ".git")); err == nil {
		t.Fatal("the vault folder holds a .git")
	}
	if lines := vaultLogLines(t, "recomp"); len(lines) != 1 || !strings.Contains(lines[0], "add skills/recomp") {
		t.Fatalf("log after add = %q", lines)
	}

	// An edit outside kennel, through the link.
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("edited\n"), 0o644)
	lines := vaultLogLines(t, "recomp")
	if len(lines) != 2 || !strings.Contains(lines[0], "sweep: skills/recomp/SKILL.md") || !strings.Contains(lines[1], "add skills/recomp") {
		t.Fatalf("log after the outside edit = %q", lines)
	}
	first := strings.Fields(lines[1])[0]
	out, code := vaultRun(t, "show", "skills/recomp/SKILL.md@"+first)
	if code != 0 || out != "original\n" {
		t.Fatalf("show at the add = %q (exit %d)", out, code)
	}
	out, _ = vaultRun(t, "show", "recomp@"+first)
	if !strings.Contains(out, "==> skills/recomp/SKILL.md <==\noriginal\n") || !strings.Contains(out, "==> skills/recomp/refs/a.md <==\nref\n") {
		t.Fatalf("show of the folder = %q", out)
	}
}

// An agent's edit is committed with its session, apart from other changes,
// which the same command sweeps into their own commit.
func TestVaultCommitNamesSession(t *testing.T) {
	h := vaultSandbox(t)
	snip := filepath.Join(h, "notes", "tone.md")
	os.MkdirAll(filepath.Dir(snip), 0o755)
	os.WriteFile(snip, []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(h, "notes", "other.md"), []byte("b\n"), 0o644)
	if _, code := vaultRun(t, "add", snip); code != 0 {
		t.Fatal("add of a snippet failed")
	}
	if _, code := vaultRun(t, "add", filepath.Join(h, "notes", "other.md")); code != 0 {
		t.Fatal("add of a second snippet failed")
	}
	os.WriteFile(snip, []byte("agent\n"), 0o644)
	os.WriteFile(filepath.Join(h, "notes", "other.md"), []byte("editor\n"), 0o644)
	if out, code := vaultRun(t, "commit", "--session", "s1", "--harness", "claude", snip); code != 0 {
		t.Fatalf("commit exited %d: %s", code, out)
	}
	if lines := vaultLogLines(t, "tone"); !strings.Contains(lines[0], "edit snippets/tone.md (claude session s1)") {
		t.Fatalf("tone log = %q", lines)
	}
	if lines := vaultLogLines(t, "other"); !strings.Contains(lines[0], "sweep: snippets/other.md") {
		t.Fatalf("other log = %q", lines)
	}
	if _, code := vaultRun(t, "commit", "--session", "s1", "--harness", "claude", filepath.Join(h, "notes", "elsewhere.md")); code != 1 {
		t.Fatal("commit of a file outside the vault did not exit 1")
	}
}

// add refuses a link, a name the vault already holds and a folder that is a
// git repository, and leaves each where it was.
func TestVaultAddRefuses(t *testing.T) {
	h := vaultSandbox(t)
	os.MkdirAll(filepath.Join(h, "a", "dup"), 0o755)
	os.MkdirAll(filepath.Join(h, "b", "dup"), 0o755)
	os.WriteFile(filepath.Join(h, "a", "dup", "SKILL.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(h, "b", "dup", "SKILL.md"), []byte("y"), 0o644)
	os.MkdirAll(filepath.Join(h, "repo", ".git"), 0o755)
	os.Symlink(filepath.Join(h, "a"), filepath.Join(h, "ln"))

	if _, code := vaultRun(t, "add", filepath.Join(h, "a", "dup")); code != 0 {
		t.Fatal("first add failed")
	}
	for _, p := range []string{filepath.Join(h, "b", "dup"), filepath.Join(h, "repo"), filepath.Join(h, "ln")} {
		before, _ := os.Lstat(p)
		if _, code := vaultRun(t, "add", p); code != 1 {
			t.Fatalf("add %s did not exit 1", p)
		}
		after, err := os.Lstat(p)
		if err != nil || before.Mode() != after.Mode() {
			t.Fatalf("add %s changed it", p)
		}
	}
	if _, err := os.Lstat(filepath.Join(h, ".local", "share", "kennel", "vault", "skills", "repo")); err == nil {
		t.Fatal("a refused repository left a copy in the vault")
	}

	// A folder reached through a link into the vault is the vault's own; adding
	// it would move part of the vault to the Trash.
	os.MkdirAll(filepath.Join(h, "a", "dup", "refs"), 0o755)
	os.WriteFile(filepath.Join(h, "a", "dup", "refs", "r.md"), []byte("r"), 0o644)
	if _, code := vaultRun(t, "add", filepath.Join(h, "a", "dup", "refs")); code != 1 {
		t.Fatal("add of a folder inside the vault did not exit 1")
	}
	if b, _ := os.ReadFile(filepath.Join(h, "a", "dup", "refs", "r.md")); string(b) != "r" {
		t.Fatal("add of a folder inside the vault moved it")
	}
	// A relative link out of the folder would dangle once the folder moves.
	os.MkdirAll(filepath.Join(h, "rel"), 0o755)
	os.Symlink("../a", filepath.Join(h, "rel", "up"))
	if _, code := vaultRun(t, "add", filepath.Join(h, "rel")); code != 1 {
		t.Fatal("add of a folder with a link out of it did not exit 1")
	}
}

// A .gitignore inside a skill keeps none of its files out of the vault: an
// edit to an ignored file, and an ignored file made after the add, are in
// the next sweep commit. Finder's .DS_Store is never committed, at add or at
// a sweep.
func TestVaultSweepIgnoredFiles(t *testing.T) {
	h := vaultSandbox(t)
	skill := filepath.Join(h, ".claude", "skills", "ign")
	os.MkdirAll(filepath.Join(skill, "sub"), 0o755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("s\n"), 0o644)
	os.WriteFile(filepath.Join(skill, ".gitignore"), []byte("*.txt\n"), 0o644)
	os.WriteFile(filepath.Join(skill, "x.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(skill, ".DS_Store"), []byte("f"), 0o644)
	if out, code := vaultRun(t, "add", skill); code != 0 {
		t.Fatalf("vault add exited %d: %s", code, out)
	}
	v := vaultAt()
	// Finder's own files alone make no sweep commit.
	os.WriteFile(filepath.Join(v.dir, ".DS_Store"), []byte("f"), 0o644)
	os.WriteFile(filepath.Join(skill, ".DS_Store"), []byte("g"), 0o644)
	head := func() string {
		return gitT(t, h, "--git-dir="+v.gitDir, "--work-tree="+v.dir, "rev-parse", "HEAD")
	}
	before := head()
	vaultLogLines(t, "ign")
	if head() != before {
		t.Fatal("a .DS_Store-only change made a sweep commit")
	}
	os.WriteFile(filepath.Join(skill, "x.txt"), []byte("two\n"), 0o644)
	os.WriteFile(filepath.Join(skill, "sub", "y.txt"), []byte("new\n"), 0o644)
	os.WriteFile(filepath.Join(skill, "sub", ".DS_Store"), []byte("f"), 0o644)
	lines := vaultLogLines(t, "ign")
	if len(lines) != 2 || !strings.Contains(lines[0], "sweep: 2 files changed") {
		t.Fatalf("log after editing the ignored file = %q", lines)
	}
	if out, _ := vaultRun(t, "show", "skills/ign/x.txt@"+strings.Fields(lines[0])[0]); out != "two\n" {
		t.Fatalf("x.txt at the sweep = %q", out)
	}
	files := gitT(t, h, "--git-dir="+v.gitDir, "--work-tree="+v.dir, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(files, ".DS_Store") || !strings.Contains(files, "skills/ign/sub/y.txt") {
		t.Fatalf("vault tree = %q", files)
	}
}
