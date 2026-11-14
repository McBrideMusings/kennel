package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const vaultConfig = `# kennel config
[tools.claude]
base   = "~/.claude"   # the base
shared = []

[tools.codex]
base   = "~/.codex"
shared = []

[profiles.work]
claude = "~/.claude-work"
`

// vaultWithSkill makes a sandbox with a config and a vault holding the skill
// recomp, added from ~/src/recomp, and returns HOME and the vault's item path.
func vaultWithSkill(t *testing.T) (string, string) {
	t.Helper()
	h := vaultSandbox(t)
	os.MkdirAll(filepath.Join(h, ".cfg"), 0o755)
	os.WriteFile(filepath.Join(h, ".cfg", "config.toml"), []byte(vaultConfig), 0o644)
	src := filepath.Join(h, "src", "recomp")
	os.MkdirAll(filepath.Join(src, "refs"), 0o755)
	os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("v1\n"), 0o644)
	os.WriteFile(filepath.Join(src, "refs", "a.md"), []byte("ref\n"), 0o644)
	if out, code := vaultRun(t, "add", src); code != 0 {
		t.Fatalf("vault add exited %d: %s", code, out)
	}
	return h, filepath.Join(h, ".local", "share", "kennel", "vault", "skills", "recomp")
}

// install links the skill into each harness's global skills and a project's;
// rm refuses while any link stands, the add's own included, and removes the
// item with one commit once they are gone.
func TestVaultInstallRm(t *testing.T) {
	h, item := vaultWithSkill(t)
	proj := filepath.Join(h, "proj")
	os.MkdirAll(proj, 0o755)

	for _, c := range []struct {
		args []string
		link string
	}{
		{[]string{"recomp", "claude"}, filepath.Join(h, ".claude", "skills", "recomp")},
		{[]string{"recomp", "codex"}, filepath.Join(h, ".codex", "skills", "recomp")},
		{[]string{"recomp", "claude", "--project", proj}, filepath.Join(proj, ".claude", "skills", "recomp")},
		{[]string{"recomp", "pi", "--project", proj}, filepath.Join(proj, ".pi", "skills", "recomp")},
	} {
		if out, code := vaultRun(t, append([]string{"install"}, c.args...)...); code != 0 {
			t.Fatalf("install %v exited %d: %s", c.args, code, out)
		}
		if got, _ := os.Readlink(c.link); got != item {
			t.Fatalf("install %v: %s links to %q, want %q", c.args, c.link, got, item)
		}
	}
	// Again is a no-op; a real folder in the way is refused and kept.
	if _, code := vaultRun(t, "install", "recomp", "claude"); code != 0 {
		t.Fatal("a second install of the same link did not exit 0")
	}
	blocked := filepath.Join(proj, ".agents", "skills", "recomp")
	os.MkdirAll(blocked, 0o755)
	if _, code := vaultRun(t, "install", "recomp", "codex", "--project", proj); code != 1 {
		t.Fatal("install over a real folder did not exit 1")
	}
	if fi, err := os.Lstat(blocked); err != nil || !fi.IsDir() {
		t.Fatal("install over a real folder changed it")
	}

	// A link in a profile's home is found by the scan, not the record.
	os.MkdirAll(filepath.Join(h, ".claude-work", "skills"), 0o755)
	os.Symlink(item, filepath.Join(h, ".claude-work", "skills", "recomp"))

	links := []string{
		filepath.Join(h, "src", "recomp"),
		filepath.Join(h, ".claude", "skills", "recomp"),
		filepath.Join(h, ".claude-work", "skills", "recomp"),
		filepath.Join(h, ".codex", "skills", "recomp"),
		filepath.Join(proj, ".claude", "skills", "recomp"),
		filepath.Join(proj, ".pi", "skills", "recomp"),
	}
	for i := range links {
		out, code := vaultRun(t, "rm", "recomp")
		if code != 1 {
			t.Fatalf("rm with %d link(s) left exited %d", len(links)-i, code)
		}
		if n := strings.Count(out, "linked "); n != len(links)-i {
			t.Fatalf("rm listed %d links, want %d: %s", n, len(links)-i, out)
		}
		if _, err := os.Stat(filepath.Join(item, "SKILL.md")); err != nil {
			t.Fatal("a refused rm removed the item")
		}
		os.Remove(links[i])
	}
	if out, code := vaultRun(t, "rm", "recomp"); code != 0 {
		t.Fatalf("rm with no links exited %d: %s", code, out)
	}
	if _, err := os.Lstat(item); err == nil {
		t.Fatal("rm left the item in the vault")
	}
	// The add put the original at ~/.Trash/recomp; rm's copy takes a timed name.
	if got, _ := filepath.Glob(filepath.Join(h, ".Trash", "recomp *", "SKILL.md")); len(got) != 1 {
		t.Fatalf("rm did not move the item to the Trash: %q", got)
	}
	if lines := vaultLogLines(t, "skills/recomp"); !strings.Contains(lines[0], "rm skills/recomp") {
		t.Fatalf("log after rm = %q", lines)
	}
}

