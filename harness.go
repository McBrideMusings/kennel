package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"go.starlark.net/starlark"
)

// kennel harness show reads what one harness (Claude, Codex or pi) gets in
// one scope. Global is the harness's home; a directory is a project, which
// shows its own settings in a Project block and its profile's home in a
// Global block. Each section reads the files that harness itself reads.

const harnessUsage = `usage: kennel harness show <skills|guidance|permissions|hooks|mcp> [--scope global|<dir>] [--harness claude|codex|pi] [--json]`

var harnessSections = []string{"skills", "guidance", "permissions", "hooks", "mcp"}
var harnessNames = []string{"claude", "codex", "pi"}

// defaultHomes is where each harness keeps its files when its variable is unset.
var defaultHomes = map[string]string{"claude": ".claude", "codex": ".codex", "pi": ".pi/agent"}

// toolBase is a harness's base home: its [tools.<name>] base, else the
// harness's default home.
func toolBase(cfg *Config, harness string) string {
	if t, ok := cfg.Tools[harness]; ok && t != nil {
		return t.Base
	}
	return filepath.Join(home(), defaultHomes[harness])
}

// harnessScope is the scope a show reads: Global, or the project a directory
// belongs to with the profile its route picks.
type harnessScope struct {
	Kind    string `json:"kind"` // "global" or "project"
	Dir     string `json:"dir,omitempty"`
	Root    string `json:"root,omitempty"` // the git work tree holding Dir, else Dir
	Profile string `json:"profile,omitempty"`
	Home    string `json:"home"` // the harness's home in this scope

	cfg         *Config
	harness     string
	defaultHome bool // the harness runs with its variable unset
}

type harnessShow struct {
	Section string         `json:"section"`
	Harness string         `json:"harness"`
	Scope   harnessScope   `json:"scope"`
	Blocks  []harnessBlock `json:"blocks"`
}

// harnessBlock is the Project or Global half of a page.
type harnessBlock struct {
	Scope   string `json:"scope"` // "project" or "global"
	Path    string `json:"path"`
	Entries any    `json:"entries"`
}

type skillEntry struct {
	Name  string   `json:"name"`
	Path  string   `json:"path"`
	Link  string   `json:"link,omitempty"` // where Path links to
	Vault bool     `json:"vault"`          // the link points into the vault
	Files []string `json:"files"`          // relative to the skill folder
}

type guidanceEntry struct {
	Path string `json:"path"`
	Rel  string `json:"rel"` // relative to the block's path
}

type permissionEntry struct {
	Rule     string `json:"rule"`
	Decision string `json:"decision"` // allow, ask, deny, or setting for a Codex config value
	File     string `json:"file"`
}

type hookEntry struct {
	Event     string `json:"event"`
	Matcher   string `json:"matcher,omitempty"`
	Command   string `json:"command,omitempty"`
	Script    string `json:"script,omitempty"`
	Tag       string `json:"tag,omitempty"`
	File      string `json:"file"`
	Generated bool   `json:"generated"` // kennel wrote it from Claude's hook list
	Problem   string `json:"problem,omitempty"`
}

type mcpEntry struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"`
	File    string   `json:"file"`
}

func cmdHarness(cfg *Config, args []string) int {
	if len(args) < 2 || args[0] != "show" || !slices.Contains(harnessSections, args[1]) {
		return failf(harnessUsage)
	}
	section, scopeArg, harness, asJSON := args[1], "global", "claude", false
	for rest := args[2:]; len(rest) > 0; rest = rest[1:] {
		switch rest[0] {
		case "--json":
			asJSON = true
		case "--scope", "--harness":
			if len(rest) < 2 {
				return failf(harnessUsage)
			}
			if rest[0] == "--scope" {
				scopeArg = rest[1]
			} else {
				harness = rest[1]
			}
			rest = rest[1:]
		default:
			return failf(harnessUsage)
		}
	}
	if !slices.Contains(harnessNames, harness) {
		return failf("unknown harness %q (claude, codex, pi)", harness)
	}
	sc, err := resolveScope(cfg, scopeArg, harness)
	if err != nil {
		return failf("%v", err)
	}
	show, err := sc.show(section)
	if err != nil {
		return failf("%v", err)
	}
	if asJSON {
		return printJSON(show)
	}
	printShow(show)
	return 0
}

