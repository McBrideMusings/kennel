package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vaultLsJSON(t *testing.T) vaultList {
	t.Helper()
	out, code := vaultRun(t, "ls", "--json")
	if code != 0 {
		t.Fatalf("vault ls exited %d: %s", code, out)
	}
	var l vaultList
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return l
}

// ls lists skills with their files and snippets by name, and a recorded copy
// turns behind once its item gets a newer commit.
func TestVaultLs(t *testing.T) {
	h, item := vaultWithSkill(t)
	snip := filepath.Join(h, "notes", "tone.md")
	os.MkdirAll(filepath.Dir(snip), 0o755)
	os.WriteFile(snip, []byte("a\n"), 0o644)
	vaultRun(t, "add", snip)
	dest := filepath.Join(h, "proj", ".claude", "skills")
	os.MkdirAll(dest, 0o755)
	vaultRun(t, "copy", "recomp", dest)

	l := vaultLsJSON(t)
	if len(l.Skills) != 1 || l.Skills[0].Item != "skills/recomp" || l.Skills[0].Path != item || strings.Join(l.Skills[0].Files, ",") != "SKILL.md,refs/a.md" {
		t.Fatalf("skills = %+v", l.Skills)
	}
	if len(l.Snippets) != 1 || l.Snippets[0].Name != "tone" || l.Snippets[0].Item != "snippets/tone.md" {
		t.Fatalf("snippets = %+v", l.Snippets)
	}
	sha1 := strings.Fields(vaultLogLines(t, "recomp")[0])[0]
	if len(l.Copies) != 1 || l.Copies[0].Path != filepath.Join(dest, "recomp") || !strings.HasPrefix(l.Copies[0].Rev, sha1) || l.Copies[0].Head != l.Copies[0].Rev || l.Copies[0].Behind {
		t.Fatalf("copies before a vault change = %+v", l.Copies)
	}

	os.WriteFile(filepath.Join(item, "SKILL.md"), []byte("v2\n"), 0o644)
	l = vaultLsJSON(t)
	sha2 := strings.Fields(vaultLogLines(t, "recomp")[0])[0]
	if c := l.Copies[0]; !c.Behind || !strings.HasPrefix(c.Head, sha2) || !strings.HasPrefix(c.Rev, sha1) {
		t.Fatalf("copy after a vault change = %+v", c)
	}
	if out, _ := vaultRun(t, "ls"); !strings.Contains(out, "kennel vault update ~/proj/.claude/skills/recomp") {
		t.Fatalf("vault ls text = %q", out)
	}
	vaultRun(t, "update", filepath.Join(dest, "recomp"))
	if c := vaultLsJSON(t).Copies[0]; c.Behind {
		t.Fatalf("copy after update = %+v", c)
	}
}

// unlink replaces a link into the vault with the item's files and records
// the copy; it refuses a folder that is not a link and a link elsewhere.
func TestVaultUnlink(t *testing.T) {
	h, item := vaultWithSkill(t)
	link := filepath.Join(h, ".claude", "skills", "recomp")
	if out, code := vaultRun(t, "install", "recomp", "claude"); code != 0 {
		t.Fatalf("install exited %d: %s", code, out)
	}
	// A config kennel cannot write leaves the link as it was.
	cfgDir := filepath.Join(h, ".cfg")
	os.Chmod(cfgDir, 0o555)
	_, code := vaultRun(t, "unlink", link)
	os.Chmod(cfgDir, 0o755)
	if code != 1 {
		t.Fatal("unlink with an unwritable config did not exit 1")
	}
	if got, _ := os.Readlink(link); got != item {
		t.Fatalf("after a failed unlink the link points at %q", got)
	}
	if out, code := vaultRun(t, "unlink", link); code != 0 {
		t.Fatalf("unlink exited %d: %s", code, out)
	}
	fi, _ := os.Lstat(link)
	if fi == nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		t.Fatal("unlink left no folder in the link's place")
	}
	if b, _ := os.ReadFile(filepath.Join(link, "refs", "a.md")); string(b) != "ref\n" {
		t.Fatalf("unlinked refs/a.md = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(item, "SKILL.md")); string(b) != "v1\n" {
		t.Fatal("unlink changed the vault item")
	}
	sha := strings.Fields(vaultLogLines(t, "recomp")[0])[0]
	b, _ := os.ReadFile(filepath.Join(h, ".cfg", "config.toml"))
	if !strings.Contains(string(b), "\"~/.claude/skills/recomp\" = \"vault:skills/recomp@"+sha) {
		t.Fatalf("config after unlink =\n%s", b)
	}
	if exists(filepath.Join(h, ".claude", "skills", ".recomp.kennel-new")) {
		t.Fatal("unlink left its temp folder")
	}
	if _, code := vaultRun(t, "unlink", link); code != 1 {
		t.Fatal("unlink of a folder that is not a link did not exit 1")
	}
	other := filepath.Join(h, ".claude", "skills", "other")
	os.Symlink(filepath.Join(h, "src"), other)
	if _, code := vaultRun(t, "unlink", other); code != 1 {
		t.Fatal("unlink of a link outside the vault did not exit 1")
	}
	if got, _ := os.Readlink(other); got != filepath.Join(h, "src") {
		t.Fatal("a refused unlink changed the link")
	}
}
