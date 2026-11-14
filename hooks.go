package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Claude Code's hooks run under Codex and pi when the config opts in. Each
// hook script carries a tag line near its top:
//
//	# kennel-hook: portable   runs unchanged on Codex's and pi's payloads
//	# kennel-hook: adapter    an edit guard that needs apply_patch split per file
//
// A script with no tag line is claude-only and never leaves Claude.

const (
	tagPortable   = "portable"
	tagAdapter    = "adapter"
	tagClaudeOnly = "claude-only"
	tagScanLines  = 20
)

var tagLine = regexp.MustCompile(`^#\s*kennel-hook:\s*(\S+)\s*$`)

// codexEvents are the hook events Codex 0.163+ fires.
var codexEvents = map[string]bool{
	"SessionStart": true, "SessionEnd": true, "PreToolUse": true, "PermissionRequest": true,
	"PostToolUse": true, "PreCompact": true, "PostCompact": true, "UserPromptSubmit": true,
	"SubagentStart": true, "SubagentStop": true, "Stop": true, "Interrupt": true,
}

// piEvents are the Claude events pi's extension maps onto its own.
var piEvents = map[string]bool{"PreToolUse": true, "PostToolUse": true}

// claudeDefaultTimeout is the seconds Claude Code gives a hook with no
// timeout; Codex's own default is 600, so generated entries carry Claude's.
const claudeDefaultTimeout = 60

// hookHandler is one command in Claude's settings.json hooks block.
type hookHandler struct {
	Event   string
	Group   int // index of its group inside the event, for stable grouping
	Matcher string
	Command string
	Timeout int
	Script  string // absolute path of the hook script it runs, "" when none
	Tag     string
	Problem string // an unreadable script or an unknown tag
}

type claudeSettings struct {
	Hooks map[string][]claudeHookGroup `json:"hooks"`
}

// claudeHookGroup is one matcher and its handlers under an event.
type claudeHookGroup struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	} `json:"hooks"`
}

// hooksDir is where Claude's hook scripts live.
func hooksDir(cfg *Config) string { return filepath.Join(toolBase(cfg, "claude"), "hooks") }

// hooksDirForms lists every spelling of the hooks directory a command can
// carry: the absolute path, ~/…, $HOME/… and ${HOME}/….
func hooksDirForms(dir string) []string {
	forms := []string{dir + "/"}
	if t := tilde(dir); strings.HasPrefix(t, "~/") {
		forms = append(forms, t+"/", "$HOME"+t[1:]+"/", "${HOME}"+t[1:]+"/")
	}
	return forms
}

// scriptIn returns the absolute path of the first hook script a command
// names inside dir, or "".
func scriptIn(command, dir string) string {
	best, at := "", -1
	for _, form := range hooksDirForms(dir) {
		i := strings.Index(command, form)
		if i < 0 || (at >= 0 && i >= at) {
			continue
		}
		rest := command[i+len(form):]
		end := strings.IndexAny(rest, " \t'\";|&)")
		if end < 0 {
			end = len(rest)
		}
		if end > 0 {
			best, at = filepath.Join(dir, rest[:end]), i
		}
	}
	return best
}

// namesHooksDir reports whether a command points into the hooks directory;
// those are the entries kennel owns in Codex's hooks.json.
func namesHooksDir(command, dir string) bool {
	for _, form := range hooksDirForms(dir) {
		if strings.Contains(command, form) {
			return true
		}
	}
	return false
}

// readTag returns a script's tag from its first lines; no tag is claude-only.
func readTag(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return tagClaudeOnly, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 0; n < tagScanLines && sc.Scan(); n++ {
		if m := tagLine.FindStringSubmatch(sc.Text()); m != nil {
			switch m[1] {
			case tagPortable, tagAdapter, tagClaudeOnly:
				return m[1], nil
			}
			return tagClaudeOnly, fmt.Errorf("unknown tag %q (portable, adapter or claude-only)", m[1])
		}
	}
	return tagClaudeOnly, sc.Err()
}