// resolveScope turns --scope into a scope: global is the harness's home with
// no profile; a directory picks its profile through routes as kennel env
// does, and its project root is the git work tree holding it.
func resolveScope(cfg *Config, arg, harness string) (*harnessScope, error) {
	sc := &harnessScope{Kind: "global", cfg: cfg, harness: harness}
	if arg != "global" {
		abs, err := filepath.Abs(expand(arg))
		if err != nil {
			return nil, err
		}
		if !isDir(abs) {
			return nil, fmt.Errorf("%s is not a folder", tilde(abs))
		}
		lines, err := readRoutes()
		if err != nil {
			return nil, err
		}
		sc.Kind, sc.Dir, sc.Root = "project", abs, abs
		md := abs
		if out, err := runGit(abs, nil, "rev-parse", "--path-format=absolute", "--git-common-dir", "--show-toplevel"); err == nil {
			if f := strings.Split(strings.TrimSpace(out), "\n"); len(f) == 2 {
				md, sc.Root = matchFromCommon(f[0], abs), f[1]
			}
		}
		sc.Profile = resolveMatch(cfg, lines, md)
	}
	sc.Home = cfg.Profiles[sc.Profile][harness]
	if sc.Home == "" {
		sc.defaultHome = true
		sc.Home = toolBase(cfg, harness)
	}
	return sc, nil
}

// show reads one section: a Project block for a project scope, then Global.
func (sc *harnessScope) show(section string) (*harnessShow, error) {
	out := &harnessShow{Section: section, Harness: sc.harness, Scope: *sc}
	scopes := []string{"global"}
	if sc.Kind == "project" {
		scopes = []string{"project", "global"}
	}
	for _, s := range scopes {
		path := sc.Home
		if s == "project" {
			path = sc.Root
		}
		var entries any
		var err error
		switch section {
		case "skills":
			entries, err = sc.skills(s)
		case "guidance":
			entries, err = sc.guidance(s)
		case "permissions":
			entries, err = sc.permissions(s)
		case "hooks":
			entries, err = sc.hooks(s)
		case "mcp":
			entries, err = sc.mcp(s)
		}
		if err != nil {
			return nil, err
		}
		out.Blocks = append(out.Blocks, harnessBlock{Scope: s, Path: path, Entries: entries})
	}
	return out, nil
}

// in joins a path to the project root for the project block and to the
// harness home for the global block.
func (sc *harnessScope) in(scope string, parts ...string) string {
	base := sc.Home
	if scope == "project" {
		base = sc.Root
	}
	return filepath.Join(append([]string{base}, parts...)...)
}

// ---- skills ----

// skillDirs lists the folders a harness loads skills from, in its order.
func (sc *harnessScope) skillDirs(scope string) []string {
	agents := filepath.Join(home(), ".agents", "skills")
	switch {
	case scope == "project" && sc.harness == "claude":
		return []string{sc.in(scope, ".claude", "skills")}
	case scope == "project" && sc.harness == "codex":
		return []string{sc.in(scope, ".agents", "skills")}
	case scope == "project":
		return []string{sc.in(scope, ".pi", "skills"), sc.in(scope, ".agents", "skills")}
	case sc.harness == "claude":
		return []string{sc.in(scope, "skills")}
	}
	return []string{sc.in(scope, "skills"), agents}
}

func (sc *harnessScope) skills(scope string) ([]skillEntry, error) {
	vaultRoot, _ := filepath.EvalSymlinks(filepath.Join(dataDir(), "vault"))
	out := []skillEntry{}
	for _, dir := range sc.skillDirs(scope) {
		ents, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if strings.HasPrefix(e.Name(), ".") || !isDir(p) {
				continue
			}
			s := skillEntry{Name: e.Name(), Path: p, Files: []string{}}
			if e.Type()&fs.ModeSymlink != 0 {
				s.Link, _ = os.Readlink(p)
				if real, err := filepath.EvalSymlinks(p); err == nil && vaultRoot != "" && strings.HasPrefix(real, vaultRoot+"/") {
					s.Vault = true
				}
			}
			if s.Files, err = listFiles(p); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// listFiles lists every file under root, relative to it, skipping .git and
// node_modules; root itself may be a link.
func listFiles(root string) ([]string, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	files := []string{}
	err = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != real && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			files = append(files, p[len(real)+1:])
		}
		return nil
	})
	return files, err
}

// ---- guidance ----

// guidanceNames are the instruction files a harness reads, at any depth of a
// project and at the top of its home.
var guidanceNames = map[string][]string{
	"claude": {"CLAUDE.md", "CLAUDE.local.md"},
	"codex":  {"AGENTS.md", "AGENTS.override.md"},
	"pi":     {"AGENTS.md", "CLAUDE.md"},
}

