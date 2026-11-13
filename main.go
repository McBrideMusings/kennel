// kennel routes directories to named profiles for coding-agent CLIs
// (Claude Code, Codex, ...). A profile gives each tool its own home directory,
// so a directory under ~/Work starts `claude` and `codex` logged into the work
// accounts. A zsh, bash or fish hook exports each tool's home variable on every cd.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const usage = `kennel — route directories to coding-agent profiles

  kennel route list                        routes and profiles
  kennel route add <path> <profile> [when] [--permanent]
                                           route a directory (expires tomorrow unless told)
  kennel route rm <path>                   delete a directory's route
  kennel route prune                       delete expired routes now
  kennel which [dir]                       the profile and homes a directory gets
  kennel homes <tool>                      each profile's home for one tool, tab-separated
  kennel link [profile]                    create profile homes and their shared symlinks
  kennel share plan|apply|undo|prune [share [item]]
                                           show, create or remove the cross-tool links in [shares]
  kennel share add|rm <share> <item>... | --link <path>...
                                           add or remove a share's items or links and their links
  kennel hooks plan|apply                  show or write Claude's shared hooks for Codex and pi ([hooks])
  kennel hooks list codex|pi               the shared hooks as JSON, wrapped for that tool
  kennel hook codex|pi -- <command>        run one Claude hook on that tool's payload
  kennel doctor                            check config, symlinks and this shell's variables
  kennel vault add <path>                  move a skill folder or snippet into the vault, link it back
  kennel vault log <item>                  a vault skill or snippet's commits
  kennel vault show <item>[@<rev>]         a vault item's files at a commit
  kennel vault commit --session <id> --harness <h> <file>
                                           commit one vault file an agent edited
  kennel harness show <skills|guidance|permissions|hooks|mcp> [--scope global|<dir>] [--harness claude|codex|pi] [--json]
                                           what one harness gets globally or in a project
  kennel file log <path> [--json]          the commits that touched a file
  kennel file show <path>[@<rev>]          a file's text now or at a commit
  kennel env [fish]                        shell commands that set the variables for $PWD
  kennel init zsh|bash|fish                the hook ~/.zshrc, ~/.bashrc or config.fish evaluates
  kennel help config                       the config file format

  when: ` + whenHelp + `
`

const configHelp = `Config: ~/.config/kennel/config.toml (KENNEL_CONFIG_DIR moves the directory)

  [tools.claude]
  # env = "CLAUDE_CONFIG_DIR"   built in for claude, codex and pi; set it to override
  base   = "~/.claude"          # the home every profile's shared items link to
  shared = ["skills", "CLAUDE.md", "settings.json"]

  [tools.codex]
  base   = "~/.codex"
  shared = ["skills", "AGENTS.md", "config.toml"]

  [profiles.personal]           # no homes: every tool uses its own default

  [profiles.work]
  claude = "~/.claude-work"
  codex  = "~/.codex-work"

  [shares.instructions]         # optional: one real file, linked from each path
  source = "~/.claude/AGENTS.md"
  links  = ["~/.codex/AGENTS.md", "~/.pi/agent/AGENTS.md"]

  [shares.skills]               # optional: one folder link per named item
  source = "~/.claude/skills"
  into   = "~/.agents/skills"
  items  = ["diagnose", "research"]   # or "*": every entry in source
  # exclude = ["grill-me"]      # with items = "*": entries not to link

  [hooks]                       # optional: run Claude's tagged hooks elsewhere
  codex = true                  # rewrite ~/.codex/hooks.json entries into ~/.claude/hooks
  pi    = true                  # write ~/.pi/agent/extensions/kennel-hooks.ts

A hook script reaches Codex and pi only with a "# kennel-hook: portable" or
"# kennel-hook: adapter" line in its first 20 lines.

