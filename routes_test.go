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

// A pi block with no env key must route PI_CODING_AGENT_DIR, or pi started in
// a work directory logs into the personal account.
func TestEnvRoutesPiBuiltIn(t *testing.T) {
	h, cfg, err := shareSandbox(t, "[tools.pi]\nbase = \"~/.pi/agent\"\nshared = []\n[profiles.work]\npi = \"~/.pi-work\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Tools["pi"]; p.Env != "PI_CODING_AGENT_DIR" || p.EnvSource != "built-in" {
		t.Fatalf("pi env = %q (%s), want PI_CODING_AGENT_DIR (built-in)", p.Env, p.EnvSource)
	}
	os.WriteFile(routesPath(), []byte("~/Work work\n"), 0o644)
	work := filepath.Join(h, "Work", "repo")
	os.MkdirAll(work, 0o755)
	t.Chdir(work)
	out, _ := quiet(t, func() int { return cmdEnv(cfg, false) })
	if want := "export PI_CODING_AGENT_DIR=" + shellQuote(filepath.Join(h, ".pi-work")) + "\n"; !strings.Contains(out, want) {
		t.Errorf("kennel env in ~/Work printed:\n%s\nwant a line %q", out, want)
	}
	t.Chdir(h)
	if out, _ := quiet(t, func() int { return cmdEnv(cfg, false) }); !strings.Contains(out, "unset PI_CODING_AGENT_DIR\n") {
		t.Errorf("kennel env in ~ printed:\n%s\nwant unset PI_CODING_AGENT_DIR", out)
	}
	if out, _ := quiet(t, func() int { return cmdEnv(cfg, true) }); !strings.Contains(out, "set -e PI_CODING_AGENT_DIR\n") || strings.Contains(out, "unset") {
		t.Errorf("kennel env fish in ~ printed:\n%s\nwant set -e PI_CODING_AGENT_DIR and no unset", out)
	}
	t.Chdir(work)
	if out, _ := quiet(t, func() int { return cmdEnv(cfg, true) }); !strings.Contains(out, "set -gx PI_CODING_AGENT_DIR "+fishQuote(filepath.Join(h, ".pi-work"))+"\n") {
		t.Errorf("kennel env fish in ~/Work printed:\n%s\nwant set -gx PI_CODING_AGENT_DIR", out)
	}
}

func TestFishQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/a/.claude": `'/Users/a/.claude'`,
		"it's":             `'it\'s'`,
		`back\slash`:       `'back\\slash'`,
		"$HOME (x)":        `'$HOME (x)'`,
	} {
		if got := fishQuote(in); got != want {
			t.Errorf("fishQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestInitHintNamesLoginShell(t *testing.T) {
	for shell, want := range map[string]string{
		"/opt/homebrew/bin/bash": "~/.bashrc",
		"/usr/local/bin/fish":    "kennel init fish | source",
		"/bin/zsh":               "~/.zshrc",
		"":                       "~/.zshrc",
	} {
		if got := initHint(shell); !strings.Contains(got, want) {
			t.Errorf("initHint(%q) = %q, want it to name %q", shell, got, want)
		}
	}
}
