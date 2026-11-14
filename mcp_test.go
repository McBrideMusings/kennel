package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain lets a test start this binary as kennel: with KENNEL_TEST_MAIN=1
// it runs the CLI on its arguments instead of the tests. kennel mcp runs
// its own binary for every command, so the server's children land here too.
func TestMain(m *testing.M) {
	if os.Getenv("KENNEL_TEST_MAIN") == "1" {
		os.Exit(run(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// mcpSession starts kennel mcp over stdio and connects a client to it.
func mcpSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	cmd := exec.Command(os.Args[0], "mcp")
	cmd.Env = append(os.Environ(), "KENNEL_TEST_MAIN=1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return r
}

func toolText(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func structured(t *testing.T, r *mcp.CallToolResult) harnessShowOut {
	t.Helper()
	if r.IsError {
		t.Fatalf("tool error: %s", toolText(r))
	}
	b, _ := json.Marshal(r.StructuredContent)
	var s harnessShowOut
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return s
}

func TestMCPHarnessToolResolvesScope(t *testing.T) {
	h, proj := harnessSandbox(t)
	cs := mcpSession(t)

	s := structured(t, callTool(t, cs, "harness", map[string]any{"directory": filepath.Join(proj, "apps")}))
	if s.Scope.Kind != "project" || s.Scope.Profile != "work" || s.Scope.Root != proj || s.Scope.Home != filepath.Join(h, ".claude-work") {
		t.Fatalf("routed directory scope = %+v", s.Scope)
	}
	if n := s.names("name"); len(n) != 2 || !eq(n[0], []string{"chat-mod"}) || !eq(n[1], []string{"diagnose"}) {
		t.Fatalf("routed directory skills = %q", n)
	}

	r := callTool(t, cs, "harness", map[string]any{})
	g := structured(t, r)
	if g.Scope.Kind != "global" || g.Scope.Profile != "" || g.Scope.Home != filepath.Join(h, ".claude") {
		t.Fatalf("no-argument scope = %+v", g.Scope)
	}
	if !strings.Contains(toolText(r), "global claude") {
		t.Fatalf("no-argument text = %q", toolText(r))
	}

	c := structured(t, callTool(t, cs, "harness", map[string]any{"harness": "codex"}))
	if c.Scope.Kind != "global" || c.Scope.Home != filepath.Join(h, ".codex") {
		t.Fatalf("codex scope = %+v", c.Scope)
	}

	if r := callTool(t, cs, "harness", map[string]any{"directory": filepath.Join(h, "nowhere")}); !r.IsError || !strings.Contains(toolText(r), "is not a folder") {
		t.Fatalf("missing folder = %v %q", r.IsError, toolText(r))
	}
}

func TestMCPToolsAndAppResource(t *testing.T) {
	harnessSandbox(t)
	cs := mcpSession(t)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ui := map[string]map[string]any{}
	for _, tl := range tools.Tools {
		ui[tl.Name], _ = tl.Meta["ui"].(map[string]any)
	}
	if ui["harness"]["resourceUri"] != appURI || ui["harness"]["visibility"] != nil {
		t.Fatalf("harness ui meta = %v", ui["harness"])
	}
	if v, _ := json.Marshal(ui["kennel"]["visibility"]); string(v) != `["app"]` {
		t.Fatalf("kennel visibility = %s", v)
	}

	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: appURI})
	if err != nil {
		t.Fatal(err)
	}
	page := res.Contents[0]
	if page.MIMEType != appMIME || !strings.Contains(page.Text, "marked v") || strings.Contains(page.Text, "/*MARKED*/") || strings.Contains(page.Text, "{{") {
		t.Fatalf("app page: mime %q, %d bytes", page.MIMEType, len(page.Text))
	}
}

func TestMCPKennelToolRunsOnlyAppCommands(t *testing.T) {
	h, proj := harnessSandbox(t)
	cs := mcpSession(t)

	guidance := filepath.Join(proj, "CLAUDE.md")
	if r := callTool(t, cs, "kennel", map[string]any{"args": []string{"file", "show", guidance}}); !r.IsError || !strings.Contains(toolText(r), "not a file the app has listed") {
		t.Fatalf("file show before any harness show = %v %q", r.IsError, toolText(r))
	}
	if r := callTool(t, cs, "kennel", map[string]any{"args": []string{"harness", "show", "guidance", "--scope", proj, "--json"}}); r.IsError {
		t.Fatalf("harness show guidance: %s", toolText(r))
	}
	r := callTool(t, cs, "kennel", map[string]any{"args": []string{"file", "show", guidance}})
	if r.IsError || toolText(r) != "project guidance\n" {
		t.Fatalf("file show of a listed file = %v %q", r.IsError, toolText(r))
	}
	r = callTool(t, cs, "kennel", map[string]any{"args": []string{"file", "log", guidance, "--json"}})
	if r.IsError || !strings.Contains(toolText(r), `"subject": "first"`) {
		t.Fatalf("file log of a listed file = %v %q", r.IsError, toolText(r))
	}
	for _, f := range []string{filepath.Join(h, ".cfg", "routes"), filepath.Join(proj, ".gitignore")} {
		if r := callTool(t, cs, "kennel", map[string]any{"args": []string{"file", "show", f}}); !r.IsError || !strings.Contains(toolText(r), "not a file the app has listed") {
			t.Fatalf("file show %s = %v %q", f, r.IsError, toolText(r))
		}
	}
	// a skill's files are listed by the harness tool itself
	callTool(t, cs, "harness", map[string]any{"directory": proj})
	if r := callTool(t, cs, "kennel", map[string]any{"args": []string{"file", "show", filepath.Join(proj, ".claude", "skills", "chat-mod", "SKILL.md") + "@HEAD"}}); r.IsError || toolText(r) != "project skill\n" {
		t.Fatalf("file show of a listed skill file at HEAD = %v %q", r.IsError, toolText(r))
	}
	for _, args := range [][]string{
		{"harness", "show", "guidance", "--scope", proj, "--json", "extra"},
		{"harness", "show", "guidance", "--json", "--json"},
		{"harness", "show", "guidance", "--scope"},
		{"harness", "show", "guidance", "--scope=" + proj},
		{"harness", "show", "nope"},
		{"harness", "show"},
		{"harness", "recent", "--json", "extra"},
		{"harness", "recent", "--help"},
	} {
		if r := callTool(t, cs, "kennel", map[string]any{"args": args}); !r.IsError || !strings.Contains(toolText(r), "not a command the app runs") {
			t.Fatalf("%v = %v %q", args, r.IsError, toolText(r))
		}
	}
	if r := callTool(t, cs, "kennel", map[string]any{"args": []string{"harness", "show", "skills", "--harness", "codex", "--scope", proj, "--json"}}); r.IsError {
		t.Fatalf("harness show in the app's shape: %s", toolText(r))
	}
	r = callTool(t, cs, "kennel", map[string]any{"args": []string{"route", "add", proj, "personal"}})
	if !r.IsError || !strings.Contains(toolText(r), "not a command the app runs") {
		t.Fatalf("route add = %v %q", r.IsError, toolText(r))
	}
	if b, _ := os.ReadFile(filepath.Join(h, ".cfg", "routes")); string(b) != "~/Work work\n" {
		t.Fatalf("routes changed: %q", b)
	}
}

// The Permissions, Hooks and MCP servers pages open the file each entry
// names, which their harness show listed.
func TestMCPKennelToolReadsEntryFiles(t *testing.T) {
	h, proj := harnessSandbox(t)
	cs := mcpSession(t)
	pages := []struct{ section, file, want string }{
		{"permissions", filepath.Join(proj, ".claude", "settings.json"), "git push"},
		{"hooks", filepath.Join(proj, ".claude", "hooks", "stop.sh"), "#!/bin/bash"},
		{"mcp", filepath.Join(proj, ".mcp.json"), "playwright"},
		{"mcp", filepath.Join(h, ".claude-work", ".claude.json"), "qmd"},
	}
	shown := map[string]bool{}
	for _, p := range pages {
		if r := callTool(t, cs, "kennel", map[string]any{"args": []string{"file", "show", p.file}}); !shown[p.section] && !r.IsError {
			t.Fatalf("file show %s before harness show %s = %q", p.file, p.section, toolText(r))
		}
		if r := callTool(t, cs, "kennel", map[string]any{"args": []string{"harness", "show", p.section, "--scope", proj, "--json"}}); r.IsError {
			t.Fatalf("harness show %s: %s", p.section, toolText(r))
		}
		shown[p.section] = true
		if r := callTool(t, cs, "kennel", map[string]any{"args": []string{"file", "show", p.file}}); r.IsError || !strings.Contains(toolText(r), p.want) {
			t.Fatalf("file show %s after harness show %s = %v %q", p.file, p.section, r.IsError, toolText(r))
		}
	}
}

func TestHarnessRecent(t *testing.T) {
	h, proj := harnessSandbox(t)
	t.Setenv("TMPDIR", filepath.Join(h, "tmp")) // the sandbox itself lives in the real temp folder
	write := func(p, body string, age time.Duration) {
		p = filepath.Join(h, p)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
		mt := time.Now().Add(-age)
		os.Chtimes(p, mt, mt)
	}
	line := func(dir string) string { return `{"type":"summary"}` + "\n" + `{"cwd":"` + dir + `"}` + "\n" }
	os.MkdirAll(filepath.Join(h, "Projects", "kennel"), 0o755)
	os.MkdirAll(filepath.Join(h, ".worktrees", "kennel", "x"), 0o755)
	write(".claude/projects/a/old.jsonl", line(filepath.Join(h, "Projects", "kennel")), 3*time.Hour)
	write(".claude-work/projects/b/s.jsonl", line(proj), time.Hour)
	write(".claude/projects/c/s.jsonl", line(filepath.Join(h, ".worktrees", "kennel", "x")), 0)
	write(".claude/projects/d/s.jsonl", line("/private/tmp/x"), 0)
	write(".claude/projects/e/s.jsonl", line(filepath.Join(h, "gone")), 0)
	os.MkdirAll(filepath.Join(h, "Library", "App", "scratch"), 0o755)
	write(".claude/projects/f/s.jsonl", line(filepath.Join(h, "Library", "App", "scratch")), 0)
	write(".claude/projects/-private-tmp-x/s.jsonl", line(filepath.Join(h, "Projects", "kennel")), 0) // skipped by its name alone

	out, code := quiet(t, func() int { return run([]string{"harness", "recent", "--json"}) })
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var dirs []recentDir
	if err := json.Unmarshal([]byte(out), &dirs); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range dirs {
		got = append(got, d.Short)
	}
	if !eq(got, []string{"~/Work/poker", "~/Projects/kennel"}) {
		t.Fatalf("recent = %q", got)
	}
}

// The app's vault actions take only paths a harness show listed: a skill
// folder for add and unlink, a skills folder for copy, an opened project for
// install --project. Each one then does what its CLI command does.
func TestMCPVaultActionsTakeOnlyListedPaths(t *testing.T) {
	h, proj := harnessSandbox(t)
	cs := mcpSession(t)
	run := func(args ...string) *mcp.CallToolResult {
		return callTool(t, cs, "kennel", map[string]any{"args": args})
	}
	admin := filepath.Join(h, ".claude", "skills", "admin")
	if r := run("vault", "add", admin); !r.IsError || !strings.Contains(toolText(r), "not a skill the app has listed") {
		t.Fatalf("vault add before a show = %v %q", r.IsError, toolText(r))
	}
	if !exists(filepath.Join(admin, "SKILL.md")) {
		t.Fatal("a refused vault add moved the skill")
	}
	callTool(t, cs, "harness", map[string]any{})
	if r := run("vault", "add", admin); r.IsError {
		t.Fatalf("vault add of a listed skill: %s", toolText(r))
	}
	if got, _ := os.Readlink(admin); got != filepath.Join(h, ".local", "share", "kennel", "vault", "skills", "admin") {
		t.Fatalf("after vault add the skill links to %q", got)
	}
	r := run("vault", "ls", "--json")
	if r.IsError || !strings.Contains(toolText(r), `"item": "skills/admin"`) {
		t.Fatalf("vault ls = %v %q", r.IsError, toolText(r))
	}
	vfile := filepath.Join(h, ".local", "share", "kennel", "vault", "skills", "admin", "SKILL.md")
	if r := run("file", "show", vfile); r.IsError || toolText(r) != "global admin\n" {
		t.Fatalf("file show of a vault ls file = %v %q", r.IsError, toolText(r))
	}

	if r := run("vault", "install", "skills/admin", "claude", "--project", proj); !r.IsError || !strings.Contains(toolText(r), "not a project the app has opened") {
		t.Fatalf("install into an unopened project = %v %q", r.IsError, toolText(r))
	}
	callTool(t, cs, "harness", map[string]any{"directory": proj})
	if r := run("vault", "install", "skills/admin", "claude", "--project", proj); r.IsError {
		t.Fatalf("install into the opened project: %s", toolText(r))
	}
	if r := run("vault", "copy", "skills/admin", filepath.Join(h, "elsewhere", "admin")); !r.IsError || !strings.Contains(toolText(r), "not in a skills folder the app has listed") {
		t.Fatalf("copy outside a listed skills folder = %v %q", r.IsError, toolText(r))
	}
	if r := run("vault", "rm", "skills/never-listed"); !r.IsError || !strings.Contains(toolText(r), "not a vault item the app has listed") {
		t.Fatalf("rm of an unlisted item = %v %q", r.IsError, toolText(r))
	}
	if r := run("vault", "copy", "skills/admin", filepath.Join(proj, ".claude", "skills", "chat-mod")); !r.IsError || !strings.Contains(toolText(r), "already exists") {
		t.Fatalf("copy onto an existing skill = %v %q", r.IsError, toolText(r))
	}
	for _, args := range [][]string{{"vault", "rm", "skills/admin", "extra"}, {"vault", "commit", "x"}, {"vault", "ls", "--json", "x"}} {
		if r := run(args...); !r.IsError {
			t.Fatalf("%v ran: %q", args, toolText(r))
		}
	}

	// the global skill is listed again after the show re-read it
	callTool(t, cs, "harness", map[string]any{})
	if r := run("vault", "unlink", admin); r.IsError {
		t.Fatalf("vault unlink of a listed skill: %s", toolText(r))
	}
	if fi, _ := os.Lstat(admin); fi == nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("vault unlink left the link")
	}
	b, _ := os.ReadFile(filepath.Join(h, ".cfg", "config.toml"))
	if !strings.Contains(string(b), `"~/.claude/skills/admin" = "vault:skills/admin@`) {
		t.Fatalf("config after unlink:\n%s", b)
	}
}
