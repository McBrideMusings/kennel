package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// feedVaultHook runs `kennel vault hook` on a Claude PostToolUse payload.
func feedVaultHook(t *testing.T, payload map[string]any) int {
	t.Helper()
	in, _ := json.Marshal(payload)
	oldIn := os.Stdin
	r, w, _ := os.Pipe()
	w.Write(in)
	w.Close()
	os.Stdin = r
	defer func() { os.Stdin = oldIn }()
	_, code := vaultRun(t, "hook")
	return code
}

func editPayload(session, file string) map[string]any {
	return map[string]any{
		"session_id": session, "hook_event_name": "PostToolUse", "tool_name": "Edit",
		"tool_input": map[string]any{"file_path": file, "old_string": "a", "new_string": "b"},
	}
}

// An agent's edit through the link into the vault becomes one commit naming
// its session and harness; an edit anywhere else commits nothing.
func TestVaultHookCommitsVaultEdits(t *testing.T) {
	h := vaultSandbox(t)
	t.Setenv("KENNEL_HARNESS", "")
	skill := filepath.Join(h, ".claude", "skills", "recomp")
	os.MkdirAll(skill, 0o755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("original\n"), 0o644)
	if out, code := vaultRun(t, "add", skill); code != 0 {
		t.Fatalf("vault add exited %d: %s", code, out)
	}

	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("edited\n"), 0o644)
	if code := feedVaultHook(t, editPayload("s-123", filepath.Join(skill, "SKILL.md"))); code != 0 {
		t.Fatalf("hook exited %d", code)
	}
	lines := vaultLogLines(t, "recomp")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "edit skills/recomp/SKILL.md (claude session s-123)") {
		t.Fatalf("log after the hooked edit = %q", lines)
	}

	t.Setenv("KENNEL_HARNESS", "codex")
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("again\n"), 0o644)
	feedVaultHook(t, editPayload("c-9", filepath.Join(skill, "SKILL.md")))
	if lines := vaultLogLines(t, "recomp"); len(lines) != 3 || !strings.HasSuffix(lines[0], "(codex session c-9)") {
		t.Fatalf("log after the codex edit = %q", lines)
	}

	outside := filepath.Join(h, "proj", "main.go")
	os.MkdirAll(filepath.Dir(outside), 0o755)
	os.WriteFile(outside, []byte("package main\n"), 0o644)
	before, _ := vaultAt().git("rev-list", "--count", "HEAD")
	if code := feedVaultHook(t, editPayload("s-123", outside)); code != 0 {
		t.Fatalf("hook on an outside file exited %d", code)
	}
	if after, _ := vaultAt().git("rev-list", "--count", "HEAD"); after != before {
		t.Fatalf("an outside edit changed the commit count from %s to %s", before, after)
	}
}

// The hook runs on every edit in every session; with no vault it must not
// make one.
func TestVaultHookCreatesNoVault(t *testing.T) {
	h := vaultSandbox(t)
	if code := feedVaultHook(t, editPayload("s-1", filepath.Join(h, "notes.md"))); code != 0 {
		t.Fatalf("hook exited %d", code)
	}
	if _, err := os.Lstat(filepath.Join(h, ".local", "share", "kennel")); err == nil {
		t.Fatal("the hook created kennel's data directory")
	}
}

// The vault's edit hook the repo ships leaves Claude for Codex and pi, and
// Codex's copy also matches apply_patch.
func TestVaultCommitHookIsShared(t *testing.T) {
	h, cfg := hookSandbox(t, "[hooks]\ncodex = true\npi = true\n")
	script, err := os.ReadFile("hooks/kennel-vault-commit.sh")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(h, ".claude/hooks/kennel-vault-commit.sh"), script, 0o755)
	settings := `{"hooks": {"PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": "~/.claude/hooks/kennel-vault-commit.sh", "timeout": 10}]}]}}`
	os.WriteFile(filepath.Join(h, ".claude/settings.json"), []byte(settings), 0o644)

	out, _ := quiet(t, func() int { return cmdHooks(cfg, []string{"plan"}) })
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "kennel-vault-commit.sh") {
			line = l
		}
	}
	if !strings.Contains(line, "PostToolUse") || !strings.Contains(line, "adapter") || !strings.HasSuffix(line, "→ codex, pi") {
		t.Fatalf("plan line for the vault hook = %q\n%s", line, out)
	}
	handlers, err := readClaudeHooks(cfg)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := planHooks(cfg, handlers, "/bin/kennel")
	if err != nil {
		t.Fatal(err)
	}
	var codexJSON string
	for _, o := range planned {
		if o.Target == "codex" {
			codexJSON = string(o.Want)
		}
	}
	if !strings.Contains(codexJSON, `"Edit|Write|MultiEdit|apply_patch"`) {
		t.Fatalf("hooks.json lacks the apply_patch matcher:\n%s", codexJSON)
	}
}