// readClaudeHooks lists every handler in Claude's settings.json, in file
// order within each event, with its script's tag.
func readClaudeHooks(cfg *Config) ([]hookHandler, error) {
	path := filepath.Join(toolBase(cfg, "claude"), "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s claudeSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %v", tilde(path), err)
	}
	// encoding/json loses the object's key order, so read the event names
	// in file order separately.
	events, err := objectKeys(data, "hooks")
	if err != nil {
		return nil, fmt.Errorf("%s: %v", tilde(path), err)
	}
	dir := hooksDir(cfg)
	tags := map[string][2]string{}
	var out []hookHandler
	for _, ev := range events {
		for gi, g := range s.Hooks[ev] {
			for _, h := range g.Hooks {
				if h.Type != "command" {
					continue
				}
				hh := hookHandler{Event: ev, Group: gi, Matcher: g.Matcher, Command: h.Command, Timeout: h.Timeout, Tag: tagClaudeOnly}
				if hh.Script = scriptIn(h.Command, dir); hh.Script != "" {
					t, ok := tags[hh.Script]
					if !ok {
						tag, err := readTag(hh.Script)
						t = [2]string{tag, ""}
						if err != nil {
							t[1] = err.Error()
						}
						tags[hh.Script] = t
					}
					hh.Tag, hh.Problem = t[0], t[1]
				}
				out = append(out, hh)
			}
		}
	}
	return out, nil
}

