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

const editableShare = `# my shares
[shares.s]   # skills for every tool
source = "~/src/skills"
into   = "~/dst"
items  = [
  "keep",  # always
]
# end
`

func reload(t *testing.T) *Config {
	t.Helper()
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func readLog(t *testing.T, h string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(h, ".local", "state", "kennel", "kennel.log"))
	return string(b)
}

// Adding a skill by hand means editing config.toml and running apply; add and
// rm do both, and must keep every comment and line of the file they did not
// mean to change.
func TestShareAddAndRmItems(t *testing.T) {
	h, cfg, err := shareSandbox(t, editableShare)
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := os.ReadFile(configPath())
	src, dst := filepath.Join(h, "src", "skills"), filepath.Join(h, "dst")
	for _, d := range []string{"keep", "a", "b"} {
		os.MkdirAll(filepath.Join(src, d), 0o755)
	}

	if out, code := quiet(t, func() int { return cmdShareEdit(cfg, "add", []string{"s", "a", "b"}) }); code != 0 {
		t.Fatalf("add exited %d:\n%s", code, out)
	}
	for _, d := range []string{"a", "b"} {
		if got, _ := os.Readlink(filepath.Join(dst, d)); got != filepath.Join(src, d) {
			t.Errorf("add left dst/%s pointing at %q", d, got)
		}
	}
	added := strings.Replace(string(orig), "  \"keep\",  # always\n", "  \"keep\",  # always\n  \"a\",\n  \"b\",\n", 1)
	if got, _ := os.ReadFile(configPath()); string(got) != added {
		t.Fatalf("config after add:\n%s\nwant:\n%s", got, added)
	}

	// b's link now points elsewhere: rm drops b from items, leaves the link,
	// reports it and exits 1.
	os.Remove(filepath.Join(dst, "b"))
	os.Symlink("/elsewhere", filepath.Join(dst, "b"))
	if out, code := quiet(t, func() int { return cmdShareEdit(reload(t), "rm", []string{"s", "a", "b"}) }); code != 1 || !strings.Contains(out, "wrong     ~/dst/b → /elsewhere") {
		t.Fatalf("rm over a foreign link exited %d, want 1 with a report:\n%s", code, out)
	}
	if _, err := os.Lstat(filepath.Join(dst, "a")); !os.IsNotExist(err) {
		t.Error("rm left dst/a")
	}
	if got, _ := os.Readlink(filepath.Join(dst, "b")); got != "/elsewhere" {
		t.Errorf("rm changed a foreign link to %q", got)
	}
	if got, _ := os.ReadFile(configPath()); string(got) != string(orig) {
		t.Fatalf("config after rm:\n%s\nwant:\n%s", got, orig)
	}

	log := readLog(t, h)
	for _, want := range []string{
		"share: " + filepath.Join(dst, "a") + " -> " + filepath.Join(src, "a"),
		"share: removed " + filepath.Join(dst, "a") + " -> ",
		"share: added a to [shares.s] items",
		"share: removed b from [shares.s] items",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("change log lacks %q:\n%s", want, log)
		}
	}
}

func TestShareAddAndRmLinks(t *testing.T) {
	h, cfg, err := shareSandbox(t, fileShare)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(h, "src", "AGENTS.md")
	os.MkdirAll(filepath.Dir(source), 0o755)
	os.WriteFile(source, []byte("x"), 0o644)
	if _, code := quiet(t, func() int { return cmdShare(cfg, "apply", nil) }); code != 0 {
		t.Fatal("apply failed")
	}

	if out, code := quiet(t, func() int { return cmdShareEdit(cfg, "add", []string{"i", "--link", "~/c/AGENTS.md"}) }); code != 0 {
		t.Fatalf("add --link exited %d:\n%s", code, out)
	}
	if got, _ := os.Readlink(filepath.Join(h, "c", "AGENTS.md")); got != source {
		t.Fatalf("add --link left c/AGENTS.md pointing at %q", got)
	}
	if got, _ := os.ReadFile(configPath()); !strings.Contains(string(got), `links = ["~/a/AGENTS.md", "~/b/AGENTS.md", "~/c/AGENTS.md"]`) {
		t.Fatalf("config after add --link:\n%s", got)
	}

	if out, code := quiet(t, func() int {
		return cmdShareEdit(reload(t), "rm", []string{"i", "--link", filepath.Join(h, "a", "AGENTS.md")})
	}); code != 0 {
		t.Fatalf("rm --link exited %d:\n%s", code, out)
	}
	if _, err := os.Lstat(filepath.Join(h, "a", "AGENTS.md")); !os.IsNotExist(err) {
		t.Error("rm --link left the link")
	}
	if got, _ := os.ReadFile(configPath()); !strings.Contains(string(got), `links = ["~/b/AGENTS.md", "~/c/AGENTS.md"]`) {
		t.Fatalf("config after rm --link:\n%s", got)
	}
}