// A relative path resolves against cwd, a payload with no session_id says
// "unknown", and a deleted file in a deleted vault folder still commits.
func TestVaultHookRelativeAndDeleted(t *testing.T) {
	h := vaultSandbox(t)
	t.Setenv("KENNEL_HARNESS", "")
	skill := filepath.Join(h, ".claude", "skills", "recomp")
	os.MkdirAll(filepath.Join(skill, "refs"), 0o755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("original\n"), 0o644)
	os.WriteFile(filepath.Join(skill, "refs", "a.md"), []byte("ref\n"), 0o644)
	if out, code := vaultRun(t, "add", skill); code != 0 {
		t.Fatalf("vault add exited %d: %s", code, out)
	}

	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("edited\n"), 0o644)
	p := editPayload("", ".claude/skills/recomp/SKILL.md")
	delete(p, "session_id")
	p["cwd"] = h
	if code := feedVaultHook(t, p); code != 0 {
		t.Fatalf("hook exited %d", code)
	}
	if lines := vaultLogLines(t, "recomp"); !strings.HasSuffix(lines[0], "edit skills/recomp/SKILL.md (claude session unknown)") {
		t.Fatalf("log after the relative edit = %q", lines)
	}

	os.RemoveAll(filepath.Join(skill, "refs"))
	if code := feedVaultHook(t, editPayload("s-7", filepath.Join(skill, "refs", "a.md"))); code != 0 {
		t.Fatalf("hook exited %d", code)
	}
	if lines := vaultLogLines(t, "recomp"); !strings.HasSuffix(lines[0], "edit skills/recomp/refs/a.md (claude session s-7)") {
		t.Fatalf("log after deleting refs/ = %q", lines)
	}
}

// An empty payload does nothing, as `kennel hook` reads one; a payload that
// is not JSON is an error.
func TestVaultHookPayloadShapes(t *testing.T) {
	vaultSandbox(t)
	oldIn := os.Stdin
	defer func() { os.Stdin = oldIn }()
	for _, c := range []struct {
		in   string
		code int
	}{{"", 0}, {"  \n", 0}, {"{not json", 1}} {
		r, w, _ := os.Pipe()
		w.Write([]byte(c.in))
		w.Close()
		os.Stdin = r
		if _, code := vaultRun(t, "hook"); code != c.code {
			t.Errorf("payload %q: exit %d, want %d", c.in, code, c.code)
		}
	}
}

// `kennel hook` tells the hook which tool runs it.
func TestHookSetsKennelHarness(t *testing.T) {
	h, cfg := hookSandbox(t, "")
	payload := map[string]any{"hook_event_name": "PostToolUse", "tool_name": "edit", "cwd": h, "tool_input": map[string]any{"path": "a.md"}}
	runHookCmd(t, cfg, "pi", payload, "/bin/sh", "-c", `echo "$KENNEL_HARNESS $KENNEL_BIN" > "$HOME/harness"`)
	b, _ := os.ReadFile(filepath.Join(h, "harness"))
	exe, _ := selfPath()
	if got := strings.TrimSpace(string(b)); got != "pi "+exe {
		t.Fatalf("KENNEL_HARNESS KENNEL_BIN = %q, want %q", got, "pi "+exe)
	}
}

// pi's generated extension, run in node with pi's session id on its context,
// sends that id through `kennel hook pi` to the repo's vault hook, and the
// edit pi made through the link becomes a vault commit naming that session.
func TestHookPiVaultCommitNamesSession(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; pi extensions run in node")
	}
	h, cfg := hookSandbox(t, "[hooks]\npi = true\n")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("KENNEL_TEST_MAIN", "1") // every kennel the children start is this binary
	// Installed as the README says: the repo's script in Claude's hooks folder.
	script, err := os.ReadFile(filepath.Join("hooks", "kennel-vault-commit.sh"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(h, ".claude", "hooks", "kennel-vault-commit.sh"), script, 0o755)
	settings := `{"hooks": {"PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": "~/.claude/hooks/kennel-vault-commit.sh"}]}]}}`
	os.WriteFile(filepath.Join(h, ".claude", "settings.json"), []byte(settings), 0o644)
	if _, code := quiet(t, func() int { return cmdHooks(cfg, []string{"apply"}) }); code != 0 {
		t.Fatal("hooks apply failed")
	}

	skill := filepath.Join(h, ".claude", "skills", "recomp")
	os.MkdirAll(skill, 0o755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("original\n"), 0o644)
	// The hook runs in a child kennel, and an in-process vault command would
	// hold the vault lock until the test exits, so add runs in a child too.
	if out, err := exec.Command(os.Args[0], "vault", "add", skill).CombinedOutput(); err != nil {
		t.Fatalf("vault add: %v: %s", err, out)
	}
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("edited\n"), 0o644)

	ext := filepath.Join(h, ".pi", "agent", "extensions", "kennel-hooks.ts")
	drive := filepath.Join(h, "drive.mjs")
	os.WriteFile(drive, []byte(`const mod = await import(`+jsString(ext)+`);
const on = {};
mod.default({ on: (name, fn) => (on[name] = fn) });
const ctx = { cwd: `+jsString(skill)+`, hasUI: false, sessionManager: { getSessionId: () => "019a-pi-session" }, ui: { notify: (m) => { console.error(m); process.exitCode = 1; } } };
await on.tool_result({ toolName: "edit", input: { path: "SKILL.md", oldText: "original", newText: "edited" }, content: [] }, ctx);
`), 0o644)
	if out, err := exec.Command(node, drive).CombinedOutput(); err != nil {
		t.Fatalf("node ran the extension: %v: %s", err, out)
	}
	if lines := vaultLogLines(t, "recomp"); len(lines) != 2 || !strings.HasSuffix(lines[0], "edit skills/recomp/SKILL.md (pi session 019a-pi-session)") {
		t.Fatalf("log after pi's edit = %q", lines)
	}
}
