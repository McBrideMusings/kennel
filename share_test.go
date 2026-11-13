package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fileShare = "[shares.i]\nsource = \"~/src/AGENTS.md\"\nlinks = [\"~/a/AGENTS.md\", \"~/b/AGENTS.md\"]\n"

func shareSandbox(t *testing.T, toml string) (string, *Config, error) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("KENNEL_CONFIG_DIR", filepath.Join(h, ".cfg"))
	os.MkdirAll(configDir(), 0o755)
	body := "[tools.claude]\nbase = \"~/.claude\"\nshared = []\n" + toml
	os.WriteFile(configPath(), []byte(body), 0o644)
	cfg, err := loadConfig()
	return h, cfg, err
}

// A share that moves or deletes a real file loses the user's data: apply must
// leave it byte-identical and exit 1, and undo must remove only links to the
// source.
func TestShareApplyAndUndo(t *testing.T) {
	h, cfg, err := shareSandbox(t, fileShare)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(h, "src"), 0o755)
	os.WriteFile(filepath.Join(h, "src", "AGENTS.md"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(h, "b"), 0o755)
	os.WriteFile(filepath.Join(h, "b", "AGENTS.md"), []byte("mine"), 0o644)

	if got := cmdShare(cfg, "apply", nil); got != 1 {
		t.Fatalf("apply with a blocked link exited %d, want 1", got)
	}
	if b, _ := os.ReadFile(filepath.Join(h, "b", "AGENTS.md")); string(b) != "mine" {
		t.Fatalf("blocking file changed to %q", b)
	}
	if got, _ := os.Readlink(filepath.Join(h, "a", "AGENTS.md")); got != filepath.Join(h, "src", "AGENTS.md") {
		t.Fatalf("link a points at %q", got)
	}
	cmdShare(cfg, "undo", nil)
	if _, err := os.Lstat(filepath.Join(h, "a", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("undo left the link it made")
	}
	if b, _ := os.ReadFile(filepath.Join(h, "b", "AGENTS.md")); string(b) != "mine" {
		t.Fatal("undo touched a real file")
	}
}

func TestShareUndoLeavesForeignSymlink(t *testing.T) {
	h, cfg, err := shareSandbox(t, fileShare)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(h, "a"), 0o755)
	os.Symlink("/elsewhere", filepath.Join(h, "a", "AGENTS.md"))
	cmdShare(cfg, "undo", nil)
	if got, _ := os.Readlink(filepath.Join(h, "a", "AGENTS.md")); got != "/elsewhere" {
		t.Fatalf("undo changed a foreign link to %q", got)
	}
}

const folderShare = "[shares.s]\nsource = \"~/src/skills\"\ninto = \"~/dst\"\nitems = [\"keep\"]\n"

// orphanSandbox links keep (listed), gone (an absolute link into the source,
// no longer listed), rel (a relative link into the source, not listed), a
// real file, a link to elsewhere, and a link to the source folder itself.
func orphanSandbox(t *testing.T) (string, *Config) {
	t.Helper()
	h, cfg, err := shareSandbox(t, folderShare)
	if err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(h, "src", "skills"), filepath.Join(h, "dst")
	for _, d := range []string{"keep", "gone", "rel"} {
		os.MkdirAll(filepath.Join(src, d), 0o755)
	}
	os.MkdirAll(dst, 0o755)
	os.Symlink(filepath.Join(src, "keep"), filepath.Join(dst, "keep"))
	os.Symlink(filepath.Join(src, "gone"), filepath.Join(dst, "gone"))
	os.Symlink("../src/skills/rel", filepath.Join(dst, "rel"))
	os.WriteFile(filepath.Join(dst, "real"), []byte("mine"), 0o644)
	os.Symlink("/elsewhere", filepath.Join(dst, "foreign"))
	os.Symlink(src, filepath.Join(dst, "whole"))
	return h, cfg
}

// An item dropped from items leaves its link behind; if plan and doctor
// cannot see it, nothing ever removes it.
func TestOrphanReportedByPlanAndDoctor(t *testing.T) {
	_, cfg := orphanSandbox(t)
	out, _ := quiet(t, func() int { return cmdShare(cfg, "plan", nil) })
	for _, name := range []string{"dst/gone", "dst/rel"} {
		if !strings.Contains(out, "orphan    ~/"+name) {
			t.Errorf("plan does not show %s as orphan:\n%s", name, out)
		}
	}
	for _, name := range []string{"keep", "real", "foreign", "whole"} {
		if strings.Contains(out, "orphan    ~/dst/"+name+" ") {
			t.Errorf("plan calls %s an orphan:\n%s", name, out)
		}
	}
	doc, code := quiet(t, func() int { return cmdDoctor(cfg) })
	if code != 1 || !strings.Contains(doc, "FAIL  share s orphan    ~/dst/gone") {
		t.Fatalf("doctor exit %d, want 1 with a FAIL orphan line:\n%s", code, doc)
	}
}

