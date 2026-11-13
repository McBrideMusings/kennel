package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const foreignHooks = `{
  "hooks": {
    "SessionStart": [
      {"matcher": "startup|resume", "hooks": [{"type": "command", "command": "bash ~/.codex/herdr-agent-state.sh session", "timeout": 10}]}
    ],
    "PreToolUse": [
      {"matcher": "^Bash$", "hooks": [
        {"type": "command", "command": "bash ~/.claude/hooks/old-guard.sh", "timeout": 10},
        {"type": "command", "command": "canvas hook pre && echo <ok>", "timeout": 5}
      ]}
    ],
    "Stop": [{"hooks": [{"type": "command", "command": "moshi notify"}]}]
  }
}
`

const sandboxSettings = `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/guard.sh"}]},
      {"matcher": "Read", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/untagged.sh"}]},
      {"matcher": "Edit|Write", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/path-guard.sh", "timeout": 5}]}
    ],
    "CwdChanged": [{"hooks": [{"type": "command", "command": "bash ~/.claude/hooks/guard.sh"}]}]
  }
}
`

// hookSandbox builds a throwaway HOME with Claude's settings and scripts,
// a Codex hooks.json carrying other tools' entries, and a config with the
// given [hooks] table.
func hookSandbox(t *testing.T, hooksTable string) (string, *Config) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("KENNEL_CONFIG_DIR", filepath.Join(h, ".cfg"))
	t.Setenv("PI_CODING_AGENT_DIR", "")
	write := func(p, body string, mode os.FileMode) {
		p = filepath.Join(h, p)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/settings.json", sandboxSettings, 0o644)
	write(".claude/hooks/guard.sh", "#!/bin/bash\n# kennel-hook: portable\ncat >/dev/null\n", 0o755)
	write(".claude/hooks/untagged.sh", "#!/bin/bash\ncat >/dev/null\n", 0o755)
	// path-guard logs every file_path it sees and denies any under bad/.
	write(".claude/hooks/path-guard.sh", "#!/bin/bash\n# kennel-hook: adapter\np=$(jq -r .tool_input.file_path)\necho \"$p\" >> \"$HOME/seen\"\ncase $p in */bad/*) jq -n --arg r \"no $p\" '{hookSpecificOutput:{hookEventName:\"PreToolUse\",permissionDecision:\"deny\",permissionDecisionReason:$r}}';; esac\n", 0o755)
	write(".claude/hooks/asker.sh", "#!/bin/bash\ncat >/dev/null\necho '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"ask\",\"permissionDecisionReason\":\"[asker] sure?\"}}'\n", 0o755)
	write(".codex/hooks.json", foreignHooks, 0o644)
	write(".pi/agent/extensions/other.ts", "// someone else's\n", 0o644)
	write(".cfg/config.toml", "[tools.claude]\nbase = \"~/.claude\"\nshared = []\n[tools.codex]\nbase = \"~/.codex\"\nshared = []\n"+hooksTable, 0o644)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg
}

func snapshot(t *testing.T, dir string) map[string]string {
	out := map[string]string{}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

func quiet(t *testing.T, f func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := f()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), code
}