func (sc *harnessScope) guidance(scope string) ([]guidanceEntry, error) {
	base := sc.in(scope)
	names := guidanceNames[sc.harness]
	found := map[string]bool{}
	for _, n := range names {
		if isFile(filepath.Join(base, n)) {
			found[n] = true
		}
	}
	rulesDir := "rules" // Claude's home keeps rules/ at its top
	if scope == "project" {
		rulesDir = filepath.Join(".claude", "rules")
		// git lists the files a project holds without walking node_modules or
		// build output; ignored files such as CLAUDE.local.md are checked above.
		if out, err := runGit(base, nil, "ls-files", "-z", "-co", "--exclude-standard"); err == nil {
			for _, rel := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
				if slices.Contains(names, filepath.Base(rel)) && isFile(filepath.Join(base, rel)) {
					found[rel] = true
				}
			}
		}
	}
	if sc.harness == "claude" {
		filepath.WalkDir(filepath.Join(base, rulesDir), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
				found[p[len(base)+1:]] = true
			}
			return nil
		})
	}
	rels := make([]string, 0, len(found))
	for r := range found {
		rels = append(rels, r)
	}
	sort.Strings(rels)
	out := []guidanceEntry{}
	for _, r := range rels {
		out = append(out, guidanceEntry{Path: filepath.Join(base, r), Rel: r})
	}
	return out, nil
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// ---- permissions ----

func (sc *harnessScope) permissions(scope string) ([]permissionEntry, error) {
	out := []permissionEntry{}
	switch sc.harness {
	case "claude":
		files := []string{sc.in(scope, "settings.json")}
		if scope == "project" {
			files = []string{sc.in(scope, ".claude", "settings.json"), sc.in(scope, ".claude", "settings.local.json")}
		}
		for _, f := range files {
			var s struct {
				Permissions struct{ Allow, Ask, Deny []string } `json:"permissions"`
			}
			if ok, err := readJSON(f, &s); err != nil {
				return nil, err
			} else if !ok {
				continue
			}
			for _, d := range []struct {
				name  string
				rules []string
			}{{"allow", s.Permissions.Allow}, {"ask", s.Permissions.Ask}, {"deny", s.Permissions.Deny}} {
				for _, r := range d.rules {
					out = append(out, permissionEntry{Rule: r, Decision: d.name, File: f})
				}
			}
		}
	case "codex":
		dir := sc.in(scope)
		if scope == "project" {
			dir = sc.in(scope, ".codex")
		}
		conf := filepath.Join(dir, "config.toml")
		var c struct {
			ApprovalPolicy string `toml:"approval_policy"`
			SandboxMode    string `toml:"sandbox_mode"`
		}
		if _, err := readTOML(conf, &c); err != nil {
			return nil, err
		}
		setting := func(key, val, file string) {
			if val != "" {
				out = append(out, permissionEntry{Rule: key + " = " + val, Decision: "setting", File: file})
			}
		}
		setting("approval_policy", c.ApprovalPolicy, conf)
		setting("sandbox_mode", c.SandboxMode, conf)
		if scope == "project" {
			// Codex keeps a project's trust in the home's config.
			global := sc.in("global", "config.toml")
			var g struct {
				Projects map[string]struct {
					TrustLevel string `toml:"trust_level"`
				} `toml:"projects"`
			}
			if _, err := readTOML(global, &g); err != nil {
				return nil, err
			}
			setting("trust_level", g.Projects[sc.Root].TrustLevel, global)
		}
		rules, _ := filepath.Glob(filepath.Join(dir, "rules", "*.rules"))
		for _, f := range rules {
			r, err := codexRules(f)
			if err != nil {
				return nil, err
			}
			out = append(out, r...)
		}
	}
	return out, nil
}

// codexDecisions maps a Codex rule's decision onto Claude's words.
var codexDecisions = map[string]string{"allow": "allow", "prompt": "ask", "forbidden": "deny"}