func TestPruneRemovesOnlyOrphans(t *testing.T) {
	h, cfg := orphanSandbox(t)
	dst := filepath.Join(h, "dst")
	out, code := quiet(t, func() int { return cmdShare(cfg, "prune", nil) })
	if code != 0 {
		t.Fatalf("prune exited %d:\n%s", code, out)
	}
	for _, name := range []string{"gone", "rel"} {
		if _, err := os.Lstat(filepath.Join(dst, name)); !os.IsNotExist(err) {
			t.Errorf("prune left orphan %s", name)
		}
		if !strings.Contains(out, "pruned    ~/dst/"+name) {
			t.Errorf("prune did not print %s:\n%s", name, out)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "real")); string(b) != "mine" {
		t.Error("prune touched a real file")
	}
	if got, _ := os.Readlink(filepath.Join(dst, "foreign")); got != "/elsewhere" {
		t.Error("prune touched a foreign link")
	}
	for _, name := range []string{"keep", "whole"} {
		if _, err := os.Lstat(filepath.Join(dst, name)); err != nil {
			t.Errorf("prune removed %s", name)
		}
	}
	log, _ := os.ReadFile(filepath.Join(h, ".local", "state", "kennel", "kennel.log"))
	if !strings.Contains(string(log), "share: pruned "+filepath.Join(dst, "gone")+" -> "+filepath.Join(h, "src", "skills", "gone")) {
		t.Errorf("log lacks the prune line:\n%s", log)
	}
	out, code = quiet(t, func() int { return cmdShare(cfg, "prune", nil) })
	if code != 0 || !strings.Contains(out, "no orphaned links") {
		t.Fatalf("second prune exit %d, output %q", code, out)
	}
}

