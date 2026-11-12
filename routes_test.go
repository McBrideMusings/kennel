package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A wrong resolution starts a CLI logged into the wrong account, so this
// covers every rule that decides the profile: longest match, expiry, rows
// naming an unknown profile, and a linked worktree following its main repo.
func TestResolve(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	repo := filepath.Join(h, "Work", "repo")
	wt := filepath.Join(h, "elsewhere", "wt")
	for _, args := range [][]string{
		{"init", "-q", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", repo, "worktree", "add", "-q", wt},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.MkdirAll(filepath.Join(h, "Workshop"), 0o755)

	cfg := &Config{Profiles: map[string]map[string]string{"work": {}, "personal": {}}}
	lines := []routeLine{
		{path: "~/Work", profile: "work"},
		{path: "~/Work/repo/docs", profile: "personal", expires: "2000-01-01"},
		{path: "~/Work/repo", profile: "ghost"},
		{path: "~/Work/repo/sub", profile: "personal", expires: "2999-01-01"},
	}
	for dir, want := range map[string]string{
		filepath.Join(h, "Work"):                 "work",
		filepath.Join(h, "Workshop"):             "", // a name sharing the prefix is not inside it
		filepath.Join(h, "Work", "repo", "docs"): "work",
		filepath.Join(h, "Work", "repo", "sub"):  "work", // inside a repo, the repo root is matched
		wt:                                       "work",
		filepath.Join(h, "elsewhere"):            "",
	} {
		os.MkdirAll(dir, 0o755)
		if got := resolve(cfg, lines, dir); got != want {
			t.Errorf("resolve(%s) = %q, want %q", strings.TrimPrefix(dir, h), got, want)
		}
	}
}

// The routes file is hand-edited and read on every cd, so a rewrite keeps
// comments, spaced paths and unreadable rows exactly as written, drops only
// the rows it means to, and an unreadable row never stops the others routing.
func TestRoutesFileRoundTrip(t *testing.T) {
	t.Setenv("KENNEL_CONFIG_DIR", t.TempDir())
	h := t.TempDir()
	t.Setenv("HOME", h)
	in := "# mine\n\n~/Work work\n  # indented note\n~/old personal 2000-01-01\n~/my  docs personal 2999-12-31\nlonely\n"
	os.WriteFile(routesPath(), []byte(in), 0o644)

	dropped, err := dropExpired()
	if err != nil || len(dropped) != 1 || dropped[0].path != "~/old" {
		t.Fatalf("dropExpired = %v, %v", dropped, err)
	}
	got, _ := os.ReadFile(routesPath())
	want := "# mine\n\n~/Work work\n  # indented note\n~/my  docs personal 2999-12-31\nlonely\n"
	if string(got) != want {
		t.Errorf("routes after prune:\n%s\nwant:\n%s", got, want)
	}

	lines, _ := readRoutes()
	cfg := &Config{Profiles: map[string]map[string]string{"work": {}, "personal": {}}}
	spaced := filepath.Join(h, "my  docs", "sub")
	os.MkdirAll(spaced, 0o755)
	if p := resolve(cfg, lines, spaced); p != "personal" {
		t.Errorf("resolve(~/my  docs/sub) = %q, want personal", p)
	}
}