A tool missing from a profile gets its variable unset, so it uses its default
home. Claude, Codex and pi have built-in variables; any other tool needs env = "...".
Routes live beside the config in ~/.config/kennel/routes.
`

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		args = []string{"route", "list"}
	}
	switch args[0] {
	case "help", "-h", "--help":
		if len(args) > 1 && args[1] == "config" {
			fmt.Print(configHelp)
		} else {
			fmt.Print(usage)
		}
		return 0
	case "hook":
		return cmdHook(args[1:])
	case "vault":
		return cmdVault(args[1:])
	case "file":
		return cmdFile(args[1:])
	case "init":
		if len(args) != 2 || (args[1] != "zsh" && args[1] != "bash" && args[1] != "fish") {
			return failf("usage: kennel init zsh|bash|fish")
		}
		return cmdInit(args[1])
	}

	cfg, err := loadConfig()
	if err != nil {
		return failf("%v", err)
	}
	switch args[0] {
	case "env":
		if len(args) > 2 || (len(args) == 2 && args[1] != "fish") {
			return failf("usage: kennel env [fish]")
		}
		return cmdEnv(cfg, len(args) == 2)
	case "which":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		return cmdWhich(cfg, dir)
	case "route":
		return cmdRoute(cfg, args[1:])
	case "homes":
		if len(args) != 2 {
			return failf("usage: kennel homes <tool>")
		}
		return cmdHomes(cfg, args[1])
	case "link":
		only := ""
		if len(args) > 1 {
			only = args[1]
		}
		return cmdLink(cfg, only)
	case "share":
		if len(args) < 2 {
			return failf(shareUsage)
		}
		if args[1] == "add" || args[1] == "rm" {
			return cmdShareEdit(cfg, args[1], args[2:])
		}
		return cmdShare(cfg, args[1], args[2:])
	case "hooks":
		return cmdHooks(cfg, args[1:])
	case "harness":
		return cmdHarness(cfg, args[1:])
	case "doctor":
		return cmdDoctor(cfg)
	}
	fmt.Fprint(os.Stderr, usage)
	return 2
}

// cmdInit prints the hook for one shell. It calls this binary by absolute
// path so the hook works before PATH is set up and in shells that change PATH.
// Each hook also wraps kennel so a route change applies at once. The wrapper
// repeats the env call rather than calling the hook function: the Claude Code
// Bash tool's shell snapshot keeps kennel() but drops underscore-prefixed
// functions.
func cmdInit(shell string) int {
	exe, err := selfPath()
	if err != nil {
		return failf("%v", err)
	}
	switch shell {
	case "zsh":
		fmt.Printf(`_kennel_apply() { eval "$(%[1]s env)" }
kennel() { %[1]s "$@"; local rc=$?; eval "$(%[1]s env)"; return $rc }
autoload -Uz add-zsh-hook
add-zsh-hook chpwd _kennel_apply
_kennel_apply
`, shellQuote(exe))
	case "bash":
		// bash has no chpwd hook. Like mise, wrap cd, pushd and popd, so
		// `cd dir && claude` and shells that never print a prompt get the
		// route; an existing function of that name is left alone. Like direnv,
		// also run from PROMPT_COMMAND (a string, or an array in bash 5.1+) to
		// catch any other directory change. Both run kennel env only when $PWD
		// changed. The wrappers inline the check, as kennel() does, because the
		// Claude Code snapshot drops _kennel_hook.
		q := shellQuote(exe)
		apply := fmt.Sprintf(`if [[ "$PWD" != "${_KENNEL_PWD-}" ]]; then _KENNEL_PWD=$PWD; eval "$(%s env)"; fi`, q)
		fmt.Printf("_kennel_hook() { local rc=$?; %s; return $rc; }\n", apply)
		for _, c := range []string{"cd", "pushd", "popd"} {
			fmt.Printf("declare -F %[1]s >/dev/null || %[1]s() { builtin %[1]s \"$@\" || return; %[2]s; }\n", c, apply)
		}
		fmt.Printf(`kennel() { %[1]s "$@"; local rc=$?; eval "$(%[1]s env)"; return $rc; }
if [[ "$(declare -p PROMPT_COMMAND 2>/dev/null)" == "declare -a"* ]]; then
  if [[ " ${PROMPT_COMMAND[*]} " != *" _kennel_hook "* ]]; then
    PROMPT_COMMAND=(_kennel_hook "${PROMPT_COMMAND[@]}")
  fi
elif [[ ";${PROMPT_COMMAND-};" != *";_kennel_hook;"* ]]; then
  PROMPT_COMMAND="_kennel_hook${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
