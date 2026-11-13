package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// `kennel hook codex|pi -- <command…>` runs one Claude hook under another
// tool. It sets the variables Claude would have set, reshapes the payload
// where the tool's differs from Claude's, and reshapes the hook's answer
// into what the tool understands.

type hookRun struct {
	stdout, stderr []byte
	code           int
	launch         bool // the script never started
}

// failClosed reports a failure of kennel itself. Codex runs a call whose
// hook exits 1, so under Codex it exits 2, which blocks; pi's extension
// blocks on any exit but 0.
func failClosed(target, format string, a ...any) int {
	failf(format, a...)
	if target == "codex" {
		return 2
	}
	return 1
}

func cmdHook(args []string) int {
	if len(args) < 3 || (args[0] != "codex" && args[0] != "pi") || args[1] != "--" {
		return failClosed("codex", "usage: kennel hook codex|pi -- <command…>")
	}
	target, argv := args[0], args[2:]
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		return failClosed(target, "read the hook payload: %v", err)
	}
	payload := map[string]any{}
	if len(bytes.TrimSpace(in)) > 0 {
		if err := json.Unmarshal(in, &payload); err != nil {
			return failClosed(target, "hook payload is not JSON: %v", err)
		}
	}
	cfg, err := loadConfig()
	if err != nil {
		return failClosed(target, "%v", err)
	}
	env := hookEnv(cfg, target, str(payload["cwd"]))
	if target == "codex" {
		return hookCodex(payload, argv, env)
	}
	return hookPi(payload, argv, env)
}

// hookEnv sets KENNEL_HARNESS to the tool running the hook, KENNEL_BIN to
// this binary (so a hook reaches kennel when PATH lacks it), CLAUDE_PROJECT_DIR
// to the session's directory and CLAUDE_HOME to the Claude home kennel routes
// that directory to; Codex and pi set neither.
func hookEnv(cfg *Config, target, cwd string) []string {
	env := append(os.Environ(), "KENNEL_HARNESS="+target)
	if exe, err := selfPath(); err == nil {
		env = append(env, "KENNEL_BIN="+exe)
	}
	if cwd == "" {
		return env
	}
	env = append(env, "CLAUDE_PROJECT_DIR="+cwd)
	if t, ok := cfg.Tools["claude"]; ok {
		h := t.Base
		if lines, err := readRoutes(); err == nil {
			if p := cfg.Profiles[resolve(cfg, lines, cwd)]["claude"]; p != "" {
				h = p
			}
		}
		env = append(env, "CLAUDE_HOME="+h)
	}
	return env
}

func runHook(argv []string, env []string, payload map[string]any) hookRun {
	in, _ := json.Marshal(payload)
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = env
	if d := str(payload["cwd"]); d != "" && isDir(d) {
		c.Dir = d
	}
	c.Stdin = bytes.NewReader(in)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	r := hookRun{}
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			r.code = ee.ExitCode()
			// Generated entries run through /bin/sh -c, so a missing or
			// non-executable script shows as the shell's 127 or 126.
			r.launch = r.code == 126 || r.code == 127
		} else {
			fmt.Fprintf(&errb, "kennel: %v\n", err)
			r.code, r.launch = 1, true
		}
	}
	r.stdout, r.stderr = out.Bytes(), errb.Bytes()
	return r
}

// ---- Codex ----

// hookCodex runs the hook once, or once per file when the call is an
// apply_patch, and prints Codex the first deny or else the first output.
func hookCodex(payload map[string]any, argv, env []string) int {
	calls := []map[string]any{payload}
	paths, isPatch := patchPaths(payload)
	if isPatch && len(paths) == 0 {
		return failClosed("codex", "apply_patch names no file (*** Update|Add|Delete File:), so no edit guard can check it")
	}
	if isPatch {
		calls = calls[:0]
		for _, p := range paths {
			calls = append(calls, patchCall(payload, p))
		}
	}
	var first *hookRun
	code := 0
	var stderr bytes.Buffer
	for _, call := range calls {
		r := runHook(argv, env, call)
		if r.launch {
			return failClosed("codex", "%s", strings.TrimSpace(string(r.stderr)))
		}
		denied := r.code == 2
		if !denied {
			r.stdout, denied = codexAnswer(r.stdout, str(payload["hook_event_name"]))
		}
		if denied {
			os.Stdout.Write(r.stdout)
			os.Stderr.Write(r.stderr)
			return r.code
		}
		stderr.Write(r.stderr)
		if r.code != 0 && code == 0 {
			code = r.code
		}
		if first == nil && len(bytes.TrimSpace(r.stdout)) > 0 {
			rr := r
			first = &rr
		}
	}
	if first != nil {
		os.Stdout.Write(first.stdout)
	}
	os.Stderr.Write(stderr.Bytes())
	return code
}

