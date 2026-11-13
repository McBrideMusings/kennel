package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// harnessSandbox builds a throwaway HOME with a work profile whose Claude
// home is ~/.claude-work, a git project routed to it, and skills, guidance,
// permissions, hooks and MCP servers at both scopes.
func harnessSandbox(t *testing.T) (h, proj string) {
	t.Helper()
	h = t.TempDir()
	h, _ = filepath.EvalSymlinks(h)
	t.Setenv("HOME", h)
	t.Setenv("KENNEL_CONFIG_DIR", filepath.Join(h, ".cfg"))
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	write := func(p, body string) {
		p = filepath.Join(h, p)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".cfg/config.toml", `[tools.claude]
base = "~/.claude"
shared = []
[tools.codex]
base = "~/.codex"
shared = []
[hooks]
codex = true
[profiles.personal]
[profiles.work]
claude = "~/.claude-work"
`)
	write(".cfg/routes", "~/Work work\n")
	write(".claude/skills/admin/SKILL.md", "global admin\n")
	write(".claude-work/skills/diagnose/SKILL.md", "work diagnose\n")
	write(".claude-work/skills/diagnose/refs/a.md", "ref\n")
	write(".claude-work/CLAUDE.md", "work guidance\n")
	write(".claude/settings.json", `{"permissions":{"allow":["Bash(ls:*)"]},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"bash ~/.claude/hooks/guard.sh"}]}]}}`)
	write(".claude/hooks/guard.sh", "#!/bin/bash\n# kennel-hook: portable\n")
	write(".claude-work/settings.json", `{"permissions":{"allow":["Read"],"deny":["Bash(rm:*)"]}}`)
	write(".claude.json", `{"mcpServers":{"rea":{"command":"rea","args":["serve"]}}}`)
	write(".claude-work/.claude.json", `{"mcpServers":{"qmd":{"command":"qmd"}},"projects":{"`+filepath.Join(h, "Work", "poker")+`":{"mcpServers":{"local":{"type":"http","url":"http://x"}}}}}`)
	write(".codex/config.toml", "approval_policy = \"on-request\"\n[mcp_servers.pencil]\ncommand = \"pencil\"\n")
	write(".codex/rules/default.rules", "prefix_rule(pattern=[\"go\", \"build\"], decision=\"allow\")\nprefix_rule(\n  pattern=[\"git\", [\"push\", \"reset\"]],\n  decision=\"prompt\",\n)\n")
	write(".codex/hooks.json", `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"'/bin/kennel' hook codex -- /bin/sh -c 'bash ~/.claude/hooks/guard.sh'"}]}],"Stop":[{"hooks":[{"type":"command","command":"moshi notify"}]}]}}`)

	proj = filepath.Join(h, "Work", "poker")
	write("Work/poker/CLAUDE.md", "project guidance\n")
	write("Work/poker/apps/web/CLAUDE.md", "nested guidance\n")
	write("Work/poker/.claude/rules/style.md", "rule\n")
	write("Work/poker/.claude/skills/chat-mod/SKILL.md", "project skill\n")
	write("Work/poker/.claude/settings.json", `{"permissions":{"ask":["Bash(git push:*)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/stop.sh"}]}]}}`)
	write("Work/poker/.claude/hooks/stop.sh", "#!/bin/bash\n")
	write("Work/poker/.mcp.json", `{"mcpServers":{"playwright":{"command":"npx"}}}`)
	write("Work/poker/CLAUDE.local.md", "mine\n")
	write("Work/poker/.gitignore", "CLAUDE.local.md\nnode_modules/\n")
	write("Work/poker/node_modules/x/CLAUDE.md", "never listed\n")
	gitT(t, proj, "init", "-q", "-b", "main")
	gitT(t, proj, "add", "-A")
	gitT(t, proj, "commit", "-q", "-m", "first")
	return h, proj
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func showJSON(t *testing.T, args ...string) harnessShowOut {
	t.Helper()
	out, code := quiet(t, func() int { return run(append([]string{"harness", "show"}, append(args, "--json")...)) })
	if code != 0 {
		t.Fatalf("harness show %v exited %d: %s", args, code, out)
	}
	var s harnessShowOut
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("harness show %v: %v\n%s", args, err, out)
	}
	return s
}