// copy writes the item's files and one [copied] line, every other line of
// config.toml unchanged; update after a vault commit rewrites the copy and the
// sha, and refuses a copy with edits of its own.
func TestVaultCopyUpdate(t *testing.T) {
	h, item := vaultWithSkill(t)
	cfgPath := filepath.Join(h, ".cfg", "config.toml")
	dest := filepath.Join(h, "proj", ".claude", "skills")
	os.MkdirAll(dest, 0o755)

	if out, code := vaultRun(t, "copy", "recomp", dest); code != 0 {
		t.Fatalf("copy exited %d: %s", code, out)
	}
	cp := filepath.Join(dest, "recomp")
	if b, _ := os.ReadFile(filepath.Join(cp, "refs", "a.md")); string(b) != "ref\n" {
		t.Fatalf("copy's refs/a.md = %q", b)
	}
	if fi, _ := os.Lstat(cp); fi == nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("copy made a link, not files")
	}
	sha1 := strings.Fields(vaultLogLines(t, "recomp")[0])[0]
	b, _ := os.ReadFile(cfgPath)
	want := vaultConfig + "\n[copied]\n\"~/proj/.claude/skills/recomp\" = \"vault:skills/recomp@" + sha1
	if !strings.HasPrefix(string(b), want) {
		t.Fatalf("config after copy =\n%s\nwant it to start\n%s", b, want)
	}
	if _, code := vaultRun(t, "copy", "recomp", dest); code != 1 {
		t.Fatal("copy over an existing copy did not exit 1")
	}

	// Nothing new in the vault: up to date.
	if out, code := vaultRun(t, "update", cp); code != 0 || !strings.Contains(out, "Up to date") {
		t.Fatalf("update with no new commit = %q (exit %d)", out, code)
	}

	os.WriteFile(filepath.Join(item, "SKILL.md"), []byte("v2\n"), 0o644)
	os.WriteFile(filepath.Join(item, "refs", "b.md"), []byte("new\n"), 0o644)
	os.Remove(filepath.Join(item, "refs", "a.md"))
	if out, code := vaultRun(t, "update", cp); code != 0 {
		t.Fatalf("update exited %d: %s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(cp, "SKILL.md")); string(b) != "v2\n" {
		t.Fatalf("SKILL.md after update = %q", b)
	}
	if exists(filepath.Join(cp, "refs", "a.md")) || !exists(filepath.Join(cp, "refs", "b.md")) {
		t.Fatal("update did not replace the copy's files")
	}
	sha2 := strings.Fields(vaultLogLines(t, "recomp")[0])[0]
	b, _ = os.ReadFile(cfgPath)
	if !strings.Contains(string(b), "@"+sha2) || strings.Contains(string(b), "@"+sha1) || !strings.HasPrefix(string(b), vaultConfig) {
		t.Fatalf("config after update =\n%s", b)
	}
	if strings.Count(string(b), "[copied]") != 1 || strings.Count(string(b), "vault:") != 1 {
		t.Fatalf("update added a line instead of editing one:\n%s", b)
	}

	// The project edits its copy; the next vault change does not overwrite it.
	os.WriteFile(filepath.Join(cp, "SKILL.md"), []byte("local\n"), 0o644)
	os.WriteFile(filepath.Join(item, "SKILL.md"), []byte("v3\n"), 0o644)
	out, code := vaultRun(t, "update", cp)
	if code != 1 || !strings.Contains(out, "SKILL.md") {
		t.Fatalf("update over a local edit = %q (exit %d)", out, code)
	}
	if b, _ := os.ReadFile(filepath.Join(cp, "SKILL.md")); string(b) != "local\n" {
		t.Fatal("a refused update changed the copy")
	}
	if _, code := vaultRun(t, "update", filepath.Join(h, "proj")); code != 1 {
		t.Fatal("update of a path with no [copied] entry did not exit 1")
	}
}