// codexRules reads one Codex .rules file, a Starlark program of
// prefix_rule(pattern=[...], decision=...) calls, the way Codex does: by
// running it with prefix_rule defined.
func codexRules(path string) ([]permissionEntry, error) {
	var out []permissionEntry
	prefixRule := starlark.NewBuiltin("prefix_rule", func(_ *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var pattern *starlark.List
		var decision starlark.String = "allow"
		var justification, match, notMatch starlark.Value
		if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "pattern", &pattern, "decision?", &decision,
			"justification?", &justification, "match?", &match, "not_match?", &notMatch); err != nil {
			return nil, err
		}
		var words []string
		for i := 0; i < pattern.Len(); i++ {
			switch v := pattern.Index(i).(type) {
			case starlark.String:
				words = append(words, string(v))
			case *starlark.List: // alternatives for one word
				var alts []string
				for j := 0; j < v.Len(); j++ {
					s, _ := starlark.AsString(v.Index(j))
					alts = append(alts, s)
				}
				words = append(words, "["+strings.Join(alts, "|")+"]")
			}
		}
		d, ok := codexDecisions[string(decision)]
		if !ok {
			return nil, fmt.Errorf("unknown decision %q", decision)
		}
		out = append(out, permissionEntry{Rule: strings.Join(words, " "), Decision: d, File: path})
		return starlark.None, nil
	})
	thread := &starlark.Thread{Name: "rules"}
	if _, err := starlark.ExecFile(thread, path, nil, starlark.StringDict{"prefix_rule": prefixRule}); err != nil {
		return nil, fmt.Errorf("%s: %v", tilde(path), err)
	}
	return out, nil
}

// ---- hooks ----

func (sc *harnessScope) hooks(scope string) ([]hookEntry, error) {
	out := []hookEntry{}
	switch sc.harness {
	case "claude":
		files := []string{sc.in(scope, "settings.json")}
		dirs := []string{sc.in(scope, "hooks"), hooksDir(sc.cfg)}
		if scope == "project" {
			files = []string{sc.in(scope, ".claude", "settings.json"), sc.in(scope, ".claude", "settings.local.json")}
			dirs = []string{sc.in(scope, ".claude", "hooks")}
		}
		for _, f := range files {
			hs, err := sc.hookFile(f, dirs)
			if err != nil {
				return nil, err
			}
			out = append(out, hs...)
		}
	case "codex":
		f := sc.in(scope, "hooks.json")
		dirs := []string{hooksDir(sc.cfg), sc.in(scope)}
		if scope == "project" {
			f = sc.in(scope, ".codex", "hooks.json")
			dirs = []string{hooksDir(sc.cfg), sc.in(scope, ".codex")}
		}
		hs, err := sc.hookFile(f, dirs)
		if err != nil {
			return nil, err
		}
		for i := range hs {
			hs[i].Generated = namesHooksDir(hs[i].Command, hooksDir(sc.cfg))
		}
		out = append(out, hs...)
	case "pi":
		dir := sc.in(scope, "extensions")
		if scope == "project" {
			dir = sc.in(scope, ".pi", "extensions")
		}
		ents, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if e.Name() != filepath.Base(piExtensionPath(sc.cfg)) {
				out = append(out, hookEntry{Event: "extension", Script: p, File: p})
				continue
			}
			// kennel's extension runs Claude's shared handlers, read at pi's start.
			handlers, err := readClaudeHooks(sc.cfg)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			for _, h := range handlers {
				if h.shared("pi") {
					out = append(out, hookEntry{Event: h.Event, Matcher: h.Matcher, Command: h.Command, Script: h.Script, Tag: h.Tag, File: p, Generated: true, Problem: h.Problem})
				}
			}
		}
	}
	return out, nil
}