// A real file where add wants a link stays byte-identical and the command
// exits 1; rm refuses to empty a share, which would leave a config that does
// not load.
func TestShareAddRmRefusals(t *testing.T) {
	h, cfg, err := shareSandbox(t, editableShare)
	if err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(h, "src", "skills"), filepath.Join(h, "dst")
	os.MkdirAll(filepath.Join(src, "a"), 0o755)
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "a"), []byte("mine"), 0o644)
	if _, code := quiet(t, func() int { return cmdShareEdit(cfg, "add", []string{"s", "a"}) }); code != 1 {
		t.Fatalf("add over a real file exited %d, want 1", code)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a")); string(b) != "mine" {
		t.Fatalf("add changed the real file to %q", b)
	}

	before, _ := os.ReadFile(configPath())
	for _, args := range [][]string{
		{"s", "keep", "a"},      // would empty items
		{"s", "nope"},           // not listed
		{"s", "--link", "~/x"},  // folder share takes no --link
		{"s", "missing-in-src"}, // add: no source to link to
	} {
		action := "rm"
		if args[1] == "missing-in-src" || args[1] == "--link" {
			action = "add"
		}
		if _, code := quiet(t, func() int { return cmdShareEdit(reload(t), action, args) }); code != 1 {
			t.Errorf("%s %v exited %d, want 1", action, args, code)
		}
	}
	if after, _ := os.ReadFile(configPath()); string(after) != string(before) {
		t.Fatalf("a refused command changed the config:\n%s", after)
	}
}