func TestSetCopied(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"new table", "a = 1", "a = 1\n\n[copied]\n\"~/x\" = \"v\"\n"},
		{"empty file", "", "[copied]\n\"~/x\" = \"v\"\n"},
		{"after last entry", "[copied]\n\"~/y\" = \"w\"\n\n[hooks]\n", "[copied]\n\"~/y\" = \"w\"\n\"~/x\" = \"v\"\n\n[hooks]\n"},
		{"replace keeps key and comment", "[copied]\n  '/H/x' = \"old\" # mine\n\"~/y\" = \"w\"\n", "[copied]\n  '/H/x' = \"v\" # mine\n\"~/y\" = \"w\"\n"},
		{"crlf kept", "[copied]\r\n\"~/x\" = \"old\"\r\n\"~/y\" = \"w\"\r\n", "[copied]\r\n\"~/x\" = \"v\"\r\n\"~/y\" = \"w\"\r\n"},
		{"crlf new table", "a = 1\r\n", "a = 1\r\n\r\n[copied]\r\n\"~/x\" = \"v\"\r\n"},
		{"table at end without newline", "[copied]\n\"~/y\" = \"w\"", "[copied]\n\"~/y\" = \"w\"\n\"~/x\" = \"v\""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", "/H")
			got, err := setCopied([]byte(c.src), "~/x", "v")
			if err != nil || string(got) != c.want {
				t.Fatalf("got %q, %v\nwant %q", got, err, c.want)
			}
		})
	}
	for _, bad := range []string{"[copied]\nnot a line\n", "copied.\"~/y\" = \"w\"\n", "copied = { \"~/y\" = \"w\" }\n", "[copied.\"~/y\"]\n"} {
		if _, err := setCopied([]byte(bad), "~/x", "v"); err == nil {
			t.Fatalf("setCopied on %q was not an error", bad)
		}
	}
}

// A snippet copies and updates as one file; install refuses it. A copy whose
// only change is its executable bit counts as edited. A crash's leftover temp
// folder does not block the next update.
func TestVaultCopySnippetAndLeftovers(t *testing.T) {
	h, item := vaultWithSkill(t)
	snip := filepath.Join(h, "notes", "tone.md")
	os.MkdirAll(filepath.Dir(snip), 0o755)
	os.WriteFile(snip, []byte("a\n"), 0o644)
	if _, code := vaultRun(t, "add", snip); code != 0 {
		t.Fatal("add of a snippet failed")
	}
	if _, code := vaultRun(t, "install", "tone", "claude"); code != 1 {
		t.Fatal("install of a snippet did not exit 1")
	}
	proj := filepath.Join(h, "proj")
	os.MkdirAll(proj, 0o755)
	if out, code := vaultRun(t, "copy", "tone", proj); code != 0 {
		t.Fatalf("copy of a snippet exited %d: %s", code, out)
	}
	cp := filepath.Join(proj, "tone.md")
	os.WriteFile(snip, []byte("b\n"), 0o644) // through the add's link
	if out, code := vaultRun(t, "update", cp); code != 0 {
		t.Fatalf("update of a snippet exited %d: %s", code, out)
	}
	if b, _ := os.ReadFile(cp); string(b) != "b\n" {
		t.Fatalf("snippet copy after update = %q", b)
	}

	skill := filepath.Join(proj, "recomp")
	if _, code := vaultRun(t, "copy", "recomp", proj); code != 0 {
		t.Fatal("copy of the skill failed")
	}
	os.Chmod(filepath.Join(skill, "SKILL.md"), 0o755)
	os.WriteFile(filepath.Join(item, "SKILL.md"), []byte("v2\n"), 0o644)
	if _, code := vaultRun(t, "update", skill); code != 1 {
		t.Fatal("update over a changed executable bit did not exit 1")
	}
	os.Chmod(filepath.Join(skill, "SKILL.md"), 0o644)
	os.MkdirAll(filepath.Join(proj, ".recomp.kennel-old", "x"), 0o755)
	if out, code := vaultRun(t, "update", skill); code != 0 {
		t.Fatalf("update with a leftover .kennel-old exited %d: %s", code, out)
	}
	if exists(filepath.Join(proj, ".recomp.kennel-old")) || exists(filepath.Join(proj, ".recomp.kennel-new")) {
		t.Fatal("update left a temp folder")
	}
}