// codexAnswer rewrites a hook's JSON answer into one Codex acts on, and
// reports whether it denies. Codex treats "ask" as a failed hook and lets
// the call run, and honours updatedInput only beside "allow", which would
// also skip its own approval; both become a deny carrying the reason.
func codexAnswer(stdout []byte, event string) ([]byte, bool) {
	var obj map[string]any
	if json.Unmarshal(stdout, &obj) != nil {
		return stdout, false
	}
	if d := str(obj["decision"]); event == "PreToolUse" && d == "block" {
		return stdout, true
	}
	hso, _ := obj["hookSpecificOutput"].(map[string]any)
	if hso == nil {
		return stdout, false
	}
	switch str(hso["permissionDecision"]) {
	case "deny":
		return stdout, true
	case "ask":
		hso["permissionDecision"] = "deny"
	case "":
		ui, ok := hso["updatedInput"].(map[string]any)
		if !ok || event != "PreToolUse" {
			return stdout, false
		}
		reason := "[kennel] Codex cannot rewrite a call in place; run this instead: " + str(ui["command"])
		if ctx := str(hso["additionalContext"]); ctx != "" {
			reason += " (" + ctx + ")"
		}
		delete(hso, "updatedInput")
		delete(hso, "additionalContext")
		hso["permissionDecision"] = "deny"
		hso["permissionDecisionReason"] = reason
	default:
		return stdout, false
	}
	b, _ := json.Marshal(obj)
	return append(b, '\n'), true
}

// patchPaths returns every file an apply_patch call touches, in patch
// order, and whether the call is an apply_patch at all.
func patchPaths(payload map[string]any) ([]string, bool) {
	if str(payload["tool_name"]) != "apply_patch" {
		return nil, false
	}
	ti, _ := payload["tool_input"].(map[string]any)
	if ti != nil && str(ti["file_path"]) != "" {
		return nil, false
	}
	if ti == nil {
		return nil, true
	}
	var paths []string
	seen := map[string]bool{}
	for _, line := range strings.Split(str(ti["command"]), "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Update File:", "*** Add File:", "*** Delete File:", "*** Move to:"} {
			if p, ok := strings.CutPrefix(line, prefix); ok {
				if p = strings.TrimSpace(p); p != "" && !seen[p] {
					seen[p] = true
					paths = append(paths, p)
				}
			}
		}
	}
	return paths, true
}

// patchCall is the Claude-shaped payload for one file of a patch.
func patchCall(payload map[string]any, path string) map[string]any {
	call := map[string]any{}
	for k, v := range payload {
		call[k] = v
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(str(payload["cwd"]), path)
	}
	call["tool_name"] = "Edit"
	call["tool_input"] = map[string]any{"file_path": path}
	return call
}

// ---- pi ----

var piToClaude = map[string]string{"bash": "Bash", "edit": "Edit", "write": "Write", "read": "Read"}

// piVerdict is what the generated extension reads back.
type piVerdict struct {
	Decision string         `json:"decision,omitempty"`
	Reason   string         `json:"reason,omitempty"`
	Context  string         `json:"context,omitempty"`
	Input    map[string]any `json:"input,omitempty"`
}

// hookPi turns pi's tool call into Claude's payload, runs the hook and
// prints one piVerdict. A hook's own failure (any exit but 0 and 2) is
// reported and lets the call run, as Claude does; kennel failing exits 1,
// which the extension turns into a block.
func hookPi(payload map[string]any, argv, env []string) int {
	event := str(payload["hook_event_name"])
	piTool := str(payload["tool_name"])
	call := map[string]any{}
	for k, v := range payload {
		call[k] = v
	}
	if name, ok := piToClaude[piTool]; ok {
		call["tool_name"] = name
	}
	ti, _ := payload["tool_input"].(map[string]any)
	cti := map[string]any{}
	for k, v := range ti {
		cti[k] = v
	}
	if p := str(ti["path"]); p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(str(payload["cwd"]), p)
		}
		cti["file_path"] = p
	}
	call["tool_input"] = cti

	r := runHook(argv, env, call)
	os.Stderr.Write(r.stderr)
	if r.launch {
		return 1
	}
	v := piVerdict{}
	switch {
	case r.code == 2 && event == "PreToolUse":
		v.Decision, v.Reason = "deny", strings.TrimSpace(string(r.stderr))
	case r.code == 2:
		v.Context = strings.TrimSpace(string(r.stderr))
	case r.code == 0:
		v = piAnswer(r.stdout, event, piTool)
	}
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
	return 0
}

func piAnswer(stdout []byte, event, piTool string) piVerdict {
	v := piVerdict{}
	var obj map[string]any
	if json.Unmarshal(stdout, &obj) != nil {
		return v
	}
	if str(obj["decision"]) == "block" {
		if event == "PreToolUse" {
			return piVerdict{Decision: "deny", Reason: str(obj["reason"])}
		}
		v.Context = str(obj["reason"])
	}
	hso, _ := obj["hookSpecificOutput"].(map[string]any)
	if hso == nil {
		return v
	}
	if ctx := str(hso["additionalContext"]); ctx != "" {
		v.Context = strings.TrimSpace(v.Context + "\n" + ctx)
	}
	switch d := str(hso["permissionDecision"]); d {
	case "deny", "ask":
		v.Decision, v.Reason = d, str(hso["permissionDecisionReason"])
		return v
	}
	if ui, ok := hso["updatedInput"].(map[string]any); ok {
		in := map[string]any{}
		for k, val := range ui {
			if k == "file_path" && piTool != "bash" {
				k = "path"
			}
			in[k] = val
		}
		v.Input = in
	}
	return v
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
