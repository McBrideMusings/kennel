package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkSandbox configures profile work with two homes: ~/.codex-work, sharing
// a, b, c and d from codex's base ~/cbase, and ~/.claude-work, sharing nothing
// from claude's base.
func linkSandbox(t *testing.T) (string, *Config) {
	t.Helper()
	h, cfg, err := shareSandbox(t, "[tools.codex]\nbase = \"~/cbase\"\nshared = [\"a\", \"b\", \"c\", \"d\"]\n[profiles.work]\ncodex = \"~/.codex-work\"\nclaude = \"~/.claude-work\"\n")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(h, "cbase", "a"), 0o755)
	os.WriteFile(filepath.Join(h, "cbase", "b"), []byte("base b"), 0o644)
	os.WriteFile(filepath.Join(h, "cbase", "c"), []byte("base c"), 0o644)
	return h, cfg
}

// A first run on a fresh profile creates each home, the one with no shared
// items included, and links every item the base has; a second run finds
// nothing to do.
func TestLinkCreatesHomeAndLinks(t *testing.T) {
	h, cfg := linkSandbox(t)
	os.WriteFile(filepath.Join(h, "cbase", "d"), []byte("base d"), 0o644)
	home := filepath.Join(h, ".codex-work")

	out, code := quiet(t, func() int { return cmdLink(cfg, "") })
	if code != 0 {
		t.Fatalf("link exited %d:\n%s", code, out)
	}
	if !isDir(home) {
		t.Fatalf("profile home %s was not created", home)
	}
	if !isDir(filepath.Join(h, ".claude-work")) {
		t.Fatal("profile home ~/.claude-work, which shares nothing, was not created")
	}
	for _, want := range []string{"created   ~/.codex-work", "created   ~/.claude-work"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	for _, item := range []string{"a", "b", "c", "d"} {
		want := filepath.Join(h, "cbase", item)
		if got, err := os.Readlink(filepath.Join(home, item)); err != nil || got != want {
			t.Fatalf("%s links to %q (%v), want %s", item, got, err, want)
		}
	}

	out, code = quiet(t, func() int { return cmdLink(cfg, "") })
	if code != 0 || out != "" {
		t.Fatalf("second run exited %d with output:\n%s", code, out)
	}
}

// Each state link cannot fix is reported by name and exits 1, the missing
// item beside them is still linked, and nothing already at a path changes.
func TestLinkReportsBlockedWrongAndNoSource(t *testing.T) {
	h, cfg := linkSandbox(t)
	home := filepath.Join(h, ".codex-work")
	os.MkdirAll(home, 0o755)
	blocked := filepath.Join(home, "b")
	os.WriteFile(blocked, []byte("my own b"), 0o600)
	os.Symlink("/elsewhere/c", filepath.Join(home, "c"))
	before, _ := os.Stat(blocked)

	out, code := quiet(t, func() int { return cmdLink(cfg, "") })
	if code != 1 {
		t.Fatalf("link exited %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{
		"linked    ~/.codex-work/a → ~/cbase/a",
		"blocked   ~/.codex-work/b is a real file or directory, not a link to ~/cbase/b",
		"wrong     ~/.codex-work/c → /elsewhere/c (should link to ~/cbase/c)",
		"no source ~/cbase/d does not exist, so ~/.codex-work/d has nothing to link to",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	if b, _ := os.ReadFile(blocked); string(b) != "my own b" {
		t.Fatalf("blocked file now holds %q", b)
	}
	if after, _ := os.Lstat(blocked); after.Mode() != before.Mode() {
		t.Fatalf("blocked file mode went from %v to %v", before.Mode(), after.Mode())
	}
	if got, _ := os.Readlink(filepath.Join(home, "c")); got != "/elsewhere/c" {
		t.Fatalf("wrong link now points at %q", got)
	}
	if _, err := os.Lstat(filepath.Join(home, "d")); !os.IsNotExist(err) {
		t.Fatalf("no-source item d got something at its path: %v", err)
	}
	if got, _ := os.Readlink(filepath.Join(home, "a")); got != filepath.Join(h, "cbase", "a") {
		t.Fatalf("a links to %q", got)
	}
}

// Each of blocked, wrong and no-source alone is enough to exit 1.
func TestLinkExitsOneOnEachBadState(t *testing.T) {
	for name, setup := range map[string]func(h string){
		"blocked":   func(h string) { os.WriteFile(filepath.Join(h, ".codex-work", "b"), []byte("x"), 0o644) },
		"wrong":     func(h string) { os.Symlink("/elsewhere", filepath.Join(h, ".codex-work", "b")) },
		"no source": func(h string) { os.Remove(filepath.Join(h, "cbase", "b")) },
	} {
		t.Run(name, func(t *testing.T) {
			h, cfg := linkSandbox(t)
			os.WriteFile(filepath.Join(h, "cbase", "d"), []byte("base d"), 0o644)
			os.MkdirAll(filepath.Join(h, ".codex-work"), 0o755)
			setup(h)
			if out, code := quiet(t, func() int { return cmdLink(cfg, "") }); code != 1 {
				t.Fatalf("link exited %d, want 1:\n%s", code, out)
			}
		})
	}
}
