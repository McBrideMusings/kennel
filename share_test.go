package main

import (
	"os"
	"path/filepath"
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

	if got := cmdShare(cfg, "apply"); got != 1 {
		t.Fatalf("apply with a blocked link exited %d, want 1", got)
	}
	if b, _ := os.ReadFile(filepath.Join(h, "b", "AGENTS.md")); string(b) != "mine" {
		t.Fatalf("blocking file changed to %q", b)
	}
	if got, _ := os.Readlink(filepath.Join(h, "a", "AGENTS.md")); got != filepath.Join(h, "src", "AGENTS.md") {
		t.Fatalf("link a points at %q", got)
	}
	cmdShare(cfg, "undo")
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
	cmdShare(cfg, "undo")
	if got, _ := os.Readlink(filepath.Join(h, "a", "AGENTS.md")); got != "/elsewhere" {
		t.Fatalf("undo changed a foreign link to %q", got)
	}
}