func TestHooksOptInOffWritesNothing(t *testing.T) {
	h, cfg := hookSandbox(t, "")
	codexBefore := snapshot(t, filepath.Join(h, ".codex"))
	piBefore := snapshot(t, filepath.Join(h, ".pi"))
	if _, code := quiet(t, func() int { return cmdHooks(cfg, []string{"apply"}) }); code != 0 {
		t.Fatalf("apply exited %d", code)
	}
	if got := snapshot(t, filepath.Join(h, ".codex")); !equalMaps(got, codexBefore) {
		t.Fatal("hooks.json changed with opt-in off")
	}
	if got := snapshot(t, filepath.Join(h, ".pi")); !equalMaps(got, piBefore) {
		t.Fatal("pi extensions changed with opt-in off")
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

type codexFile struct {
	Hooks map[string][]struct {
		Matcher string            `json:"matcher"`
		Hooks   []json.RawMessage `json:"hooks"`
	} `json:"hooks"`
}

// A generated run that dropped or changed another tool's entry would stop
// that tool's hook firing in every Codex session; a rewrite of unchanged
// output would make Codex ask to re-trust every entry.
func TestHooksApplyCodexKeepsForeignAndIsStable(t *testing.T) {
	h, cfg := hookSandbox(t, "[hooks]\ncodex = true\n")
	path := filepath.Join(h, ".codex", "hooks.json")
	if _, code := quiet(t, func() int { return cmdHooks(cfg, []string{"apply"}) }); code != 0 {
		t.Fatalf("apply exited %d", code)
	}
	first, _ := os.ReadFile(path)
	var f codexFile
	if err := json.Unmarshal(first, &f); err != nil {
		t.Fatal(err)
	}
	all := string(first)
	for _, want := range []string{"herdr-agent-state.sh session", "canvas hook pre && echo <ok>", "moshi notify"} {
		if !strings.Contains(all, want) {
			t.Fatalf("lost foreign entry %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "old-guard.sh") || strings.Contains(all, "untagged.sh") {
		t.Fatalf("kept an entry it owns or a claude-only hook:\n%s", all)
	}
	pre := f.Hooks["PreToolUse"]
	if len(pre) != 3 || pre[0].Matcher != "^Bash$" || len(pre[0].Hooks) != 1 || pre[1].Matcher != "Bash" || pre[2].Matcher != "Edit|Write|apply_patch" {
		t.Fatalf("PreToolUse groups wrong:\n%s", all)
	}
	var gen codexHandler
	json.Unmarshal(pre[2].Hooks[0], &gen)
	if !strings.HasSuffix(gen.Command, " hook codex -- /bin/sh -c 'bash ~/.claude/hooks/path-guard.sh'") || gen.Timeout != 5 {
		t.Fatalf("generated handler %+v", gen)
	}
	if _, ok := f.Hooks["CwdChanged"]; ok {
		t.Fatal("wrote an event Codex does not fire")
	}
	if out, _ := quiet(t, func() int { return cmdHooks(cfg, []string{"apply"}) }); !strings.Contains(out, "unchanged") {
		t.Fatalf("second apply said %q", out)
	}
	if second, _ := os.ReadFile(path); !bytes.Equal(first, second) {
		t.Fatal("second apply changed hooks.json")
	}
}

func TestHooksApplyPiWritesExtension(t *testing.T) {
	h, cfg := hookSandbox(t, "[hooks]\npi = true\n")
	quiet(t, func() int { return cmdHooks(cfg, []string{"apply"}) })
	b, err := os.ReadFile(filepath.Join(h, ".pi", "agent", "extensions", "kennel-hooks.ts"))
	if err != nil || !strings.Contains(string(b), `["hooks", "list", "pi"]`) {
		t.Fatalf("extension missing or wrong: %v", err)
	}
	out, _ := quiet(t, func() int { return cmdHooks(cfg, []string{"list", "pi"}) })
	var items []struct{ Event, Matcher, Tag string }
	json.Unmarshal([]byte(out), &items)
	if len(items) != 2 || items[0].Tag != "portable" || items[1].Matcher != "Edit|Write" {
		t.Fatalf("list pi = %s", out)
	}
}

// runHookCmd feeds payload to `kennel hook <target> -- <argv>` and returns
// what it printed and its exit status.
func runHookCmd(t *testing.T, cfg *Config, target string, payload any, argv ...string) (string, int) {
	t.Helper()
	in, _ := json.Marshal(payload)
	oldIn := os.Stdin
	r, w, _ := os.Pipe()
	w.Write(in)
	w.Close()
	os.Stdin = r
	defer func() { os.Stdin = oldIn }()
	return quiet(t, func() int { return cmdHook(append([]string{target, "--"}, argv...)) })
}

// A patch guard that saw only the first file, or let a two-file patch
// through when the second file is denied, would let Codex edit what
// Claude cannot.
func TestHookCodexSplitsApplyPatch(t *testing.T) {
	h, cfg := hookSandbox(t, "")
	patch := "*** Begin Patch\n*** Update File: ok/a.go\n@@\n-x\n+y\n*** Add File: bad/b.go\n+z\n*** End Patch\n"
	payload := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "apply_patch", "cwd": h, "tool_input": map[string]any{"command": patch}}
	out, code := runHookCmd(t, cfg, "codex", payload, "bash", filepath.Join(h, ".claude/hooks/path-guard.sh"))
	seen, _ := os.ReadFile(filepath.Join(h, "seen"))
	if got := strings.Fields(string(seen)); len(got) != 2 || got[0] != filepath.Join(h, "ok/a.go") || got[1] != filepath.Join(h, "bad/b.go") {
		t.Fatalf("guard saw %q", seen)
	}
	if code != 0 || !strings.Contains(out, `"deny"`) || !strings.Contains(out, "no "+filepath.Join(h, "bad/b.go")) {
		t.Fatalf("patch not denied: %d %s", code, out)
	}
}

// Codex treats "ask" as a failed hook and runs the call, so an ask passed
// through unchanged is an allow.
func TestHookCodexAskBecomesDeny(t *testing.T) {
	h, cfg := hookSandbox(t, "")
	payload := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": h, "tool_input": map[string]any{"command": "gh pr create"}}
	out, _ := runHookCmd(t, cfg, "codex", payload, "bash", filepath.Join(h, ".claude/hooks/asker.sh"))
	var got struct {
		HSO struct{ PermissionDecision, PermissionDecisionReason string } `json:"hookSpecificOutput"`
	}
	json.Unmarshal([]byte(out), &got)
	if got.HSO.PermissionDecision != "deny" || got.HSO.PermissionDecisionReason != "[asker] sure?" {
		t.Fatalf("got %s", out)
	}
}

func TestHookPiTranslatesPathAndAsk(t *testing.T) {
	h, cfg := hookSandbox(t, "")
	payload := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "edit", "cwd": h, "tool_input": map[string]any{"path": "bad/c.go"}}
	out, _ := runHookCmd(t, cfg, "pi", payload, "bash", filepath.Join(h, ".claude/hooks/path-guard.sh"))
	if !strings.Contains(out, `"decision":"deny"`) || !strings.Contains(out, filepath.Join(h, "bad/c.go")) {
		t.Fatalf("pi edit: %s", out)
	}
	out, _ = runHookCmd(t, cfg, "pi", map[string]any{"hook_event_name": "PreToolUse", "tool_name": "bash", "cwd": h, "tool_input": map[string]any{"command": "x"}}, "bash", filepath.Join(h, ".claude/hooks/asker.sh"))
	if !strings.Contains(out, `"decision":"ask"`) {
		t.Fatalf("pi ask: %s", out)
	}
}

// Each of these failures used to let the call run under Codex: an
// unparseable patch reached the edit guard with no path, and a broken
// config or a missing script exited 1, which Codex treats as allow.
func TestHookCodexFailsClosed(t *testing.T) {
	h, cfg := hookSandbox(t, "")
	guard := filepath.Join(h, ".claude/hooks/path-guard.sh")
	patch := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "apply_patch", "cwd": h, "tool_input": map[string]any{"command": "*** Begin Patch\n*** End Patch\n"}}
	if _, code := runHookCmd(t, cfg, "codex", patch, "bash", guard); code != 2 {
		t.Fatalf("empty patch exited %d, want 2", code)
	}
	bash := map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": h, "tool_input": map[string]any{"command": "ls"}}
	if _, code := runHookCmd(t, cfg, "codex", bash, filepath.Join(h, "no-such-hook")); code != 2 {
		t.Fatalf("missing script exited %d, want 2", code)
	}
	// The shape every generated entry has: the shell starts, the script does not.
	if _, code := runHookCmd(t, cfg, "codex", bash, "/bin/sh", "-c", "bash "+filepath.Join(h, "no-such-hook.sh")); code != 2 {
		t.Fatalf("missing script behind sh -c exited %d, want 2", code)
	}
	if _, code := runHookCmd(t, cfg, "pi", bash, "/bin/sh", "-c", filepath.Join(h, "no-such-hook.sh")); code != 1 {
		t.Fatalf("pi missing script behind sh -c exited %d, want 1", code)
	}
	os.WriteFile(configPath(), []byte("[tools.claude\n"), 0o644)
	if _, code := runHookCmd(t, cfg, "codex", bash, "true"); code != 2 {
		t.Fatalf("broken config exited %d, want 2", code)
	}
	if _, code := runHookCmd(t, cfg, "pi", bash, "true"); code != 1 {
		t.Fatalf("pi broken config exited %d, want 1", code)
	}
}

func TestCodexMatcherAddsApplyPatch(t *testing.T) {
	for in, want := range map[string]string{
		"Edit|Write":                        "Edit|Write|apply_patch",
		"Edit|Write|MultiEdit|NotebookEdit": "Edit|Write|MultiEdit|NotebookEdit|apply_patch",
		"^(Edit|Write)$":                    "^(Edit|Write)$|apply_patch",
		"^Edit$":                            "^Edit$|apply_patch",
		"Bash":                              "Bash",
		"NotebookEdit":                      "NotebookEdit",
		"":                                  "",
	} {
		if got := codexMatcher(in); got != want {
			t.Errorf("codexMatcher(%q) = %q, want %q", in, got, want)
		}
	}
}