// hookFile lists the command handlers in one Claude-shaped hooks file
// (Claude's settings.json, Codex's hooks.json), in file order, with the
// script each runs from one of dirs and that script's tag.
func (sc *harnessScope) hookFile(path string, dirs []string) ([]hookEntry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s claudeSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %v", tilde(path), err)
	}
	events, err := objectKeys(data, "hooks")
	if err != nil {
		return nil, fmt.Errorf("%s: %v", tilde(path), err)
	}
	var out []hookEntry
	for _, ev := range events {
		for _, g := range s.Hooks[ev] {
			for _, h := range g.Hooks {
				if h.Type != "command" {
					continue
				}
				e := hookEntry{Event: ev, Matcher: g.Matcher, Command: h.Command, File: path}
				cmd := h.Command
				if sc.Kind == "project" {
					for _, v := range []string{`"$CLAUDE_PROJECT_DIR"`, `"${CLAUDE_PROJECT_DIR}"`, "$CLAUDE_PROJECT_DIR", "${CLAUDE_PROJECT_DIR}"} {
						cmd = strings.ReplaceAll(cmd, v, sc.Root)
					}
				}
				for _, d := range dirs {
					if e.Script = scriptIn(cmd, d); e.Script != "" {
						break
					}
				}
				if e.Script != "" {
					tag, err := readTag(e.Script)
					e.Tag = tag
					if err != nil {
						e.Problem = err.Error()
					}
				}
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// ---- mcp ----

type mcpServer struct {
	Type    string   `json:"type" toml:"type"`
	Command string   `json:"command" toml:"command"`
	Args    []string `json:"args" toml:"args"`
	URL     string   `json:"url" toml:"url"`
}

func (sc *harnessScope) mcp(scope string) ([]mcpEntry, error) {
	out := []mcpEntry{}
	add := func(file string, servers map[string]mcpServer) {
		names := make([]string, 0, len(servers))
		for n := range servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			s := servers[n]
			if s.Type == "" {
				s.Type = "stdio"
				if s.Command == "" {
					s.Type = "http"
				}
			}
			out = append(out, mcpEntry{Name: n, Type: s.Type, Command: s.Command, Args: s.Args, URL: s.URL, File: file})
		}
	}
	switch sc.harness {
	case "claude":
		// Claude keeps user servers, and each project's local ones, in
		// .claude.json: in the home when its variable is set, else in ~.
		cj := filepath.Join(home(), ".claude.json")
		if !sc.defaultHome {
			cj = filepath.Join(sc.Home, ".claude.json")
		}
		var c struct {
			MCPServers map[string]mcpServer `json:"mcpServers"`
			Projects   map[string]struct {
				MCPServers map[string]mcpServer `json:"mcpServers"`
			} `json:"projects"`
		}
		if _, err := readJSON(cj, &c); err != nil {
			return nil, err
		}
		if scope == "global" {
			add(cj, c.MCPServers)
			break
		}
		mj := sc.in(scope, ".mcp.json")
		var m struct {
			MCPServers map[string]mcpServer `json:"mcpServers"`
		}
		if _, err := readJSON(mj, &m); err != nil {
			return nil, err
		}
		add(mj, m.MCPServers)
		add(cj, c.Projects[sc.Root].MCPServers)
	case "codex":
		conf := sc.in(scope, "config.toml")
		if scope == "project" {
			conf = sc.in(scope, ".codex", "config.toml")
		}
		var c struct {
			MCPServers map[string]mcpServer `toml:"mcp_servers"`
		}
		if _, err := readTOML(conf, &c); err != nil {
			return nil, err
		}
		add(conf, c.MCPServers)
	}
	return out, nil
}

// readJSON decodes a JSON file into v; a missing file reports false.
func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("%s: %v", tilde(path), err)
	}
	return true, nil
}

// readTOML decodes a TOML file into v; a missing file reports false.
func readTOML(path string, v any) (bool, error) {
	_, err := toml.DecodeFile(path, v)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: %v", tilde(path), err)
	}
	return true, nil
}

// ---- text output ----

func printShow(s *harnessShow) {
	for i, b := range s.Blocks {
		if i > 0 {
			fmt.Println()
		}
		title := "Global"
		if b.Scope == "project" {
			title = "Project"
		}
		fmt.Printf("%s  %s\n", title, tilde(b.Path))
		var lines []string
		switch es := b.Entries.(type) {
		case []skillEntry:
			for _, e := range es {
				line := e.Name
				if e.Vault {
					line += "  (vault)"
				} else if e.Link != "" {
					line += "  → " + tilde(e.Link)
				}
				lines = append(lines, line)
			}
		case []guidanceEntry:
			for _, e := range es {
				lines = append(lines, e.Rel)
			}
		case []permissionEntry:
			for _, e := range es {
				lines = append(lines, e.Decision+"\t"+e.Rule)
			}
		case []hookEntry:
			for _, e := range es {
				what := e.Command
				if e.Script != "" {
					what = tilde(e.Script)
				}
				line := e.Event + "\t" + e.Matcher + "\t" + what
				if e.Generated {
					line += "  (from Claude's hooks)"
				}
				lines = append(lines, line)
			}
		case []mcpEntry:
			for _, e := range es {
				what := e.URL
				if e.Command != "" {
					what = strings.Join(append([]string{e.Command}, e.Args...), " ")
				}
				lines = append(lines, e.Name+"\t"+e.Type+"\t"+what)
			}
		}
		for _, l := range lines {
			fmt.Println("  " + l)
		}
	}
}
