package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordSandbox applies fileShare (links a and b) and then drops a from
// config.toml by hand, the way a user edits the file, leaving a's link behind.
func recordSandbox(t *testing.T) (h, source string, cfg *Config) {
	t.Helper()
	h, cfg, err := shareSandbox(t, fileShare)
	if err != nil {
		t.Fatal(err)
	}
	source = filepath.Join(h, "src", "AGENTS.md")
	os.MkdirAll(filepath.Dir(source), 0o755)
	os.WriteFile(source, []byte("x"), 0o644)
	if out, code := quiet(t, func() int { return cmdShare(cfg, "apply", nil) }); code != 0 {
		t.Fatalf("apply exited %d:\n%s", code, out)
	}
	body, _ := os.ReadFile(configPath())
	os.WriteFile(configPath(), []byte(strings.Replace(string(body), `"~/a/AGENTS.md", `, "", 1)), 0o644)
	return h, source, reload(t)
}

func readRecordFile(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(recordPath())
	return string(b)
}

// apply must write one sorted line per link it made, so a path that later
// leaves config.toml can still be traced back to kennel.
func TestApplyRecordsLinks(t *testing.T) {
	h, source, _ := recordSandbox(t)
	want := filepath.Join(h, "a", "AGENTS.md") + "\t" + source + "\n" + filepath.Join(h, "b", "AGENTS.md") + "\t" + source + "\n"
	if got := readRecordFile(t); got != want {
		t.Fatalf("record is %q, want %q", got, want)
	}
}

// Case 1 and 2: a link whose path left a file share's links still points at
// the recorded target, so doctor must FAIL it and prune must remove it and its
// line, leaving the link config still asks for.
func TestRecordedOrphanDoctorAndPrune(t *testing.T) {
	h, source, cfg := recordSandbox(t)
	a := filepath.Join(h, "a", "AGENTS.md")
	out, code := quiet(t, func() int { return cmdDoctor(cfg) })
	if code != 1 || !strings.Contains(out, "FAIL  recorded orphan    ~/a/AGENTS.md → ~/src/AGENTS.md (no share lists it any more; `kennel share prune` removes it)") {
		t.Fatalf("doctor exited %d:\n%s", code, out)
	}
	if strings.Contains(out, "~/b/AGENTS.md (no share") {
		t.Fatalf("doctor called a listed link an orphan:\n%s", out)
	}
	if out, code := quiet(t, func() int { return cmdShare(cfg, "prune", nil) }); code != 0 || !strings.Contains(out, "pruned    ~/a/AGENTS.md") {
		t.Fatalf("prune exited %d:\n%s", code, out)
	}
	if exists(a) {
		t.Fatal("prune left the orphan")
	}
	if got, _ := os.Readlink(filepath.Join(h, "b", "AGENTS.md")); got != source {
		t.Fatal("prune touched the listed link")
	}
	if got := readRecordFile(t); got != filepath.Join(h, "b", "AGENTS.md")+"\t"+source+"\n" {
		t.Fatalf("record after prune is %q", got)
	}
}

// Case 3: a link the user re-pointed by hand is theirs now. doctor and prune
// leave it, and prune drops the stale line; so does a path that is gone.
func TestRecordedRepointedLinkLeftAlone(t *testing.T) {
	h, source, cfg := recordSandbox(t)
	a := filepath.Join(h, "a", "AGENTS.md")
	os.Remove(a)
	os.Symlink("/elsewhere", a)
	out, code := quiet(t, func() int { return cmdDoctor(cfg) })
	if strings.Contains(out, "~/a/AGENTS.md") {
		t.Fatalf("doctor (exit %d) reported a re-pointed link:\n%s", code, out)
	}
	if out, code := quiet(t, func() int { return cmdShare(cfg, "prune", nil) }); code != 0 || !strings.Contains(out, "no orphaned links") {
		t.Fatalf("prune exited %d:\n%s", code, out)
	}
	if got, _ := os.Readlink(a); got != "/elsewhere" {
		t.Fatalf("prune touched the re-pointed link: %q", got)
	}
	if got := readRecordFile(t); got != filepath.Join(h, "b", "AGENTS.md")+"\t"+source+"\n" {
		t.Fatalf("prune kept the stale line: %q", got)
	}
}