// copy, update and unlink with a config kennel cannot write leave the path as
// it was: no copy, the copy's old files, or the link, and no temp folder.
func TestVaultPlaceCopyUnwritableConfig(t *testing.T) {
	h, item := vaultWithSkill(t)
	cfgDir, proj := filepath.Join(h, ".cfg"), filepath.Join(h, "proj")
	os.MkdirAll(proj, 0o755)
	cp := filepath.Join(proj, "recomp")
	noTemps := func(when string) {
		t.Helper()
		if exists(filepath.Join(proj, ".recomp.kennel-new")) || exists(filepath.Join(proj, ".recomp.kennel-old")) {
			t.Fatalf("%s left a temp folder", when)
		}
	}

	os.Chmod(cfgDir, 0o555)
	_, code := vaultRun(t, "copy", "recomp", proj)
	os.Chmod(cfgDir, 0o755)
	if code != 1 || exists(cp) {
		t.Fatalf("copy with an unwritable config exited %d, copy left: %v", code, exists(cp))
	}
	noTemps("a failed copy")

	if out, code := vaultRun(t, "copy", "recomp", proj); code != 0 {
		t.Fatalf("copy exited %d: %s", code, out)
	}
	before, _ := os.ReadFile(filepath.Join(cfgDir, "config.toml"))
	os.WriteFile(filepath.Join(item, "SKILL.md"), []byte("v2\n"), 0o644)
	os.Chmod(cfgDir, 0o555)
	_, code = vaultRun(t, "update", cp)
	os.Chmod(cfgDir, 0o755)
	if code != 1 {
		t.Fatal("update with an unwritable config did not exit 1")
	}
	if b, _ := os.ReadFile(filepath.Join(cp, "SKILL.md")); string(b) != "v1\n" {
		t.Fatalf("after a failed update SKILL.md = %q", b)
	}
	if after, _ := os.ReadFile(filepath.Join(cfgDir, "config.toml")); string(after) != string(before) {
		t.Fatalf("a failed update changed config.toml:\n%s", after)
	}
	noTemps("a failed update")

	if out, code := vaultRun(t, "install", "recomp", "claude"); code != 0 {
		t.Fatalf("install exited %d: %s", code, out)
	}
	link := filepath.Join(h, ".claude", "skills", "recomp")
	os.Chmod(cfgDir, 0o555)
	_, code = vaultRun(t, "unlink", link)
	os.Chmod(cfgDir, 0o755)
	if got, _ := os.Readlink(link); code != 1 || got != item {
		t.Fatalf("unlink with an unwritable config exited %d, link now %q", code, got)
	}
	for _, tmp := range []string{".recomp.kennel-new", ".recomp.kennel-old"} {
		if exists(filepath.Join(h, ".claude", "skills", tmp)) {
			t.Fatalf("a failed unlink left %s", tmp)
		}
	}
}
