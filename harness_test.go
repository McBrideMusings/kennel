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
	Rows   []string     `json:"rows"`
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
	// the root files carry their sidebar row's label; nested files and rules carry none
	s := showJSON(t, "guidance", "--scope", proj)
	if r := s.names("row"); !eq(s.Rows, []string{"Global CLAUDE.md", "Project CLAUDE.md", "CLAUDE.local.md"}) ||
		!eq(r[0], []string{"null", "CLAUDE.local.md", "Project CLAUDE.md", "null"}) || !eq(r[1], []string{"Global CLAUDE.md"}) {
		t.Fatalf("guidance rows = %q, entry rows = %q", s.Rows, r)
	}
	if c := showJSON(t, "guidance", "--scope", "global", "--harness", "codex"); !eq(c.Rows, []string{"Global AGENTS.md", "Project AGENTS.md", "AGENTS.override.md"}) {
		t.Fatalf("codex guidance rows = %q", c.Rows)
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

// A .rules file or config.toml that fails stands as one problem entry; the
// other files still list.
func TestHarnessShowPermissionsBadRules(t *testing.T) {
	h, _ := harnessSandbox(t)
	for name, body := range map[string]string{
		"rules/alt.rules":      "prefix_rule(pattern=[\"git\", [\"push\", 1]])\n",
		"rules/broken.rules":   "prefix_rule(pattern=[\"ls\"])\nprefix_rule(pattern=[\"x\"], decision=\"maybe\")\n",
		"rules/empty.rules":    "prefix_rule(pattern=[])\n",
		"rules/emptyalt.rules": "prefix_rule(pattern=[\"git\", []])\n",
		"rules/number.rules":   "prefix_rule(pattern=[\"rm\", 3])\n",
		"rules/syntax.rules":   "prefix_rule(\n",
		"config.toml":          "approval_policy = \n",
	} {
		if err := os.WriteFile(filepath.Join(h, ".codex", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := showJSON(t, "permissions", "--harness", "codex")
	r, d, p := c.names("rule"), c.names("decision"), c.names("problem")
	if !eq(r[0], []string{"config.toml", "alt.rules", "broken.rules", "go build", "git [push|reset]", "empty.rules", "emptyalt.rules", "number.rules", "syntax.rules"}) ||
		!eq(d[0], []string{"problem", "problem", "problem", "allow", "ask", "problem", "problem", "problem", "problem"}) {
		t.Fatalf("codex permissions = %q %q", r, d)
	}
	for i, want := range map[int]string{
		0: "config.toml",
		1: "pattern word 2 has an alternative of type int, want string",
		2: "unknown decision",
		5: "empty pattern",
		6: "pattern word 2 is an empty list",
		7: "pattern word 2 has type int, want string or list",
		8: "syntax.rules",
	} {
		if !strings.Contains(p[0][i], want) {
			t.Errorf("problem %d (%s) = %q, want %q", i, r[0][i], p[0][i], want)
		}
	}
	out, code := quiet(t, func() int { return run([]string{"harness", "show", "permissions", "--harness", "codex"}) })
	if code != 0 || !strings.Contains(out, "  problem\tnumber.rules  (~/.codex/rules/number.rules: pattern word 2 has type int, want string or list)\n") {
		t.Fatalf("text output (exit %d):\n%s", code, out)
	}
}

// A Claude settings file that fails to parse stands as one problem entry; the
// other settings file still lists.
func TestHarnessShowPermissionsBadClaudeSettings(t *testing.T) {
	_, proj := harnessSandbox(t)
	bad := filepath.Join(proj, ".claude", "settings.json")
	for p, body := range map[string]string{bad: `{"permissions":`, filepath.Join(proj, ".claude", "settings.local.json"): `{"permissions":{"deny":["WebFetch"]}}`} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := showJSON(t, "permissions", "--scope", proj)
	r, d, f, p := s.names("rule"), s.names("decision"), s.names("file"), s.names("problem")
	if !eq(r[0], []string{"settings.json", "WebFetch"}) || !eq(d[0], []string{"problem", "deny"}) || f[0][0] != bad || p[0][0] == "null" || p[0][0] == "" {
		t.Fatalf("claude permissions = %q %q %q %q", r, d, f, p)
	}
	if !eq(r[1], []string{"Read", "Bash(rm:*)"}) {
		t.Fatalf("global claude permissions = %q", r[1])
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

// Codex takes hooks inline in config.toml beside hooks.json and runs both:
// each lists, hooks.json's first, with its own file.
func TestHarnessShowHooksCodexConfigTOML(t *testing.T) {
	h, proj := harnessSandbox(t)
	conf := filepath.Join(h, ".codex", "config.toml")
	body := "approval_policy = \"on-request\"\n[hooks]\nmanaged_dir = \"/x\"\n[hooks.state.\"/h/.codex/hooks.json:pre_tool_use:0:0\"]\ntrusted_hash = \"sha256:00\"\n[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = \"command\"\ncommand = 'bash ~/.claude/hooks/guard.sh'\n" +
		"[[hooks.PreToolUse]]\nmatcher = \"^Bash$\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"moshi \\\"pre\\\" now\"\n[mcp_servers.pencil]\ncommand = \"pencil\"\n"
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c := showJSON(t, "hooks", "--harness", "codex")
	if ev := c.names("event"); !eq(ev[0], []string{"PreToolUse", "Stop", "SessionStart", "PreToolUse"}) {
		t.Fatalf("codex hook events = %q", ev)
	}
	hj := filepath.Join(h, ".codex", "hooks.json")
	if f := c.names("file"); !eq(f[0], []string{hj, hj, conf, conf}) {
		t.Fatalf("codex hook files = %q", f)
	}
	if cmd, sc, gen := c.names("command"), c.names("script"), c.names("generated"); cmd[0][3] != `moshi \"pre\" now` || sc[0][2] != filepath.Join(h, ".claude", "hooks", "guard.sh") || !eq(gen[0], []string{"true", "false", "true", "false"}) {
		t.Fatalf("codex config.toml hooks = %q %q %q", cmd, sc, gen)
	}
	pconf := filepath.Join(proj, ".codex", "config.toml")
	os.MkdirAll(filepath.Dir(pconf), 0o755)
	if err := os.WriteFile(pconf, []byte("[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"moshi stop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := showJSON(t, "hooks", "--harness", "codex", "--scope", proj)
	if cmd, f := p.names("command"), p.names("file"); !eq(cmd[0], []string{"moshi stop"}) || !eq(f[0], []string{pconf}) {
		t.Fatalf("project codex config.toml hooks = %q %q", cmd, f)
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
	// each entry names the keys that lead to it in its file, so the app marks
	// the right one when a name sits in two tables of .claude.json
	p := showJSON(t, "mcp", "--scope", proj).names("keys")
	local, _ := json.Marshal([]string{"projects", proj, "mcpServers", "local"})
	if !eq(p[0], []string{`["mcpServers","playwright"]`, string(local)}) || !eq(p[1], []string{`["mcpServers","qmd"]`}) {
		t.Fatalf("project mcp keys = %q", p)
	}
	if c := showJSON(t, "mcp", "--harness", "codex").names("keys"); !eq(c[0], []string{`["mcp_servers","pencil"]`}) {
		t.Fatalf("codex mcp keys = %q", c)
	}
}

// One server name under both mcpServers and projects[<dir>].mcpServers in
// .claude.json: each entry's keys name its own block.
func TestHarnessShowMCPSameName(t *testing.T) {
	h, proj := harnessSandbox(t)
	os.WriteFile(filepath.Join(h, ".claude-work", ".claude.json"), []byte(`{"projects":{"`+proj+`":{"mcpServers":{"qmd":{"command":"qmd"}}}},"mcpServers":{"qmd":{"command":"qmd"}}}`), 0o644)
	p := showJSON(t, "mcp", "--scope", proj).names("keys")
	local, _ := json.Marshal([]string{"projects", proj, "mcpServers", "qmd"})
	if !eq(p[0], []string{`["mcpServers","playwright"]`, string(local)}) || !eq(p[1], []string{`["mcpServers","qmd"]`}) {
		t.Fatalf("mcp keys = %q", p)
	}
}

func TestRedactArgs(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"serve", "--token", "s3cret", "--api-key=s3cret", "--verbose", "-p", "8080"},
			[]string{"serve", "--token", "REDACTED", "--api-key=REDACTED", "--verbose", "-p", "8080"}},
		{[]string{"GITHUB_TOKEN=s3cret", "Authorization: Bearer s3cret", "--header", "X-Trace: on"},
			[]string{"GITHUB_TOKEN=REDACTED", "Authorization: REDACTED", "--header", "X-Trace: on"}},
		// mcp-remote's header forms, and a flag=value whose value is a pair
		{[]string{"--header", "Authorization:Bearer s3cret", "--header=Authorization: Bearer s3cret==", "--env=GITHUB_TOKEN=s3cret", "x-api-key:s3cret"},
			[]string{"--header", "Authorization:REDACTED", "--header=Authorization: REDACTED", "--env=GITHUB_TOKEN=REDACTED", "x-api-key:REDACTED"}},
		// URLs: password, user-as-token, query, fragment; a bare query key stays bare
		{[]string{"https://u:s3cret@h.example/sse?api_key=s3cret&x", "https://s3cret@github.com/x", "postgresql://s3cret@db/app",
			"https://h.example/cb#access_token=s3cret", "https://h.example/doc#usage", "--url=https://h.example/?t=s3cret"},
			[]string{"https://REDACTED@h.example/sse?api_key=REDACTED&x", "https://REDACTED@github.com/x", "postgresql://REDACTED@db/app",
				"https://h.example/cb#access_token=REDACTED", "https://h.example/doc#usage", "--url=https://h.example/?t=REDACTED"}},
		// JSON config args and shell strings
		{[]string{"--config", `{"apiKey":"s3cret","debug":true,"url":"https://h.example/?k=s3cret"}`, "-c", "export TOKEN=s3cret && run", "-c", "TOKEN=s3cret npx foo --bar"},
			[]string{"--config", `{"apiKey":"REDACTED","debug":true,"url":"https://h.example/?k=REDACTED"}`, "-c", "export TOKEN=REDACTED && run", "-c", "TOKEN=REDACTED npx foo --bar"}},
		// a secret flag's URL keeps host and path; names that only contain a word stay
		{[]string{"--auth-url", "https://idp.example/authorize?state=s3cret", "--no-auth", "server.js", "--author", "bob", "--keyfile", "./k.json", "--pat", "s3cret"},
			[]string{"--auth-url", "https://idp.example/authorize?state=REDACTED", "--no-auth", "server.js", "--author", "bob", "--keyfile", "./k.json", "--pat", "REDACTED"}},
		// env references and ordinary values stay
		{[]string{"--token", "${GH_TOKEN}", "API_KEY=$KEY", "mcp/github:latest", `C:\tools\x.exe`},
			[]string{"--token", "${GH_TOKEN}", "API_KEY=$KEY", "mcp/github:latest", `C:\tools\x.exe`}},
	}
	for _, c := range cases {
		if got := redactArgs(c.in); !eq(got, c.want) {
			t.Errorf("redactArgs\n got %q\nwant %q", got, c.want)
		}
	}
	if got := redactArg("TOKEN=s3cret mcp-server"); got != "TOKEN=REDACTED mcp-server" {
		t.Errorf("redactArg(command) = %q", got)
	}
}

func TestHarnessShowMCPRedacts(t *testing.T) {
	h, _ := harnessSandbox(t)
	os.WriteFile(filepath.Join(h, ".claude.json"), []byte(`{"mcpServers":{
		"gh":{"command":"GH_TOKEN=s3cret gh-mcp","args":["--token","s3cret"]},
		"web":{"type":"http","url":"https://h.example/mcp?key=s3cret"}}}`), 0o644)
	for _, args := range [][]string{{"mcp", "--json"}, {"mcp"}} {
		out, code := quiet(t, func() int { return run(append([]string{"harness", "show"}, args...)) })
		if code != 0 || strings.Contains(out, "s3cret") || !strings.Contains(out, "REDACTED") {
			t.Fatalf("harness show %v exited %d:\n%s", args, code, out)
		}
	}
}

func TestHarnessShowFlags(t *testing.T) {
	_, proj := harnessSandbox(t)
	want, code := quiet(t, func() int {
		return run([]string{"harness", "show", "skills", "--scope", proj, "--harness", "codex", "--json"})
	})
	if code != 0 {
		t.Fatalf("space form exited %d:\n%s", code, want)
	}
	got, code := quiet(t, func() int {
		return run([]string{"harness", "show", "skills", "--scope=" + proj, "--harness=codex", "--json"})
	})
	if code != 0 || got != want {
		t.Fatalf("= form exited %d:\n%s\nwant:\n%s", code, got, want)
	}
	for _, h := range []string{"-h", "--help"} {
		if out, code := quiet(t, func() int { return run([]string{"harness", "show", "skills", h}) }); code != 0 || !strings.Contains(out, "usage:") {
			t.Errorf("%s exited %d:\n%s", h, code, out)
		}
	}
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"--scope", "--json"}, "--scope needs a value"},
		{[]string{"--harness", "--json"}, "--harness needs a value"},
		{[]string{"--scope"}, "--scope needs a value"},
		{[]string{"--scope="}, "--scope needs a value"},
		{[]string{"-json"}, "unknown flag -json"},
		{[]string{"--json=false"}, "unknown flag --json=false"},
		{[]string{"--"}, "unknown flag --"},
		{[]string{"extra", "--json"}, `unexpected argument "extra"`},
	} {
		var stdout string
		stderr, code := capture(t, &os.Stderr, func() int {
			out, code := quiet(t, func() int { return run(append([]string{"harness", "show", "skills"}, c.args...)) })
			stdout = out
			return code
		})
		if code != 1 || stdout != "" || !strings.Contains(stderr, c.msg) || !strings.Contains(stderr, "usage:") {
			t.Errorf("harness show skills %v exited %d, stdout %q, stderr:\n%s\nwant %q", c.args, code, stdout, stderr, c.msg)
		}
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

func TestFileLogEmptyRepo(t *testing.T) {
	h, _ := harnessSandbox(t)
	dir := filepath.Join(h, "fresh")
	os.MkdirAll(dir, 0o755)
	gitT(t, dir, "init", "-q")
	f := filepath.Join(dir, "CLAUDE.md")
	os.WriteFile(f, []byte("new\n"), 0o644)
	gitT(t, dir, "add", "CLAUDE.md")
	if out, code := quiet(t, func() int { return run([]string{"file", "log", f, "--json"}) }); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("file log in a repo with no commits = %d %q", code, out)
	}
}

func TestFileShowRevisions(t *testing.T) {
	_, proj := harnessSandbox(t)
	f := filepath.Join(proj, "CLAUDE.md")
	gitT(t, proj, "update-ref", "refs/remotes/origin/main", "HEAD")
	if out, code := quiet(t, func() int { return run([]string{"file", "show", f + "@origin/main"}) }); code != 0 || out != "project guidance\n" {
		t.Fatalf("file show @origin/main = %d %q", code, out)
	}
	if msg, code := capture(t, &os.Stderr, func() int { return run([]string{"file", "show", f + "@nope"}) }); code != 1 || !strings.Contains(msg, "nope: unknown revision") {
		t.Fatalf("file show at a bad revision = %d %q", code, msg)
	}
	added := filepath.Join(proj, "added.md")
	os.WriteFile(added, []byte("later\n"), 0o644)
	if msg, code := capture(t, &os.Stderr, func() int { return run([]string{"file", "show", added + "@HEAD"}) }); code != 1 || !strings.Contains(msg, "added.md does not exist at HEAD") {
		t.Fatalf("file show of a file missing at the revision = %d %q", code, msg)
	}
}

func TestSplitRev(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "only@whole"), nil, 0o644)
	os.MkdirAll(filepath.Join(dir, "box@v1"), 0o755)
	os.WriteFile(filepath.Join(dir, "box@v1", "f.md"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "both@HEAD"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "both"), nil, 0o644)
	for _, c := range []struct{ arg, path, rev string }{
		{"a.md@HEAD", "a.md", "HEAD"},
		{"a.md@origin/main", "a.md", "origin/main"},
		{"a.md", "a.md", ""},
		{"a.md@", "a.md@", ""},
		{"@HEAD", "@HEAD", ""},
		{dir + "/only@whole", dir + "/only@whole", ""},
		{dir + "/box@v1/f.md", dir + "/box@v1/f.md", ""},
		{dir + "/box@v1/gone.md", dir + "/box@v1/gone.md", ""},
		{dir + "/both@HEAD", dir + "/both", "HEAD"},
	} {
		if p, r := splitRev(c.arg); p != c.path || r != c.rev {
			t.Errorf("splitRev(%q) = %q, %q; want %q, %q", c.arg, p, r, c.path, c.rev)
		}
	}
}

// file log and file show follow a link into the vault to the vault's history.
func TestFileLogThroughVaultLink(t *testing.T) {
	h := vaultSandbox(t)
	skill := filepath.Join(h, ".claude", "skills", "recomp")
	os.MkdirAll(skill, 0o755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("original\n"), 0o644)
	if out, code := vaultRun(t, "add", "~/.claude/skills/recomp"); code != 0 {
		t.Fatalf("vault add exited %d: %s", code, out)
	}
	f := filepath.Join(skill, "SKILL.md")
	out, code := quiet(t, func() int { return run([]string{"file", "log", f, "--json"}) })
	var commits []fileCommit
	if code != 0 || json.Unmarshal([]byte(out), &commits) != nil || len(commits) != 1 || !strings.Contains(commits[0].Subject, "add skills/recomp") {
		t.Fatalf("file log through the vault link = %d %s", code, out)
	}
	os.WriteFile(f, []byte("edited\n"), 0o644)
	if out, code := quiet(t, func() int { return run([]string{"file", "show", f + "@" + commits[0].Short}) }); code != 0 || out != "original\n" {
		t.Fatalf("file show through the vault link = %d %q", code, out)
	}
}

func TestHarnessShowFollowsLinkedFolders(t *testing.T) {
	h, proj := harnessSandbox(t)
	mk := func(p, body string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, at string) {
		if err := os.Symlink(target, at); err != nil {
			t.Fatal(err)
		}
	}
	// A share link in place of the project's rules folder.
	shared := filepath.Join(h, "shared-rules")
	mk(filepath.Join(shared, "go.md"), "rule\n")
	mk(filepath.Join(shared, "web", "css.md"), "rule\n")
	os.RemoveAll(filepath.Join(proj, ".claude", "rules"))
	link(shared, filepath.Join(proj, ".claude", "rules"))
	n := showJSON(t, "guidance", "--scope", proj).names("rel")
	want := []string{".claude/rules/go.md", ".claude/rules/web/css.md", "CLAUDE.local.md", "CLAUDE.md", "apps/web/CLAUDE.md"}
	if !eq(n[0], want) {
		t.Fatalf("guidance through a linked rules folder = %q", n[0])
	}
	// The same link as the Claude home's rules folder.
	link(shared, filepath.Join(h, ".claude-work", "rules"))
	n = showJSON(t, "guidance", "--scope", proj).names("rel")
	if want := []string{"CLAUDE.md", "rules/go.md", "rules/web/css.md"}; !eq(n[1], want) {
		t.Fatalf("home guidance through a linked rules folder = %q", n[1])
	}

	// A linked folder inside a skill, a link back up to the skill itself, a
	// broken link (skipped: nothing behind it to read) and node_modules inside
	// the linked folder (skipped).
	skill := filepath.Join(h, ".claude-work", "skills", "diagnose")
	refs := filepath.Join(h, "refs")
	mk(filepath.Join(refs, "b.md"), "ref\n")
	link(refs, filepath.Join(skill, "more"))
	link(skill, filepath.Join(skill, "loop"))
	link(filepath.Join(h, "gone"), filepath.Join(skill, "dead.md"))
	mk(filepath.Join(refs, "node_modules", "x", "y.md"), "never listed\n")
	files, err := listFiles(skill)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"SKILL.md", "more/b.md", "refs/a.md"}; !eq(files, want) {
		t.Fatalf("skill files = %q, want %q", files, want)
	}
}

// A hooks file that fails to parse lists as one problem entry; the other
// files' hooks still list.
func TestHarnessShowHooksBadFile(t *testing.T) {
	h, proj := harnessSandbox(t)
	bad := filepath.Join(proj, ".claude", "settings.json")
	for p, body := range map[string]string{
		bad: `{"hooks":`,
		filepath.Join(proj, ".claude", "settings.local.json"): `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"moshi notify"}]}]}}`,
		filepath.Join(h, ".codex", "config.toml"):             "[hooks\n",
	} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := showJSON(t, "hooks", "--scope", proj)
	ev, f, c, p := s.names("event"), s.names("file"), s.names("command"), s.names("problem")
	if !eq(ev[0], []string{"problem", "Stop"}) || f[0][0] != bad || c[0][1] != "moshi notify" || !strings.Contains(p[0][0], "settings.json") {
		t.Fatalf("project hooks = %q %q %q %q", ev, f, c, p)
	}
	x := showJSON(t, "hooks", "--harness", "codex")
	if ev, f, p := x.names("event"), x.names("file"), x.names("problem"); !eq(ev[0], []string{"PreToolUse", "Stop", "problem"}) || f[0][2] != filepath.Join(h, ".codex", "config.toml") || !strings.Contains(p[0][2], "config.toml") {
		t.Fatalf("codex hooks = %q %q %q", ev, f, p)
	}
}

// An MCP config that fails to parse lists as one problem entry; the other
// files' servers still list.
func TestHarnessShowMCPBadFile(t *testing.T) {
	h, proj := harnessSandbox(t)
	bad := filepath.Join(proj, ".mcp.json")
	for p, body := range map[string]string{bad: `{"mcpServers":`, filepath.Join(h, ".codex", "config.toml"): "[mcp_servers\n"} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := showJSON(t, "mcp", "--scope", proj)
	n, ty, f, p := s.names("name"), s.names("type"), s.names("file"), s.names("problem")
	if !eq(n[0], []string{".mcp.json", "local"}) || !eq(ty[0], []string{"problem", "http"}) || f[0][0] != bad || !strings.Contains(p[0][0], ".mcp.json") {
		t.Fatalf("project mcp = %q %q %q %q", n, ty, f, p)
	}
	x := showJSON(t, "mcp", "--harness", "codex")
	if ty, p := x.names("type"), x.names("problem"); !eq(ty[0], []string{"problem"}) || !strings.Contains(p[0][0], "config.toml") {
		t.Fatalf("codex mcp = %q %q", ty, p)
	}
}

// A bad Claude settings.json lists as a problem in Claude's global hooks and
// in pi's, whose kennel extension runs Claude's hooks.
func TestHarnessShowHooksBadClaudeSettingsPi(t *testing.T) {
	h, _ := harnessSandbox(t)
	bad := filepath.Join(h, ".claude", "settings.json")
	ext := filepath.Join(h, ".pi", "agent", "extensions", "kennel-hooks.ts")
	os.MkdirAll(filepath.Dir(ext), 0o755)
	for p, body := range map[string]string{bad: `{"hooks":`, ext: "// kennel"} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, harness := range []string{"claude", "pi"} {
		s := showJSON(t, "hooks", "--harness", harness)
		if ev, f, p := s.names("event"), s.names("file"), s.names("problem"); !eq(ev[0], []string{"problem"}) || f[0][0] != bad || !strings.HasPrefix(p[0][0], "~/.claude/settings.json: ") {
			t.Fatalf("%s hooks = %q %q %q", harness, ev, f, p)
		}
	}
}