// With every share gone from config.toml, doctor still FAILs the recorded
// links, so the prune it names must still remove them.
func TestPruneWithNoSharesLeft(t *testing.T) {
	h, _, _ := recordSandbox(t)
	body, _ := os.ReadFile(configPath())
	os.WriteFile(configPath(), []byte(strings.Split(string(body), "[shares.i]")[0]), 0o644)
	if out, code := quiet(t, func() int { return cmdShare(reload(t), "prune", nil) }); code != 0 || !strings.Contains(out, "pruned    ~/b/AGENTS.md") {
		t.Fatalf("prune exited %d:\n%s", code, out)
	}
	if exists(filepath.Join(h, "a", "AGENTS.md")) || exists(filepath.Join(h, "b", "AGENTS.md")) || readRecordFile(t) != "" {
		t.Fatal("prune left a recorded orphan or its line")
	}
}

// A scoped prune acts on one share, and a recorded orphan belongs to none:
// it must not remove the orphan or drop stale lines.
func TestScopedPruneLeavesRecord(t *testing.T) {
	h, _, cfg := recordSandbox(t)
	before := readRecordFile(t)
	quiet(t, func() int { return cmdShare(cfg, "prune", []string{"i"}) })
	if !exists(filepath.Join(h, "a", "AGENTS.md")) || readRecordFile(t) != before {
		t.Fatal("prune i reached a recorded orphan")
	}
}

// Case 4: links made before the record existed get recorded by the next
// apply that finds them already correct.
func TestApplyRecordsExistingLinks(t *testing.T) {
	h, source, cfg := recordSandbox(t)
	os.Remove(recordPath())
	quiet(t, func() int { return cmdShare(cfg, "apply", nil) })
	if got := readRecordFile(t); got != filepath.Join(h, "b", "AGENTS.md")+"\t"+source+"\n" {
		t.Fatalf("record after re-apply is %q", got)
	}
}

// undo and rm remove links, so their lines go too.
func TestUndoAndRmForgetLinks(t *testing.T) {
	h, cfg, err := shareSandbox(t, fileShare)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(h, "src", "AGENTS.md")
	os.MkdirAll(filepath.Dir(source), 0o755)
	os.WriteFile(source, []byte("x"), 0o644)
	quiet(t, func() int { return cmdShare(cfg, "apply", nil) })
	quiet(t, func() int { return cmdShareEdit(cfg, "rm", []string{"i", "--link", "~/a/AGENTS.md"}) })
	if got := readRecordFile(t); got != filepath.Join(h, "b", "AGENTS.md")+"\t"+source+"\n" {
		t.Fatalf("record after rm is %q", got)
	}
	quiet(t, func() int { return cmdShare(reload(t), "undo", nil) })
	if got := readRecordFile(t); got != "" {
		t.Fatalf("record after undo is %q", got)
	}
}

// A missing record is a note, not a FAIL; an unreadable one is never wiped.
func TestRecordMissingOrUnreadable(t *testing.T) {
	h, cfg, err := shareSandbox(t, fileShare)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := quiet(t, func() int { return cmdDoctor(cfg) })
	if !strings.Contains(out, "      (no share link recorded yet in ~/.local/state/kennel/links)") {
		t.Fatalf("doctor without a record:\n%s", out)
	}
	os.MkdirAll(filepath.Dir(recordPath()), 0o755)
	os.WriteFile(recordPath(), []byte("/x\t/y\n"), 0o000)
	source := filepath.Join(h, "src", "AGENTS.md")
	os.MkdirAll(filepath.Dir(source), 0o755)
	os.WriteFile(source, []byte("x"), 0o644)
	quiet(t, func() int { return cmdShare(cfg, "apply", nil) })
	os.Chmod(recordPath(), 0o644)
	if got := readRecordFile(t); got != "/x\t/y\n" {
		t.Fatalf("apply rewrote an unreadable record: %q", got)
	}
}

// Case 5: kennel env runs on every cd and must never open the record: with
// it unreadable env prints the same lines and exits 0.
func TestEnvNeverReadsRecord(t *testing.T) {
	_, _, cfg := recordSandbox(t)
	want, _ := quiet(t, func() int { return cmdEnv(cfg, false) })
	os.Chmod(recordPath(), 0o000)
	defer os.Chmod(recordPath(), 0o644)
	got, code := quiet(t, func() int { return cmdEnv(cfg, false) })
	if code != 0 || got != want {
		t.Fatalf("env with an unreadable record exited %d, printed %q, want %q", code, got, want)
	}
}