func TestUndoRemovesOrphansToo(t *testing.T) {
	h, cfg := orphanSandbox(t)
	dst := filepath.Join(h, "dst")
	quiet(t, func() int { return cmdShare(cfg, "undo", nil) })
	for _, name := range []string{"keep", "gone", "rel"} {
		if _, err := os.Lstat(filepath.Join(dst, name)); !os.IsNotExist(err) {
			t.Errorf("undo left %s", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(dst, "foreign")); err != nil {
		t.Error("undo removed a foreign link")
	}
}

// Two shares into one folder from one source: the name listed by either is
// not an orphan, and a name neither lists is reported and pruned once.
func TestOrphanSharedIntoCountedOnce(t *testing.T) {
	h, cfg, err := shareSandbox(t, folderShare+"[shares.t]\nsource = \"~/src/skills\"\ninto = \"~/dst\"\nitems = [\"other\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(h, "src", "skills"), filepath.Join(h, "dst")
	for _, d := range []string{"keep", "other", "gone"} {
		os.MkdirAll(filepath.Join(src, d), 0o755)
	}
	os.MkdirAll(dst, 0o755)
	for _, d := range []string{"keep", "other", "gone"} {
		os.Symlink(filepath.Join(src, d), filepath.Join(dst, d))
	}
	got := orphanChecks(cfg)
	if len(got) != 1 || got[0].path != filepath.Join(dst, "gone") {
		t.Fatalf("orphans = %+v, want only gone", got)
	}
}

func TestOrphanMissingIntoIsNone(t *testing.T) {
	_, cfg, err := shareSandbox(t, folderShare)
	if err != nil {
		t.Fatal(err)
	}
	if got := orphanChecks(cfg); len(got) != 0 {
		t.Fatalf("orphans with no into folder = %+v", got)
	}
}

const twoShares = fileShare + "[shares.s]\nsource = \"~/src/skills\"\ninto = \"~/dst\"\nitems = [\"one\", \"two\"]\n"

// scopedSandbox applies both shares: i links ~/a and ~/b AGENTS.md, s links
// ~/dst/one and ~/dst/two.
func scopedSandbox(t *testing.T) (string, *Config) {
	t.Helper()
	h, cfg, err := shareSandbox(t, twoShares)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(h, "src", "skills", "one"), 0o755)
	os.MkdirAll(filepath.Join(h, "src", "skills", "two"), 0o755)
	os.WriteFile(filepath.Join(h, "src", "AGENTS.md"), []byte("x"), 0o644)
	return h, cfg
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// Undo of one share must leave every other share's links; otherwise the
// command is still all-or-nothing.
func TestShareUndoOneShare(t *testing.T) {
	h, cfg := scopedSandbox(t)
	quiet(t, func() int { return cmdShare(cfg, "apply", nil) })
	if out, code := quiet(t, func() int { return cmdShare(cfg, "undo", []string{"s"}) }); code != 0 {
		t.Fatalf("undo s exited %d:\n%s", code, out)
	}
	for _, n := range []string{"dst/one", "dst/two"} {
		if exists(filepath.Join(h, n)) {
			t.Errorf("undo s left %s", n)
		}
	}
	for _, n := range []string{"a/AGENTS.md", "b/AGENTS.md"} {
		if !exists(filepath.Join(h, n)) {
			t.Errorf("undo s removed share i's %s", n)
		}
	}
}

func TestShareUndoOneItem(t *testing.T) {
	h, cfg := scopedSandbox(t)
	quiet(t, func() int { return cmdShare(cfg, "apply", nil) })
	quiet(t, func() int { return cmdShare(cfg, "undo", []string{"s", "one"}) })
	quiet(t, func() int { return cmdShare(cfg, "undo", []string{"i", filepath.Join(h, "a", "AGENTS.md")}) })
	for n, want := range map[string]bool{"dst/one": false, "dst/two": true, "a/AGENTS.md": false, "b/AGENTS.md": true} {
		if exists(filepath.Join(h, n)) != want {
			t.Errorf("%s exists = %v, want %v", n, !want, want)
		}
	}
}

func TestSharePlanAndApplyOneShare(t *testing.T) {
	h, cfg := scopedSandbox(t)
	out, _ := quiet(t, func() int { return cmdShare(cfg, "plan", []string{"s"}) })
	if !strings.Contains(out, "~/dst/one") || strings.Contains(out, "AGENTS.md") {
		t.Fatalf("plan s shows the wrong rows:\n%s", out)
	}
	quiet(t, func() int { return cmdShare(cfg, "apply", []string{"s"}) })
	if !exists(filepath.Join(h, "dst", "one")) || exists(filepath.Join(h, "a", "AGENTS.md")) {
		t.Fatal("apply s did not create only share s's links")
	}
}

// A typo must change nothing: if apply fell back to every share, a mistyped
// name would link everything.
func TestShareUnknownScopeChangesNothing(t *testing.T) {
	h, cfg := scopedSandbox(t)
	for _, args := range [][]string{{"nope"}, {"s", "nope"}, {"i", filepath.Join(h, "c", "AGENTS.md")}} {
		if _, code := quiet(t, func() int { return cmdShare(cfg, "apply", args) }); code != 1 {
			t.Errorf("apply %v exited %d, want 1", args, code)
		}
	}
	if exists(filepath.Join(h, "dst")) || exists(filepath.Join(h, "a")) {
		t.Fatal("an unknown scope created links")
	}
}

// An orphan's name counts as an item of its folder share, so one stale link
// can be undone by name; orphans of the share stay in scope of the share.
func TestShareScopeReachesOrphans(t *testing.T) {
	h, cfg := orphanSandbox(t)
	dst := filepath.Join(h, "dst")
	if _, code := quiet(t, func() int { return cmdShare(cfg, "undo", []string{"s", "gone"}) }); code != 0 {
		t.Fatal("undo of an orphan by name failed")
	}
	if exists(filepath.Join(dst, "gone")) || !exists(filepath.Join(dst, "rel")) || !exists(filepath.Join(dst, "keep")) {
		t.Fatal("undo s gone removed the wrong links")
	}
	quiet(t, func() int { return cmdShare(cfg, "prune", []string{"s"}) })
	if exists(filepath.Join(dst, "rel")) || !exists(filepath.Join(dst, "keep")) {
		t.Fatal("prune s did not remove only the orphan")
	}
}

// Two shares into one folder: scoping to one must not prune or undo the
// other's orphan, which points into the other share's source.
func TestShareScopeLeavesOtherSharesOrphan(t *testing.T) {
	h, cfg, err := shareSandbox(t, folderShare+"[shares.t]\nsource = \"~/src/other\"\ninto = \"~/dst\"\nitems = [\"x\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(h, "src", "other", "old"), 0o755)
	os.MkdirAll(filepath.Join(h, "dst"), 0o755)
	os.Symlink(filepath.Join(h, "src", "other", "old"), filepath.Join(h, "dst", "old"))
	quiet(t, func() int { return cmdShare(cfg, "prune", []string{"s"}) })
	quiet(t, func() int { return cmdShare(cfg, "undo", []string{"s"}) })
	if !exists(filepath.Join(h, "dst", "old")) {
		t.Fatal("scoping to share s removed share t's orphan")
	}
	quiet(t, func() int { return cmdShare(cfg, "prune", []string{"t"}) })
	if exists(filepath.Join(h, "dst", "old")) {
		t.Fatal("prune t left its own orphan")
	}
}