type harnessShowOut struct {
	Scope  harnessScope `json:"scope"`
	Blocks []struct {
		Scope   string           `json:"scope"`
		Path    string           `json:"path"`
		Entries []map[string]any `json:"entries"`
	} `json:"blocks"`
}

// names collects one field from each entry of each block, per block.
func (s harnessShowOut) names(field string) [][]string {
	var out [][]string
	for _, b := range s.Blocks {
		var ns []string
		for _, e := range b.Entries {
			v, _ := json.Marshal(e[field])
			ns = append(ns, strings.Trim(string(v), `"`))
		}
		out = append(out, ns)
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }

func TestHarnessShowSkillsProjectAndGlobal(t *testing.T) {
	h, proj := harnessSandbox(t)
	s := showJSON(t, "skills", "--scope", filepath.Join(proj, "apps"))
	if s.Scope.Kind != "project" || s.Scope.Profile != "work" || s.Scope.Root != proj || s.Scope.Home != filepath.Join(h, ".claude-work") {
		t.Fatalf("scope = %+v", s.Scope)
	}
	n := s.names("name")
	if len(n) != 2 || !eq(n[0], []string{"chat-mod"}) || !eq(n[1], []string{"diagnose"}) {
		t.Fatalf("skills blocks = %q", n)
	}
	if files := s.Blocks[1].Entries[0]["files"]; len(files.([]any)) != 2 {
		t.Fatalf("diagnose files = %v", files)
	}

	g := showJSON(t, "skills", "--scope", "global")
	if gn := g.names("name"); len(gn) != 1 || g.Blocks[0].Scope != "global" || !eq(gn[0], []string{"admin"}) {
		t.Fatalf("global skills = %q", gn)
	}
}

func TestHarnessShowGuidance(t *testing.T) {
	_, proj := harnessSandbox(t)
	n := showJSON(t, "guidance", "--scope", proj).names("rel")
	want := []string{".claude/rules/style.md", "CLAUDE.local.md", "CLAUDE.md", "apps/web/CLAUDE.md"}
	if !eq(n[0], want) || !eq(n[1], []string{"CLAUDE.md"}) {
		t.Fatalf("guidance = %q", n)
	}
}

func TestHarnessShowPermissions(t *testing.T) {
	_, proj := harnessSandbox(t)
	s := showJSON(t, "permissions", "--scope", proj)
	if r, d := s.names("rule"), s.names("decision"); !eq(r[0], []string{"Bash(git push:*)"}) || !eq(d[0], []string{"ask"}) ||
		!eq(r[1], []string{"Read", "Bash(rm:*)"}) || !eq(d[1], []string{"allow", "deny"}) {
		t.Fatalf("claude permissions = %q %q", r, d)
	}
	c := showJSON(t, "permissions", "--harness", "codex")
	if r, d := c.names("rule"), c.names("decision"); !eq(r[0], []string{"approval_policy = on-request", "go build", "git [push|reset]"}) ||
		!eq(d[0], []string{"setting", "allow", "ask"}) {
		t.Fatalf("codex permissions = %q %q", r, d)
	}
}

func TestHarnessShowHooks(t *testing.T) {
	h, proj := harnessSandbox(t)
	s := showJSON(t, "hooks", "--scope", proj)
	if sc := s.names("script"); !eq(sc[0], []string{filepath.Join(proj, ".claude", "hooks", "stop.sh")}) {
		t.Fatalf("project hook scripts = %q", sc)
	}
	g := showJSON(t, "hooks")
	if sc, tag := g.names("script"), g.names("tag"); !eq(sc[0], []string{filepath.Join(h, ".claude", "hooks", "guard.sh")}) || !eq(tag[0], []string{"portable"}) {
		t.Fatalf("global hooks = %q %q", sc, tag)
	}
	c := showJSON(t, "hooks", "--harness", "codex")
	if ev, gen := c.names("event"), c.names("generated"); !eq(ev[0], []string{"PreToolUse", "Stop"}) || !eq(gen[0], []string{"true", "false"}) {
		t.Fatalf("codex hooks = %q %q", ev, gen)
	}
}

func TestHarnessShowHooksWithoutClaudeTool(t *testing.T) {
	h, _ := harnessSandbox(t)
	if err := os.WriteFile(filepath.Join(h, ".cfg", "config.toml"), []byte("[tools.codex]\nbase = \"~/.codex\"\nshared = []\n[profiles.personal]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := showJSON(t, "hooks", "--harness", "codex")
	if gen := c.names("generated"); !eq(gen[0], []string{"true", "false"}) {
		t.Fatalf("codex hooks with no [tools.claude] = %q", gen)
	}
	if sc := showJSON(t, "hooks").names("script"); !eq(sc[0], []string{filepath.Join(h, ".claude", "hooks", "guard.sh")}) {
		t.Fatalf("claude hooks with no [tools.claude] = %q", sc)
	}
}

func TestHarnessShowScopeLinkedWorktree(t *testing.T) {
	h, proj := harnessSandbox(t)
	wt := filepath.Join(h, "elsewhere", "poker-wt")
	gitT(t, proj, "worktree", "add", "-q", "-b", "wt", wt)
	s := showJSON(t, "skills", "--scope", filepath.Join(wt, ".claude"))
	if s.Scope.Profile != "work" || s.Scope.Root != wt {
		t.Fatalf("worktree scope = %+v", s.Scope)
	}
}

func TestHarnessShowMCP(t *testing.T) {
	_, proj := harnessSandbox(t)
	n := showJSON(t, "mcp", "--scope", proj).names("name")
	if !eq(n[0], []string{"playwright", "local"}) || !eq(n[1], []string{"qmd"}) {
		t.Fatalf("project mcp = %q", n)
	}
	if g := showJSON(t, "mcp").names("name"); !eq(g[0], []string{"rea"}) {
		t.Fatalf("global mcp = %q", g)
	}
	if c := showJSON(t, "mcp", "--harness", "codex").names("name"); !eq(c[0], []string{"pencil"}) {
		t.Fatalf("codex mcp = %q", c)
	}
}

func TestFileLogAndShow(t *testing.T) {
	_, proj := harnessSandbox(t)
	f := filepath.Join(proj, "CLAUDE.md")
	first := strings.TrimSpace(gitT(t, proj, "rev-parse", "HEAD"))
	os.WriteFile(f, []byte("changed\n"), 0o644)
	gitT(t, proj, "commit", "-q", "-am", "second")

	out, code := quiet(t, func() int { return run([]string{"file", "log", f, "--json"}) })
	var commits []fileCommit
	if code != 0 || json.Unmarshal([]byte(out), &commits) != nil || len(commits) != 2 || commits[0].Subject != "second" || commits[1].Commit != first {
		t.Fatalf("file log = %d %s", code, out)
	}
	if out, code := quiet(t, func() int { return run([]string{"file", "log", filepath.Join(proj, "CLAUDE.local.md"), "--json"}) }); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("file log on an untracked file = %d %q", code, out)
	}
	if out, code := quiet(t, func() int { return run([]string{"file", "show", f + "@" + first[:7]}) }); code != 0 || out != "project guidance\n" {
		t.Fatalf("file show at first = %d %q", code, out)
	}
	if out, code := quiet(t, func() int { return run([]string{"file", "show", f}) }); code != 0 || out != "changed\n" {
		t.Fatalf("file show now = %d %q", code, out)
	}
}