// objectKeys returns the keys of the object at data[field], in file order.
func objectKeys(data []byte, field string) ([]string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	raw, ok := top[field]
	if !ok {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("%q is not an object", field)
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, t.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// shared reports whether a handler leaves Claude for the given target.
func (h hookHandler) shared(target string) bool {
	if h.Tag != tagPortable && h.Tag != tagAdapter {
		return false
	}
	if target == "codex" {
		return codexEvents[h.Event]
	}
	return piEvents[h.Event]
}

// wrapped is the command a target runs: the handler behind kennel's wrapper.
// The handler's command runs through /bin/sh -c as one quoted word, so a
// command carrying &&, | or ; stays wholly inside the wrapper.
func (h hookHandler) wrapped(exe, target string) string {
	return shellQuote(exe) + " hook " + target + " -- /bin/sh -c " + shellQuote(h.Command)
}

var editMatcher = regexp.MustCompile(`\b(Edit|Write|MultiEdit)\b`)

// codexMatcher adds apply_patch to a matcher naming Claude's edit tools,
// since Codex's one edit tool is apply_patch.
func codexMatcher(m string) string {
	if editMatcher.MatchString(m) && !strings.Contains(m, "apply_patch") {
		return m + "|apply_patch"
	}
	return m
}

// marshalRaw encodes without HTML escaping, so another tool's command
// carrying & < or > is written back byte for byte.
func marshalRaw(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func (h hookHandler) timeout() int {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return claudeDefaultTimeout
}

// ---- Codex ----

func codexHooksPath(cfg *Config) string { return filepath.Join(cfg.Tools["codex"].Base, "hooks.json") }

type codexHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// codexHooksJSON returns hooks.json as it should be: every group and
// handler not pointing into Claude's hooks directory kept as it was, in
// place, and one generated group per Claude group with shared handlers
// appended after them in settings.json order. Output is indented with
// sorted keys, so the same inputs always give the same bytes.
func codexHooksJSON(current []byte, handlers []hookHandler, dir, exe string) ([]byte, error) {
	top := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(current)) > 0 {
		if err := json.Unmarshal(current, &top); err != nil {
			return nil, fmt.Errorf("hooks.json: %v", err)
		}
	}
	events := map[string][]map[string]json.RawMessage{}
	if raw, ok := top["hooks"]; ok {
		if err := json.Unmarshal(raw, &events); err != nil {
			return nil, fmt.Errorf("hooks.json hooks: %v", err)
		}
	}
	for ev, groups := range events {
		var kept []map[string]json.RawMessage
		for _, g := range groups {
			var hs []json.RawMessage
			if raw, ok := g["hooks"]; ok {
				if err := json.Unmarshal(raw, &hs); err != nil {
					return nil, fmt.Errorf("hooks.json %s: %v", ev, err)
				}
			}
			var keep []json.RawMessage
			for _, h := range hs {
				var c struct {
					Command string `json:"command"`
				}
				json.Unmarshal(h, &c)
				if !namesHooksDir(c.Command, dir) {
					keep = append(keep, h)
				}
			}
			if len(keep) == 0 && len(hs) > 0 {
				continue
			}
			if len(keep) != len(hs) {
				b, _ := marshalRaw(keep)
				g["hooks"] = b
			}
			kept = append(kept, g)
		}
		events[ev] = kept
	}

	type key struct {
		ev string
		gi int
	}
	var order []key
	byGroup := map[key][]codexHandler{}
	matchers := map[key]string{}
	for _, h := range handlers {
		if !h.shared("codex") {
			continue
		}
		k := key{h.Event, h.Group}
		if _, ok := byGroup[k]; !ok {
			order = append(order, k)
			matchers[k] = h.Matcher
		}
		byGroup[k] = append(byGroup[k], codexHandler{Type: "command", Command: h.wrapped(exe, "codex"), Timeout: h.timeout()})
	}
	for _, k := range order {
		g := map[string]json.RawMessage{}
		g["hooks"], _ = marshalRaw(byGroup[k])
		if matchers[k] != "" {
			g["matcher"], _ = marshalRaw(codexMatcher(matchers[k]))
		}
		events[k.ev] = append(events[k.ev], g)
	}
	for ev, groups := range events {
		if len(groups) == 0 {
			delete(events, ev)
		}
	}
	b, err := marshalRaw(events)
	if err != nil {
		return nil, err
	}
	top["hooks"] = b
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(top); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- pi ----

// piExtensionPath puts the extension in pi's base home when the config has a
// [tools.pi] block, as Codex's entries go in its base; profile homes reach it
// through `shared`. Without the block it goes where pi itself looks.
func piExtensionPath(cfg *Config) string {
	if t, ok := cfg.Tools["pi"]; ok {
		return filepath.Join(t.Base, "extensions", "kennel-hooks.ts")
	}
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		dir = filepath.Join(home(), ".pi", "agent")
	}
	return filepath.Join(dir, "extensions", "kennel-hooks.ts")
}

// piExtension is the one pi extension. It asks kennel for the shared
// handlers when it loads and runs each through `kennel hook pi`, so a change
// to Claude's hook list or tags reaches pi on its next start without a
// rewrite of this file.
func piExtension(exe string) []byte {
	return []byte(strings.ReplaceAll(piExtensionSource, "@KENNEL@", jsString(exe)))
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

const piExtensionSource = `// Written by ` + "`kennel hooks apply`" + `; a later apply overwrites it.
// Runs the Claude Code hooks tagged portable or adapter from pi's tool_call
// and tool_result events, each through ` + "`kennel hook pi`" + `.
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { spawn, spawnSync } from "node:child_process";

const KENNEL = @KENNEL@;
const CLAUDE_NAME: Record<string, string> = { bash: "Bash", edit: "Edit", write: "Write", read: "Read" };

type Handler = { event: string; matcher: string; command: string };
type Verdict = { decision?: string; reason?: string; context?: string; input?: Record<string, unknown> };

function matches(h: Handler, tool: string): boolean {
	if (!h.matcher || h.matcher === "*") return true;
	return new RegExp("^(?:" + h.matcher + ")$").test(tool);
}

// run rejects on any failure, and pi blocks a tool call whose handler throws.
function run(command: string, payload: object): Promise<Verdict> {
	return new Promise((resolve, reject) => {
		const child = spawn("/bin/sh", ["-c", command], { stdio: ["pipe", "pipe", "pipe"] });
		let out = "";
		let err = "";
		child.stdout.on("data", (d) => (out += d));
		child.stderr.on("data", (d) => (err += d));
		child.on("error", reject);
		child.on("close", (code) => {
			if (code !== 0) return reject(new Error("kennel hook exited " + code + ": " + err.trim()));
			try {
				resolve(out.trim() ? JSON.parse(out) : {});
			} catch (e) {
				reject(e);
			}
		});
		child.stdin.end(JSON.stringify(payload));
	});
}

export default function (pi: ExtensionAPI) {
	let handlers: Handler[] = [];
	let loadError = "";
	const listed = spawnSync(KENNEL, ["hooks", "list", "pi"], { encoding: "utf8" });
	try {
		if (listed.status !== 0) throw new Error((listed.stderr || String(listed.error)).trim());
		handlers = JSON.parse(listed.stdout);
	} catch (e) {
		loadError = String((e as Error).message ?? e);
	}

	pi.on("session_start", async (_event, ctx) => {
		if (loadError) ctx.ui.notify("kennel hooks did not load: " + loadError, "error");
	});

	pi.on("tool_call", async (event, ctx) => {
		// Without the list no guard runs, so every call is blocked until it loads.
		if (loadError) return { block: true, reason: "kennel hooks did not load: " + loadError };
		const tool = CLAUDE_NAME[event.toolName] ?? event.toolName;
		for (const h of handlers) {
			if (h.event !== "PreToolUse" || !matches(h, tool)) continue;
			const payload = { hook_event_name: "PreToolUse", tool_name: event.toolName, tool_input: event.input, cwd: ctx.cwd, session_id: ctx.sessionManager.getSessionId() };
			const v = await run(h.command, payload);
			if (v.decision === "deny") return { block: true, reason: v.reason };
			if (v.decision === "ask" && !(ctx.hasUI && (await ctx.ui.confirm("Hook asks", v.reason ?? "")))) {
				return { block: true, reason: v.reason };
			}
			if (v.input) Object.assign(event.input, v.input);
		}
		return undefined;
	});

	pi.on("tool_result", async (event, ctx) => {
		const tool = CLAUDE_NAME[event.toolName] ?? event.toolName;
		const notes: string[] = [];
		for (const h of handlers) {
			if (h.event !== "PostToolUse" || !matches(h, tool)) continue;
			const payload = { hook_event_name: "PostToolUse", tool_name: event.toolName, tool_input: event.input, tool_response: event.content, cwd: ctx.cwd, session_id: ctx.sessionManager.getSessionId() };
			try {
				const v = await run(h.command, payload);
				if (v.context) notes.push(v.context);
			} catch (e) {
				ctx.ui.notify("kennel hook failed: " + String((e as Error).message ?? e), "error");
			}
		}
		if (notes.length === 0) return undefined;
		return { content: [...(event.content ?? []), { type: "text", text: notes.join("\n") }] };
	});
}
`

// ---- plan, apply, list ----

// hookOutput is one file kennel generates and what is on disk now.
type hookOutput struct {
	Target  string
	Path    string
	Want    []byte
	Current []byte
	Exists  bool
}

func (o hookOutput) changed() bool { return !o.Exists || !bytes.Equal(o.Want, o.Current) }

// planHooks computes every file the opt-in asks for. It writes nothing.
func planHooks(cfg *Config, handlers []hookHandler, exe string) ([]hookOutput, error) {
	var out []hookOutput
	if cfg.Hooks.Codex {
		path := codexHooksPath(cfg)
		cur, err := os.ReadFile(path)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		want, err := codexHooksJSON(cur, handlers, hooksDir(cfg), exe)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		out = append(out, hookOutput{Target: "codex", Path: path, Want: want, Current: cur, Exists: exists})
	}
	if cfg.Hooks.Pi {
		path := piExtensionPath(cfg)
		cur, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		out = append(out, hookOutput{Target: "pi", Path: path, Want: piExtension(exe), Current: cur, Exists: err == nil})
	}
	return out, nil
}

// writeAtomic replaces the file a path resolves to (following a symlink, so
// a profile home's link to the base file stays a link) with data.
func writeAtomic(path string, data []byte) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".kennel-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find this binary's path: %v", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

func cmdHooks(cfg *Config, args []string) int {
	if len(args) == 0 || (args[0] != "plan" && args[0] != "apply" && args[0] != "list") {
		return failf("usage: kennel hooks plan|apply|list <codex|pi>")
	}
	if _, ok := cfg.Tools["claude"]; !ok {
		return failf("kennel hooks needs a [tools.claude] block in %s", cfg.Path)
	}
	handlers, err := readClaudeHooks(cfg)
	if err != nil {
		return failf("%v", err)
	}
	exe, err := selfPath()
	if err != nil {
		return failf("%v", err)
	}
	switch args[0] {
	case "list":
		if len(args) != 2 || (args[1] != "codex" && args[1] != "pi") {
			return failf("usage: kennel hooks list codex|pi")
		}
		type item struct {
			Event   string `json:"event"`
			Matcher string `json:"matcher"`
			Command string `json:"command"`
			Script  string `json:"script"`
			Tag     string `json:"tag"`
		}
		items := []item{}
		for _, h := range handlers {
			if h.shared(args[1]) {
				items = append(items, item{h.Event, h.Matcher, h.wrapped(exe, args[1]), h.Script, h.Tag})
			}
		}
		b, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(b))
		return 0
	case "plan":
		return hooksPlan(cfg, handlers, exe)
	}
	if !cfg.Hooks.Codex && !cfg.Hooks.Pi {
		fmt.Println("nothing to do: [hooks] in the config opts into neither codex nor pi")
		return 0
	}
	outs, err := planHooks(cfg, handlers, exe)
	if err != nil {
		return failf("%v", err)
	}
	for _, o := range outs {
		if !o.changed() {
			fmt.Printf("unchanged %s\n", tilde(o.Path))
			continue
		}
		if err := writeAtomic(o.Path, o.Want); err != nil {
			return failf("write %s: %v", o.Path, err)
		}
		logf("hooks: wrote %s for %s (%d bytes)", o.Path, o.Target, len(o.Want))
		fmt.Printf("wrote     %s\n", tilde(o.Path))
		if o.Target == "codex" {
			fmt.Println("          new or changed entries run once trusted in Codex's /hooks")
		}
	}
	return reportUnshared(cfg)
}

// unsharedHookHomes names each profile home that never sees a generated hook
// file: the file goes in the tool's base, and a profile home reaches it only
// through the base entry that holds it (hooks.json, extensions) being in
// the shared list that home uses: its profile's own, else the tool's.
func unsharedHookHomes(cfg *Config) []string {
	var out []string
	for _, w := range []struct {
		tool, entry string
		on          bool
	}{{"codex", "hooks.json", cfg.Hooks.Codex}, {"pi", "extensions", cfg.Hooks.Pi}} {
		t, ok := cfg.Tools[w.tool]
		if !w.on || !ok {
			continue
		}
		for _, prof := range cfg.profileNames() {
			h := cfg.Profiles[prof][w.tool]
			items, own := cfg.sharedItems(prof, w.tool)
			if h == "" || slices.Contains(items, w.entry) {
				continue
			}
			list := fmt.Sprintf("[tools.%s] shared", w.tool)
			if own {
				list = fmt.Sprintf("[profiles.%s] %s shared", prof, w.tool)
			}
			out = append(out, fmt.Sprintf("[profiles.%s] %s home %s never sees %s: add %q to %s", prof, w.tool, tilde(h), tilde(filepath.Join(t.Base, w.entry)), w.entry, list))
		}
	}
	return out
}

// reportUnshared prints each unshared home and returns the exit status.
func reportUnshared(cfg *Config) int {
	missed := unsharedHookHomes(cfg)
	for _, m := range missed {
		fmt.Fprintf(os.Stderr, "kennel: %s\n", m)
	}
	if len(missed) > 0 {
		return 1
	}
	return 0
}

func hooksPlan(cfg *Config, handlers []hookHandler, exe string) int {
	fmt.Printf("Opt-in ([hooks] in %s): codex=%v pi=%v\n", tilde(cfg.Path), cfg.Hooks.Codex, cfg.Hooks.Pi)
	fmt.Printf("Claude hooks (%s)\n", tilde(filepath.Join(cfg.Tools["claude"].Base, "settings.json")))
	problems := 0
	for _, h := range handlers {
		if h.Script == "" {
			continue
		}
		where := []string{}
		for _, t := range []string{"codex", "pi"} {
			if h.shared(t) {
				where = append(where, t)
			}
		}
		to := "claude only"
		if len(where) > 0 {
			to = "→ " + strings.Join(where, ", ")
		}
		m := h.Matcher
		if m == "" {
			m = "*"
		}
		line := fmt.Sprintf("  %-11s %-18s %-12s %-40s %s", h.Event, trunc(m, 18), h.Tag, filepath.Base(h.Script), to)
		if h.Problem != "" {
			line += "  [" + h.Problem + "]"
			problems++
		}
		fmt.Println(line)
	}
	outs, err := planHooks(cfg, handlers, exe)
	if err != nil {
		return failf("%v", err)
	}
	fmt.Println("Files")
	if len(outs) == 0 {
		fmt.Println("  (opted into neither; apply writes nothing)")
	}
	for _, o := range outs {
		state := "unchanged"
		if o.changed() {
			state = "would write"
		}
		fmt.Printf("  %-11s %s\n", state, tilde(o.Path))
	}
	if problems > 0 {
		fmt.Fprintf(os.Stderr, "kennel: %d hook script(s) unreadable or carrying an unknown tag; they stay claude-only\n", problems)
		return 1
	}
	return reportUnshared(cfg)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
