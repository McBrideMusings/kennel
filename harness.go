package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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

const harnessUsage = `usage:
  kennel harness show <skills|guidance|permissions|hooks|mcp> [--scope global|<dir>] [--harness claude|codex|pi] [--json]
  kennel harness recent [--json]`

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
	Scope   string   `json:"scope"` // "project" or "global"
	Path    string   `json:"path"`
	Dirs    []string `json:"dirs,omitempty"` // Skills: the folders the harness loads skills from, in its order
	Entries any      `json:"entries"`
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
	Decision string `json:"decision"` // allow, ask, deny, setting for a Codex config value, or problem for a .rules file that failed
	File     string `json:"file"`
	Problem  string `json:"problem,omitempty"`
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
	Keys    []string `json:"keys"` // the keys that lead to the server's entry in File
}

func cmdHarness(cfg *Config, args []string) int {
	if len(args) > 0 && args[0] == "recent" {
		return cmdHarnessRecent(cfg, args[1:])
	}
	if len(args) < 2 || args[0] != "show" || !slices.Contains(harnessSections, args[1]) {
		return failf(harnessUsage)
	}
	section, scopeArg, harness, asJSON := args[1], "global", "claude", false
	// --scope and --harness take their value as the next word or after =.
	// A next word starting with -- is another flag, never a value; a folder
	// with such a name goes after = or as ./--name.
	for rest := args[2:]; len(rest) > 0; rest = rest[1:] {
		name, val, hasEq := strings.Cut(rest[0], "=")
		switch {
		case rest[0] == "-h" || rest[0] == "--help":
			fmt.Println(harnessUsage)
			return 0
		case rest[0] == "--json":
			asJSON = true
		case name == "--scope" || name == "--harness":
			if !hasEq {
				if len(rest) < 2 || strings.HasPrefix(rest[1], "--") {
					return failf("%s needs a value\n%s", name, harnessUsage)
				}
				val, rest = rest[1], rest[1:]
			}
			if val == "" {
				return failf("%s needs a value\n%s", name, harnessUsage)
			}
			if name == "--scope" {
				scopeArg = val
			} else {
				harness = val
			}
		case strings.HasPrefix(rest[0], "-"):
			return failf("unknown flag %s\n%s", rest[0], harnessUsage)
		default:
			return failf("unexpected argument %q\n%s", rest[0], harnessUsage)
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
		b := harnessBlock{Scope: s, Path: path, Entries: entries}
		if section == "skills" {
			b.Dirs = sc.skillDirs(s)
		}
		out.Blocks = append(out.Blocks, b)
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
// node_modules; root and any folder inside it may be a link.
func listFiles(root string) ([]string, error) {
	files := []string{}
	err := walkFiles(root, func(rel string) { files = append(files, rel) })
	return files, err
}

// walkFiles calls fn with the path, relative to root, of every regular file
// under root, following links to folders (root included) and skipping .git and
// node_modules. A link back to a folder it is already inside is not followed,
// and a broken link is skipped.
func walkFiles(root string, fn func(rel string)) error {
	inside := map[string]bool{}
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}
		if inside[real] {
			return nil
		}
		inside[real] = true
		defer delete(inside, real)
		ents, err := os.ReadDir(real)
		if err != nil {
			return err
		}
		for _, e := range ents {
			p, r := filepath.Join(real, e.Name()), filepath.Join(rel, e.Name())
			fi, err := os.Stat(p)
			switch {
			case err != nil:
			case fi.IsDir():
				if e.Name() == ".git" || e.Name() == "node_modules" {
					continue
				}
				if err := walk(p, r); err != nil {
					return err
				}
			case fi.Mode().IsRegular():
				fn(r)
			}
		}
		return nil
	}
	return walk(root, "")
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
		walkFiles(filepath.Join(base, rulesDir), func(rel string) {
			if strings.HasSuffix(rel, ".md") {
				found[filepath.Join(rulesDir, rel)] = true
			}
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
		// A file that fails to read stands as one problem entry, so the rest
		// of the section still lists.
		problem := func(file string, err error) {
			out = append(out, permissionEntry{Rule: filepath.Base(file), Decision: "problem", File: file, Problem: err.Error()})
		}
		if _, err := readTOML(conf, &c); err != nil {
			problem(conf, err)
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
				problem(global, err)
			}
			setting("trust_level", g.Projects[sc.Root].TrustLevel, global)
		}
		rules, _ := filepath.Glob(filepath.Join(dir, "rules", "*.rules"))
		for _, f := range rules {
			r, err := codexRules(f)
			if err != nil {
				problem(f, err)
				continue
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
		if pattern.Len() == 0 {
			return nil, fmt.Errorf("empty pattern")
		}
		var words []string
		for i := 0; i < pattern.Len(); i++ {
			switch v := pattern.Index(i).(type) {
			case starlark.String:
				words = append(words, string(v))
			case *starlark.List: // alternatives for one word
				if v.Len() == 0 {
					return nil, fmt.Errorf("pattern word %d is an empty list", i+1)
				}
				var alts []string
				for j := 0; j < v.Len(); j++ {
					s, ok := starlark.AsString(v.Index(j))
					if !ok {
						return nil, fmt.Errorf("pattern word %d has an alternative of type %s, want string", i+1, v.Index(j).Type())
					}
					alts = append(alts, s)
				}
				words = append(words, "["+strings.Join(alts, "|")+"]")
			default:
				return nil, fmt.Errorf("pattern word %d has type %s, want string or list", i+1, v.Type())
			}
		}
		d, ok := codexDecisions[string(decision)]
		if !ok {
			return nil, fmt.Errorf("unknown decision %q", string(decision))
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
	// at is the key path to the servers' table in file.
	add := func(file string, servers map[string]mcpServer, at ...string) {
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
			out = append(out, mcpEntry{Name: n, Type: s.Type, Command: redactArg(s.Command), Args: redactArgs(s.Args), URL: redactArg(s.URL), File: file,
				Keys: append(append([]string{}, at...), n)})
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
			add(cj, c.MCPServers, "mcpServers")
			break
		}
		mj := sc.in(scope, ".mcp.json")
		var m struct {
			MCPServers map[string]mcpServer `json:"mcpServers"`
		}
		if _, err := readJSON(mj, &m); err != nil {
			return nil, err
		}
		add(mj, m.MCPServers, "mcpServers")
		add(cj, c.Projects[sc.Root].MCPServers, "projects", sc.Root, "mcpServers")
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
		add(conf, c.MCPServers, "mcp_servers")
	}
	return out, nil
}

// redacted stands in for a value that may be a secret.
const redacted = "REDACTED"

// secretWords are the words that make a flag, variable, header or JSON key
// name a secret. A name matches when one of its words (split at - _ . and
// lower-to-upper case changes) is in the set: --api-key, GITHUB_TOKEN and
// apiKey match; --author and --keyfile do not.
var secretWords = map[string]bool{
	"token": true, "secret": true, "password": true, "passwd": true, "pass": true, "pwd": true,
	"key": true, "apikey": true, "auth": true, "authorization": true, "bearer": true,
	"credential": true, "credentials": true, "cookie": true, "session": true, "pat": true,
	"jwt": true, "sig": true, "signature": true,
}

// nameWords splits a name into lowercase words at - _ . and case changes.
func nameWords(name string) []string {
	var words []string
	var w []rune
	flush := func() {
		if len(w) > 0 {
			words = append(words, strings.ToLower(string(w)))
			w = w[:0]
		}
	}
	prevLower := false
	for _, r := range name {
		switch {
		case r == '-' || r == '_' || r == '.':
			flush()
			prevLower = false
			continue
		case r >= 'A' && r <= 'Z' && prevLower:
			flush()
		}
		w = append(w, r)
		prevLower = r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
	}
	flush()
	return words
}

// secretName reports whether a name names a secret. A switch that turns
// something off (--no-auth, --disable-auth) takes no value, so it does not.
func secretName(name string) bool {
	words := nameWords(name)
	if len(words) > 1 && (words[0] == "no" || words[0] == "disable" || words[0] == "skip" || words[0] == "without") {
		return false
	}
	for _, w := range words {
		if secretWords[w] {
			return true
		}
	}
	return false
}

// envRef reports whether a value only names an environment variable
// ($VAR or ${VAR}), which shows where the secret comes from, not the secret.
func envRef(v string) bool {
	if !strings.HasPrefix(v, "$") {
		return false
	}
	v = v[1:]
	if strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}") {
		v = v[1 : len(v)-1]
	}
	for i, r := range v {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return v != ""
}

// redactValue replaces the value of a secret-named thing. An empty value or
// an environment reference stays; a URL keeps its host and path.
func redactValue(v string) string {
	if v == "" || envRef(v) {
		return v
	}
	if u := redactURL(v); u != "" {
		return u
	}
	return redacted
}

// redactPairs replaces every value in an &-separated k=v list; a bare key
// stays bare.
func redactPairs(s string) string {
	parts := strings.Split(s, "&")
	for i, p := range parts {
		if k, v, ok := strings.Cut(p, "="); ok {
			parts[i] = k + "=" + redactValue(v)
		}
	}
	return strings.Join(parts, "&")
}

// redactURL returns s with its user info, every query value and every
// k=v value in its fragment replaced, or "" when s is not a URL with a
// scheme and a host. A secret carried in the path is not found.
func redactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" || strings.ContainsAny(s, " \t\n") {
		return ""
	}
	if u.User != nil {
		u.User = url.User(redacted)
	}
	if u.RawQuery != "" {
		u.RawQuery = redactPairs(u.RawQuery)
	}
	if strings.Contains(u.Fragment, "=") {
		u.Fragment = redactPairs(u.Fragment)
	}
	return u.String()
}

var (
	pairRe   = regexp.MustCompile(`(?s)^(-{0,2}[A-Za-z_][A-Za-z0-9_.-]*)=(.*)$`)
	headerRe = regexp.MustCompile(`(?s)^([A-Za-z][A-Za-z0-9_-]*)([ \t]*:[ \t]*)(.*)$`)
)

// redactArg redacts one string: a JSON object or array (values under
// secret-named keys, and every string inside), a URL, a "Name: value"
// header, a name=value pair (the value redacted when the name is a secret,
// else read again), or words split at whitespace, read as an argument list.
func redactArg(a string) string {
	if t := strings.TrimSpace(a); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		var v any
		if json.Unmarshal([]byte(t), &v) == nil {
			var b strings.Builder
			enc := json.NewEncoder(&b)
			enc.SetEscapeHTML(false)
			if enc.Encode(redactJSON(v)) == nil {
				return strings.TrimSuffix(b.String(), "\n")
			}
		}
	}
	if u := redactURL(a); u != "" {
		return u
	}
	if m := headerRe.FindStringSubmatch(a); m != nil {
		if secretName(m[1]) {
			return m[1] + m[2] + redactValue(m[3])
		}
		return m[1] + m[2] + redactArg(m[3])
	}
	if m := pairRe.FindStringSubmatch(a); m != nil {
		if !secretName(m[1]) {
			return m[1] + "=" + redactArg(m[2])
		}
		// TOKEN=abc npx server: the value ends at the first space.
		if i := strings.IndexAny(m[2], " \t\n"); i >= 0 {
			return m[1] + "=" + redactValue(m[2][:i]) + m[2][i:i+1] + redactArg(m[2][i+1:])
		}
		return m[1] + "=" + redactValue(m[2])
	}
	if f := strings.Fields(a); len(f) > 1 {
		return strings.Join(redactArgs(f), " ")
	}
	return a
}

// redactJSON redacts values under secret-named keys and every string.
func redactJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && secretName(k) {
				x[k] = redactValue(s)
			} else if secretName(k) && e != nil {
				x[k] = redacted
			} else {
				x[k] = redactJSON(e)
			}
		}
	case []any:
		for i, e := range x {
			x[i] = redactJSON(e)
		}
	case string:
		return redactArg(x)
	}
	return v
}

// redactArgs redacts an argument list: each argument through redactArg,
// and the argument after a secret-named flag (--token x) through
// redactValue. A short flag (-t x) is not recognised.
func redactArgs(args []string) []string {
	if args == nil {
		return nil
	}
	out := make([]string, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") && !strings.Contains(a, "=") && secretName(a) &&
			i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			out[i] = a
			i++
			out[i] = redactValue(args[i])
			continue
		}
		out[i] = redactArg(a)
	}
	return out
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
				line := e.Decision + "\t" + e.Rule
				if e.Problem != "" {
					line += "  (" + e.Problem + ")"
				}
				lines = append(lines, line)
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