// A path two shares claim would follow whichever share sorts first and leave
// the other's undo unable to find it, so loadConfig names both and the path.
func TestShareOverlapRejected(t *testing.T) {
	for _, c := range []struct{ name, toml, want string }{
		{"two file shares", fileShare + "[shares.j]\nsource = \"~/src/CLAUDE.md\"\nlinks = [\"~/b/AGENTS.md\"]\n",
			"[shares.i] and [shares.j] both link ~/b/AGENTS.md"},
		{"two folder shares, one item", folderShare + "[shares.t]\nsource = \"~/other\"\ninto = \"~/dst\"\nitems = [\"keep\"]\n",
			"[shares.s] and [shares.t] both link ~/dst/keep"},
		{"file link on a folder item", folderShare + "[shares.f]\nsource = \"~/src/AGENTS.md\"\nlinks = [\"~/dst/keep/\"]\n",
			"[shares.f] and [shares.s] both link ~/dst/keep"},
		{"one share twice", "[shares.i]\nsource = \"~/src/AGENTS.md\"\nlinks = [\"~/a/AGENTS.md\", \"~/a/../a/AGENTS.md\"]\n",
			"[shares.i] lists ~/a/AGENTS.md twice"},
		{"one item twice", "[shares.s]\nsource = \"~/src/skills\"\ninto = \"~/dst\"\nitems = [\"keep\", \"keep\"]\n",
			"[shares.s] lists ~/dst/keep twice"},
		{"different items in one into", folderShare + "[shares.t]\nsource = \"~/other\"\ninto = \"~/dst\"\nitems = [\"other\"]\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := shareSandbox(t, c.toml)
			if c.want == "" {
				if err != nil {
					t.Fatalf("loadConfig rejected a valid config: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("loadConfig error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// share add onto a path another share already links puts the config back
// and exits 1, since the edited config no longer loads.
func TestShareAddOverlapRestoresConfig(t *testing.T) {
	h, cfg, err := shareSandbox(t, fileShare+"[shares.j]\nsource = \"~/src/CLAUDE.md\"\nlinks = [\"~/c/CLAUDE.md\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(h, "src"), 0o755)
	os.WriteFile(filepath.Join(h, "src", "CLAUDE.md"), []byte("x"), 0o644)
	before, _ := os.ReadFile(configPath())
	if out, code := quiet(t, func() int { return cmdShareEdit(cfg, "add", []string{"j", "--link", "~/a/AGENTS.md"}) }); code != 1 {
		t.Fatalf("add of a claimed path exited %d, want 1:\n%s", code, out)
	}
	if after, _ := os.ReadFile(configPath()); string(after) != string(before) {
		t.Fatalf("config changed:\n%s", after)
	}
	if exists(filepath.Join(h, "a", "AGENTS.md")) {
		t.Fatal("add made a link for a refused entry")
	}
}

const starShare = "[shares.s]\nsource = \"~/src/skills\"\ninto = \"~/dst\"\nitems = \"*\"\nexclude = [\"skip\"]\n"

// starSandbox fills source with a, b, skip (excluded) and .DS_Store.
func starSandbox(t *testing.T) (src, dst string, cfg *Config) {
	t.Helper()
	h, cfg, err := shareSandbox(t, starShare)
	if err != nil {
		t.Fatal(err)
	}
	src, dst = filepath.Join(h, "src", "skills"), filepath.Join(h, "dst")
	for _, d := range []string{"a", "b", "skip"} {
		os.MkdirAll(filepath.Join(src, d), 0o755)
	}
	os.WriteFile(filepath.Join(src, ".DS_Store"), nil, 0o644)
	return src, dst, cfg
}

// items = "*" exists so a new skill needs no config edit: apply links every
// entry but excludes and dotfiles, and links one added later on the next run.
func TestStarLinksEveryEntry(t *testing.T) {
	src, dst, cfg := starSandbox(t)
	if out, code := quiet(t, func() int { return cmdShare(cfg, "apply", nil) }); code != 0 {
		t.Fatalf("apply exited %d:\n%s", code, out)
	}
	for _, d := range []string{"a", "b"} {
		if got, _ := os.Readlink(filepath.Join(dst, d)); got != filepath.Join(src, d) {
			t.Errorf("dst/%s points at %q", d, got)
		}
	}
	for _, d := range []string{"skip", ".DS_Store"} {
		if _, err := os.Lstat(filepath.Join(dst, d)); !os.IsNotExist(err) {
			t.Errorf("apply linked %s", d)
		}
	}
	os.MkdirAll(filepath.Join(src, "c"), 0o755)
	if out, code := quiet(t, func() int { return cmdShare(cfg, "apply", nil) }); code != 0 || !strings.Contains(out, "linked    ~/dst/c") {
		t.Fatalf("second apply exited %d, want c linked:\n%s", code, out)
	}
}

// An entry deleted from source, or excluded after apply, leaves a link no
// share asks for: doctor must flag it and prune remove it, and nothing else.
func TestStarDanglingAndExcludedAreOrphans(t *testing.T) {
	src, dst, cfg := starSandbox(t)
	quiet(t, func() int { return cmdShare(cfg, "apply", nil) })
	os.RemoveAll(filepath.Join(src, "b"))
	os.Symlink(filepath.Join(src, "skip"), filepath.Join(dst, "skip"))

	doc, code := quiet(t, func() int { return cmdDoctor(cfg) })
	for _, d := range []string{"b", "skip"} {
		if !strings.Contains(doc, "FAIL  share s orphan    ~/dst/"+d) {
			t.Errorf("doctor does not flag dst/%s:\n%s", d, doc)
		}
	}
	if code != 1 {
		t.Errorf("doctor exited %d, want 1", code)
	}
	if out, code := quiet(t, func() int { return cmdShare(cfg, "prune", nil) }); code != 0 {
		t.Fatalf("prune exited %d:\n%s", code, out)
	}
	for _, d := range []string{"b", "skip"} {
		if _, err := os.Lstat(filepath.Join(dst, d)); !os.IsNotExist(err) {
			t.Errorf("prune left dst/%s", d)
		}
	}
	if got, _ := os.Readlink(filepath.Join(dst, "a")); got != filepath.Join(src, "a") {
		t.Errorf("prune touched dst/a: %q", got)
	}
}

// A source that is gone must not read as "every link is an orphan", or one
// prune during an unmounted volume removes them all.
func TestStarUnreadableSource(t *testing.T) {
	src, dst, cfg := starSandbox(t)
	quiet(t, func() int { return cmdShare(cfg, "apply", nil) })
	os.Rename(src, src+".away")
	out, _ := quiet(t, func() int { return cmdShare(cfg, "plan", nil) })
	if !strings.Contains(out, "no source ~/src/skills is not a readable folder, so ~/dst/* links nothing") || strings.Contains(out, "orphan") {
		t.Fatalf("plan with no source:\n%s", out)
	}
	if _, code := quiet(t, func() int { return cmdShare(cfg, "apply", nil) }); code != 1 {
		t.Errorf("apply with no source exited %d, want 1", code)
	}
	if _, err := os.Lstat(filepath.Join(dst, "*")); !os.IsNotExist(err) {
		t.Error("apply made a link named *")
	}
	quiet(t, func() int { return cmdShare(cfg, "prune", nil) })
	if _, err := os.Lstat(filepath.Join(dst, "a")); err != nil {
		t.Error("prune removed a link while source was gone")
	}
}

// share rm on a "*" share has no items entry to drop, so it excludes the
// entry; add takes it back out of exclude.
func TestStarAddAndRmEditExclude(t *testing.T) {
	h, cfg, err := shareSandbox(t, "[shares.s]\nsource = \"~/src/skills\"\ninto = \"~/dst\"\nitems = \"*\"  # all\n")
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := os.ReadFile(configPath())
	src, dst := filepath.Join(h, "src", "skills"), filepath.Join(h, "dst")
	for _, d := range []string{"a", "b"} {
		os.MkdirAll(filepath.Join(src, d), 0o755)
	}
	quiet(t, func() int { return cmdShare(cfg, "apply", nil) })

	if out, code := quiet(t, func() int { return cmdShareEdit(cfg, "rm", []string{"s", "a"}) }); code != 0 {
		t.Fatalf("rm exited %d:\n%s", code, out)
	}
	if _, err := os.Lstat(filepath.Join(dst, "a")); !os.IsNotExist(err) {
		t.Error("rm left dst/a")
	}
	want := strings.Replace(string(orig), "items = \"*\"  # all\n", "items = \"*\"  # all\nexclude = [\"a\"]\n", 1)
	if got, _ := os.ReadFile(configPath()); string(got) != want {
		t.Fatalf("config after rm:\n%s\nwant:\n%s", got, want)
	}
	quiet(t, func() int { return cmdShareEdit(reload(t), "rm", []string{"s", "b"}) })
	if got, _ := os.ReadFile(configPath()); !strings.Contains(string(got), `exclude = ["a", "b"]`) {
		t.Fatalf("second rm:\n%s", got)
	}

	if out, code := quiet(t, func() int { return cmdShareEdit(reload(t), "add", []string{"s", "a"}) }); code != 0 {
		t.Fatalf("add exited %d:\n%s", code, out)
	}
	if got, _ := os.Readlink(filepath.Join(dst, "a")); got != filepath.Join(src, "a") {
		t.Errorf("add left dst/a pointing at %q", got)
	}
	if got, _ := os.ReadFile(configPath()); !strings.Contains(string(got), `exclude = ["b"]`) {
		t.Fatalf("config after add:\n%s", got)
	}
	if out, code := quiet(t, func() int { return cmdShareEdit(reload(t), "rm", []string{"s", "nope"}) }); code != 1 {
		t.Fatalf("rm of an entry not in source exited %d:\n%s", code, out)
	}
	os.WriteFile(filepath.Join(src, ".hidden"), nil, 0o644)
	before, _ := os.ReadFile(configPath())
	if _, code := quiet(t, func() int { return cmdShareEdit(reload(t), "rm", []string{"s", ".hidden"}) }); code != 1 {
		t.Fatalf("rm of a dotfile exited %d, want 1", code)
	}
	if got, _ := os.ReadFile(configPath()); string(got) != string(before) {
		t.Fatalf("rm of a dotfile edited the config:\n%s", got)
	}
}

func TestStarConfigErrors(t *testing.T) {
	const star = "[shares.s]\nsource = \"~/src\"\ninto = \"~/dst\"\nitems = \"*\"\n"
	cases := []struct{ name, toml, want string }{
		{"exclude without star", folderShare + "exclude = [\"x\"]\n", "`exclude` needs `items = \"*\"`"},
		{"items a string", "[shares.s]\nsource = \"~/src\"\ninto = \"~/dst\"\nitems = \"all\"\n", `write "*" or an array`},
		{"exclude with a slash", star + "exclude = [\"a/b\"]\n", `item "a/b" must be one name`},
		{"two stars into one folder", star + "[shares.t]\nsource = \"~/other\"\ninto = \"~/dst\"\nitems = \"*\"\n", "both link every entry into ~/dst"},
		{"named item under a star", star + "[shares.t]\nsource = \"~/other\"\ninto = \"~/dst\"\nitems = [\"x\"]\n", `add "x" to [shares.s] exclude`},
		{"file link under a star", star + "[shares.t]\nsource = \"~/f\"\nlinks = [\"~/dst/x\"]\n", `add "x" to [shares.s] exclude`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := shareSandbox(t, c.toml)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	// An excluded name may belong to another share.
	if _, _, err := shareSandbox(t, star+"exclude = [\"x\"]\n[shares.t]\nsource = \"~/other\"\ninto = \"~/dst\"\nitems = [\"x\"]\n"); err != nil {
		t.Fatalf("excluded name claimed by another share: %v", err)
	}
}
