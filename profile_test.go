package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const profileTools = "[tools.codex]\nbase = \"~/cbase\"\nshared = [\"a\", \"b\"]\n"

// profileSandbox loads profileTools plus the given [profiles] text, with
// codex's base holding a folder a and a file b.
func profileSandbox(t *testing.T, profiles string) (string, *Config, error) {
	t.Helper()
	h, cfg, err := shareSandbox(t, profileTools+profiles)
	os.MkdirAll(filepath.Join(h, "cbase", "a"), 0o755)
	os.WriteFile(filepath.Join(h, "cbase", "b"), []byte("base b"), 0o644)
	return h, cfg, err
}

// Cases 1 and 2: a profile's own list replaces the tool's, so its home keeps
// its own b; a string-form profile still links the tool's whole list.
func TestProfileOwnSharedList(t *testing.T) {
	h, cfg, err := profileSandbox(t, "[profiles.work]\ncodex = { home = \"~/.codex-work\", shared = [\"a\"] }\n[profiles.home]\ncodex = \"~/.codex-home\"\n")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(h, ".codex-work"), 0o755)
	os.WriteFile(filepath.Join(h, ".codex-work", "b"), []byte("work b"), 0o644)
	if out, code := quiet(t, func() int { return cmdLink(cfg, "") }); code != 0 {
		t.Fatalf("link exited %d:\n%s", code, out)
	}
	if got, _ := os.Readlink(filepath.Join(h, ".codex-work", "a")); got != filepath.Join(h, "cbase", "a") {
		t.Fatalf("work a links to %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(h, ".codex-work", "b")); string(b) != "work b" {
		t.Fatalf("work's own b became %q", b)
	}
	for _, item := range []string{"a", "b"} {
		if got, _ := os.Readlink(filepath.Join(h, ".codex-home", item)); got != filepath.Join(h, "cbase", item) {
			t.Fatalf("string-form home %s links to %q", item, got)
		}
	}
}

// Case 3: a table missing home or shared, or a shared item with a /, is an
// error naming the profile and tool.
func TestProfileTableErrors(t *testing.T) {
	for _, c := range []struct{ toml, want string }{
		{"codex = { home = \"~/.codex-work\" }", "[profiles.work] codex needs both `home`"},
		{"codex = { shared = [\"a\"] }", "[profiles.work] codex needs both `home`"},
		{"codex = { home = \"~/.codex-work\", shared = [\"a/b\"] }", `[profiles.work] codex shared item "a/b" must be one name`},
		{"codex = { home = \"~/.codex-work\", shared = [], extra = 1 }", `[profiles.work] codex has unknown key "extra"`},
		{"codex = 3", "[profiles.work] codex must be a home path"},
		{"codex = { home = \"~/.codex-work\", shared = \"a\" }", "[profiles.work] codex shared must be an array"},
	} {
		_, _, err := profileSandbox(t, "[profiles.work]\n"+c.toml+"\n")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.toml, err, c.want)
		}
	}
}

// Case 4: an item the profile stopped sharing still links to the base. link
// reports it and exits 1 without touching it; doctor FAILs it.
func TestProfileUnsharedLinkReported(t *testing.T) {
	h, cfg, err := profileSandbox(t, "[profiles.work]\ncodex = \"~/.codex-work\"\n")
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, func() int { return cmdLink(cfg, "") })
	body, _ := os.ReadFile(configPath())
	os.WriteFile(configPath(), []byte(strings.Replace(string(body), `codex = "~/.codex-work"`, `codex = { home = "~/.codex-work", shared = ["a"] }`, 1)), 0o644)
	cfg = reload(t)
	b := filepath.Join(h, ".codex-work", "b")
	want := "unshared  not shared by [profiles.work]: ~/.codex-work/b → ~/cbase/b"
	out, code := quiet(t, func() int { return cmdLink(cfg, "") })
	if code != 1 || !strings.Contains(out, want) {
		t.Fatalf("link exited %d:\n%s", code, out)
	}
	if got, _ := os.Readlink(b); got != filepath.Join(h, "cbase", "b") {
		t.Fatalf("link touched the unshared link: %q", got)
	}
	out, code = quiet(t, func() int { return cmdDoctor(cfg) })
	if code != 1 || !strings.Contains(out, "FAIL  work/codex "+want) {
		t.Fatalf("doctor exited %d:\n%s", code, out)
	}
}

// A [shares] link into a profile home can point at the base item of the
// same name; it is the share's, so link must not call it unshared.
func TestProfileShareLinkIsNotUnshared(t *testing.T) {
	h, cfg, err := profileSandbox(t, "[profiles.work]\ncodex = { home = \"~/.codex-work\", shared = [\"a\"] }\n[shares.x]\nsource = \"~/cbase/b\"\nlinks = [\"~/.codex-work/b\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, func() int { return cmdShare(cfg, "apply", nil) })
	if got, _ := os.Readlink(filepath.Join(h, ".codex-work", "b")); got != filepath.Join(h, "cbase", "b") {
		t.Fatalf("share did not link b: %q", got)
	}
	if out, code := quiet(t, func() int { return cmdLink(cfg, "") }); code != 0 || strings.Contains(out, "unshared") {
		t.Fatalf("link exited %d:\n%s", code, out)
	}
}

// Case 5: kennel env reads only homes, so the table form prints the same
// lines as the string form.
func TestProfileTableEnvUnchanged(t *testing.T) {
	h, cfg, err := profileSandbox(t, "[profiles.work]\ncodex = \"~/.codex-work\"\n")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(routesPath(), []byte("~/Work work\n"), 0o644)
	os.MkdirAll(filepath.Join(h, "Work"), 0o755)
	t.Chdir(filepath.Join(h, "Work"))
	want, _ := quiet(t, func() int { return cmdEnv(cfg, false) })
	body, _ := os.ReadFile(configPath())
	os.WriteFile(configPath(), []byte(strings.Replace(string(body), `codex = "~/.codex-work"`, `codex = { home = "~/.codex-work", shared = [] }`, 1)), 0o644)
	got, code := quiet(t, func() int { return cmdEnv(reload(t), false) })
	if code != 0 || got != want || !strings.Contains(got, "CODEX_HOME=") {
		t.Fatalf("env with the table form printed %q (exit %d), want %q", got, code, want)
	}
}