fi
_kennel_hook
`, q)
	case "fish":
		// fish sets $PWD on every directory change, so --on-variable PWD is
		// its chpwd. fish cannot read export/unset lines; kennel env fish
		// prints set -gx and set -e.
		fmt.Printf(`function __kennel_apply --on-variable PWD
    %[1]s env fish | source
end
function kennel
    %[1]s $argv
    set -l rc $status
    %[1]s env fish | source
    return $rc
end
__kennel_apply
`, fishQuote(exe))
	}
	return 0
}

// cmdEnv prints the commands that set each tool's variable for $PWD: export
// and unset lines for zsh and bash, set -gx and set -e lines for fish.
func cmdEnv(cfg *Config, fish bool) int {
	if _, err := dropExpired(); err != nil {
		return failf("%v", err)
	}
	lines, err := readRoutes()
	if err != nil {
		return failf("%v", err)
	}
	dir, err := os.Getwd()
	if err != nil {
		return failf("%v", err)
	}
	profile := resolve(cfg, lines, dir)
	for _, name := range cfg.toolNames() {
		env := cfg.Tools[name].Env
		h := cfg.Profiles[profile][name]
		switch {
		case fish && h != "":
			fmt.Printf("set -gx %s %s\n", env, fishQuote(h))
		case fish:
			fmt.Printf("set -e %s\n", env)
		case h != "":
			fmt.Printf("export %s=%s\n", env, shellQuote(h))
		default:
			fmt.Printf("unset %s\n", env)
		}
	}
	return 0
}

func cmdWhich(cfg *Config, dir string) int {
	abs, err := filepath.Abs(expand(dir))
	if err != nil {
		return failf("%v", err)
	}
	lines, err := readRoutes()
	if err != nil {
		return failf("%v", err)
	}
	profile := resolve(cfg, lines, abs)
	shown := profile
	if shown == "" {
		shown = "(no route)"
	}
	fmt.Printf("profile\t%s\n", shown)
	for _, name := range cfg.toolNames() {
		t := cfg.Tools[name]
		if h := cfg.Profiles[profile][name]; h != "" {
			fmt.Printf("%s\t%s=%s\n", name, t.Env, h)
		} else {
			fmt.Printf("%s\t%s unset (default home)\n", name, t.Env)
		}
	}
	return 0
}

// cmdHomes prints `<profile>\t<home>` for every profile, for scripts that
// act on each account's files. A profile without a home of its own for the
// tool prints the base home.
func cmdHomes(cfg *Config, tool string) int {
	t, ok := cfg.Tools[tool]
	if !ok {
		return failf("no [tools.%s] in %s", tool, cfg.Path)
	}
	for _, prof := range cfg.profileNames() {
		h, ok := cfg.Profiles[prof][tool]
		if !ok {
			h = t.Base
		}
		fmt.Printf("%s\t%s\n", prof, h)
	}
	return 0
}

func cmdRoute(cfg *Config, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list", "ls":
		return routeList(cfg)
	case "add":
		return routeAdd(cfg, args)
	case "rm", "remove":
		if len(args) != 1 {
			return failf("usage: kennel route rm <path>")
		}
		return routeRemove(args[0])
	case "prune":
		dropped, err := dropExpired()
		if err != nil {
			return failf("%v", err)
		}
		for _, l := range dropped {
			fmt.Printf("dropped expired route: %s\n", l.format())
		}
		if len(dropped) == 0 {
			fmt.Println("nothing expired")
		}
		return 0
	}
	return failf("unknown route command %q (list, add, rm, prune)", sub)
}

func routeList(cfg *Config) int {
	lines, err := readRoutes()
	if err != nil {
		return failf("%v", err)
	}
	day := today()
	fmt.Printf("Routes (%s):\n", tilde(routesPath()))
	any := false
	for _, l := range lines {
		if l.bad != "" {
			fmt.Printf("  [IGNORED — unreadable] %s\n", l.bad)
		}
		if !l.isRoute() {
			continue
		}
		any = true
		status := ""
		if l.expires != "" {
			if l.expired(day) {
				status = " [EXPIRED " + l.expires + " — dropped on the next cd]"
			} else {
				status = " [until " + l.expires + "]"
			}
		}
		if _, ok := cfg.Profiles[l.profile]; !ok {
			status = " [IGNORED — no such profile]" + status
		}
		fmt.Printf("  %-45s → %s%s\n", l.path, l.profile, status)
	}
	if !any {
		fmt.Println("  (none)")
	}
	fmt.Printf("Profiles (%s):\n", tilde(cfg.Path))
	for _, prof := range cfg.profileNames() {
		var parts []string
		for _, name := range cfg.toolNames() {
			if h, ok := cfg.Profiles[prof][name]; ok {
				parts = append(parts, name+"="+tilde(h))
			}
		}
		if len(parts) == 0 {
			parts = []string{"(every tool on its default home)"}
		}
		fmt.Printf("  %-12s %s\n", prof, strings.Join(parts, "  "))
	}
	return 0
}

func routeAdd(cfg *Config, args []string) int {
	permanent := false
	var pos []string
	for _, a := range args {
		if a == "-p" || a == "--permanent" {
			permanent = true
		} else {
			pos = append(pos, a)
		}
	}
	if len(pos) < 2 {
		return failf("usage: kennel route add <path> <profile> [when] [--permanent]")
	}
	path, err := normalizeRoutePath(pos[0])
	if err != nil {
		return failf("%v", err)
	}
	profile := pos[1]
	if _, ok := cfg.Profiles[profile]; !ok {
		return failf("no [profiles.%s] in %s (profiles: %s)", profile, cfg.Path, strings.Join(cfg.profileNames(), ", "))
	}
	expires := ""
	switch {
	case permanent && len(pos) > 2:
		return failf("give a date or --permanent, not both")
	case permanent:
	case len(pos) == 2:
		expires = time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	default:
		if expires, err = parseWhen(strings.Join(pos[2:], " "), time.Now()); err != nil {
			return failf("%v", err)
		}
	}

	lines, err := readRoutes()
	if err != nil {
		return failf("%v", err)
	}
	if lines == nil {
		for _, h := range strings.Split(strings.TrimRight(routesHeader, "\n"), "\n") {
			lines = append(lines, routeLine{raw: h})
		}
	}
	// One row per path: two rows for one path resolve by file order, so a
	// stale row would outrank the new one.
	entry := routeLine{path: path, profile: profile, expires: expires}
	var kept []routeLine
	var old *routeLine
	for _, l := range lines {
		if l.isRoute() && l.path == path {
			old = &l
			continue
		}
		kept = append(kept, l)
	}
	if err := writeRoutes(append(kept, entry)); err != nil {
		return failf("%v", err)
	}
	if old != nil {
		logf("route replaced: %s -> %s", old.format(), entry.format())
		fmt.Printf("Replaced: %s\n    with: %s\n", old.format(), entry.format())
	} else {
		logf("route added: %s", entry.format())
		fmt.Printf("Added: %s\n", entry.format())
	}
	return 0
}

func routeRemove(p string) int {
	path, err := normalizeRoutePath(p)
	if err != nil {
		return failf("%v", err)
	}
	lines, err := readRoutes()
	if err != nil {
		return failf("%v", err)
	}
	var kept []routeLine
	removed := 0
	for _, l := range lines {
		// Compare the path field only, so `rm work` can't match a profile name.
		if l.isRoute() && l.path == path {
			logf("route removed: %s", l.format())
			fmt.Printf("Removed: %s\n", l.format())
			removed++
			continue
		}
		kept = append(kept, l)
	}
	if removed == 0 {
		return failf("no route for %s", path)
	}
	if err := writeRoutes(kept); err != nil {
		return failf("%v", err)
	}
	return 0
}

func cmdDoctor(cfg *Config) int {
	problems := 0
	note := func(ok bool, format string, a ...any) {
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			problems++
		}
		fmt.Printf("%s  %s\n", mark, fmt.Sprintf(format, a...))
	}

	fmt.Printf("Config %s\n", tilde(cfg.Path))
	for _, name := range cfg.toolNames() {
		t := cfg.Tools[name]
		missing := ""
		if _, err := os.Stat(t.Base); err != nil {
			missing = " (missing)"
		}
		note(missing == "", "%s: variable %s (%s), base %s%s", name, t.Env, t.EnvSource, tilde(t.Base), missing)
	}

	fmt.Println("Symlinks")
	checks := checkPairs(profilePairs(cfg, ""))
	if len(checks) == 0 {
		fmt.Println("      (no profile has a home of its own)")
	}
	for _, c := range checks {
		note(c.state == linkOK, "%s %s", c.label, c.describe())
	}

	fmt.Println("Shares")
	shares := checkPairs(sharePairs(cfg))
	if len(shares) == 0 {
		fmt.Println("      (no [shares] in the config)")
	}
	rec, err := readRecord()
	switch {
	case os.IsNotExist(err):
		fmt.Printf("      (no share link recorded yet in %s)\n", tilde(recordPath()))
	case err != nil:
		fmt.Printf("      (the links record cannot be read, so links that left config.toml go unseen: %v)\n", err)
	}
	recorded, _ := recordedOrphans(cfg, rec)
	for _, c := range append(append(shares, orphanChecks(cfg)...), recorded...) {
		note(c.state == linkOK, "%s %s", c.label, c.describe())
	}

	fmt.Println("Hooks")
	if !cfg.Hooks.Codex && !cfg.Hooks.Pi {
		fmt.Println("      (no [hooks] opt-in)")
	} else if handlers, err := readClaudeHooks(cfg); err != nil {
		note(false, "%v", err)
	} else if exe, err := selfPath(); err != nil {
		note(false, "%v", err)
	} else if outs, err := planHooks(cfg, handlers, exe); err != nil {
		note(false, "%v", err)
	} else {
		for _, o := range outs {
			state := "is current"
			if o.changed() {
				state = "is out of date; run `kennel hooks apply`"
			}
			note(!o.changed(), "%s: %s %s", o.Target, tilde(o.Path), state)
		}
	}

	fmt.Println("Routes")
	lines, err := readRoutes()
	if err != nil {
		note(false, "%v", err)
	}
	for _, l := range lines {
		if l.bad != "" {
			note(false, "%s; the row is ignored", l.bad)
		}
		if l.isRoute() {
			if _, ok := cfg.Profiles[l.profile]; !ok {
				note(false, "%s names profile %q, which config.toml does not define; the row is ignored", l.path, l.profile)
			}
		}
	}

	fmt.Println("This shell")
	dir, _ := os.Getwd()
	profile := resolve(cfg, lines, dir)
	before := problems
	for _, name := range cfg.toolNames() {
		env, want := cfg.Tools[name].Env, cfg.Profiles[profile][name]
		got, set := os.LookupEnv(env)
		shown := "unset"
		if set {
			shown = env + "=" + got
		}
		switch {
		case want == "":
			note(!set, "%s: %s; the route for %s wants it unset", name, shown, tilde(dir))
		default:
			note(got == want, "%s: %s; the route for %s wants %s", name, shown, tilde(dir), tilde(want))
		}
	}
	if problems > before {
		fmt.Printf("      (a wrong variable here usually means %s)\n", initHint(os.Getenv("SHELL")))
	}

	if problems > 0 {
		fmt.Fprintf(os.Stderr, "kennel: %d problem(s)\n", problems)
		return 1
	}
	return 0
}

// initHint names the rc file and hook line for the login shell in $SHELL.
func initHint(shell string) string {
	switch filepath.Base(shell) {
	case "bash":
		return "~/.bashrc does not run `eval \"$(kennel init bash)\"`"
	case "fish":
		return "~/.config/fish/config.fish does not run `kennel init fish | source`"
	}
	return "~/.zshrc does not run `eval \"$(kennel init zsh)\"`"
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// fishQuote single-quotes s for fish, where \ and ' are the only escapes
// inside single quotes.
func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func failf(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "kennel: "+format+"\n", a...)
	return 1
}

func fatalf(format string, a ...any) { os.Exit(failf(format, a...)) }

// logf appends a timestamped line to ~/.local/state/kennel/kennel.log
// for every change kennel makes on disk. Logging never fails a command.
func logf(format string, a ...any) {
	dir := filepath.Join(home(), ".local", "state", "kennel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "kennel.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
}
