# kennel

Routes directories to profiles for coding-agent CLIs. A profile gives each tool its own home
directory, and with it its own login, so `claude` and `codex` started anywhere under `~/Work`
use the work accounts while everything else uses the personal ones.

Each CLI reads its home from one environment variable, read once at startup:

| Tool | Variable | Default home |
| --- | --- | --- |
| Claude Code | `CLAUDE_CONFIG_DIR` | `~/.claude` |
| Codex | `CODEX_HOME` | `~/.codex` |
| Pi | `PI_CODING_AGENT_DIR` | `~/.pi/agent` |

A shell hook (zsh, bash or fish) runs `kennel env` on every `cd` and exports or unsets those
variables for the new directory, so a CLI started from that shell, or from anything it
launches, gets the right home. bash has no directory-change hook, so `kennel init bash` wraps
`cd`, `pushd` and `popd` (leaving alone any you already define as functions) and checks
`$PWD` from `PROMPT_COMMAND` for every other way of changing directory.

## Install

```sh
go install github.com/McBrideMusings/kennel@latest   # or `go install .` in a checkout
echo 'command -v kennel >/dev/null && eval "$(kennel init zsh)"' >> ~/.zshrc
# bash: echo 'command -v kennel >/dev/null && eval "$(kennel init bash)"' >> ~/.bashrc
# fish: echo 'command -q kennel; and kennel init fish | source' >> ~/.config/fish/config.fish
```

## Config

`~/.config/kennel/config.toml` (`KENNEL_CONFIG_DIR` moves the directory):

```toml
[tools.claude]
# env = "CLAUDE_CONFIG_DIR"   built in for claude, codex and pi; set it to override
base   = "~/.claude"
shared = ["skills", "CLAUDE.md", "settings.json"]

[tools.codex]
base   = "~/.codex"
shared = ["skills", "AGENTS.md", "config.toml"]

[profiles.personal]           # no homes: every tool uses its default

[profiles.work]
claude = "~/.claude-work"
codex  = { home = "~/.codex-work", shared = ["skills"] }
```

- `base` and `shared` are required for every tool. `env` is required only for a tool
  kennel has no built-in variable for.
- A tool left out of a profile gets its variable unset, so it runs on its default home.
- `shared` names items inside `base`. `kennel link` creates each profile home and
  symlinks every shared item in it back to the base, so skills, instructions and settings
  are edited once. Everything not shared stays per profile: the login and the account's
  own state.
- A profile's tool is a home path, which links the tool's `shared` items, or a table with
  `home` and its own `shared`, which replaces the tool's list for that home: above,
  `~/.codex-work` links only `skills` and keeps its own `AGENTS.md`. When an item leaves the
  list a home uses, `kennel link` and `kennel doctor` report its link to the base as
  unshared and exit 1; kennel never removes it, so delete it by hand to give the profile
  its own copy.

## Shares

An optional `[shares]` table links one tool's files into another tool's home. Nothing is
shared until you add an entry; with none, every `share` command does nothing.

```toml
[shares.instructions]          # a file share: one real file, linked from each path
source = "~/.claude/AGENTS.md"
links  = ["~/.codex/AGENTS.md", "~/.pi/agent/AGENTS.md"]

[shares.skills]                # a folder share: one link per named item
source = "~/.claude/skills"
into   = "~/.agents/skills"
items  = ["diagnose", "research"]
```

A share has `source` and either `links` or `into` and `items`, never both. Each item is one
name with no `/`.

`items = "*"` links every entry in `source` but dotfiles, read each time a command runs, so
a skill added to `source` is linked by the next `share apply` with no config edit.
`exclude = ["grill-me"]` beside it leaves those entries out; `exclude` without `"*"` is an
error. A `"*"` share claims every name in its `into`, so another share may link a name there
only when the `"*"` share excludes it, and two `"*"` shares never share an `into`. When
`source` is missing or not a folder, `plan` and `doctor` report it once and nothing in `into`
counts as an orphan. One link path belongs to one share: `kennel` refuses a config where two
shares link the same path, one share lists a path twice, or a share links a path inside
another link (a file link under a folder share's item, or under another file share's link).

| Command | Does |
| --- | --- |
| `kennel share plan` | prints every share link as ok, missing, wrong, blocked, no source or orphan; changes nothing |
| `kennel share apply` | creates each missing link and any parent directory; exits 1 if a link needs a hand fix |
| `kennel share undo` | removes only symlinks that point at a configured share's source, orphans included |
| `kennel share prune` | removes orphans only |

Each command takes an optional share name, and after it an item, to act on less than every
share: `kennel share undo skills` removes only the `skills` share's links, and
`kennel share undo skills grill-me` only that one link. The item is a folder share's item
name, an orphan's name in its `into`, or a file share's link path (a relative path is read
from the current directory). An unknown share or item
exits 1 and changes nothing.

`kennel share add skills grill-me research` adds each item to the share's `items` and links
it; `kennel share rm skills grill-me` removes its link, only if it points into the share's
`source`, and drops it from `items`. A real file or a link elsewhere at that path is
reported, left alone, and makes `rm` exit 1. A file share takes link paths after `--link`:
`kennel share add instructions --link ~/.gemini/AGENTS.md`. Both edit `config.toml` line by
line, so every comment and key outside the edited array stays as written, and log each
config edit and link to kennel's change log. `add` refuses an item missing from `source`;
`rm` refuses to empty a share (run `share undo` and delete the table by hand). On a `"*"` share,
`rm` adds the entry to `exclude` (writing an `exclude` line under `items` when there is none)
and removes its link, and `add` takes it back out of `exclude` and links it.

An orphan is a symlink in a folder share's `into` that points inside its `source` under a
name no share lists, as when an item is dropped from `items` after `apply`, or, with
`items = "*"`, when an entry is excluded or deleted from `source`. A file share's link is
an orphan once its path leaves every share's `links` while it still points where kennel
linked it. `plan` and `doctor` report each one, `doctor` as a FAIL.

kennel finds a file share's orphans in `~/.local/state/kennel/links`, its record of every
share link it made: one line per link, the path and its target separated by a tab. `apply`
and `add` record each link in place, a link made before the record existed included, and
`undo`, `rm` and `prune` drop the lines of the links they remove. A recorded link no share
lists belongs to no share, so only the unscoped `kennel share prune` and `kennel share undo`
remove it. A recorded path that is gone or now points elsewhere is left alone, and `prune`
drops its line.

Like `link`, `share apply` never moves or deletes a file: a real file or directory where a
link goes is reported as blocked and left alone. Profile homes need nothing extra, since
`~/.codex-work/AGENTS.md` links to `~/.codex/AGENTS.md`, which follows the share.

## Hooks

An optional `[hooks]` table runs Claude Code's hook scripts under Codex and pi too. With
neither key set, `kennel hooks apply` writes nothing.

```toml
[hooks]
codex = true    # entries in <codex base>/hooks.json
pi    = true    # <pi base>/extensions/kennel-hooks.ts
```

Without a `[tools.pi]` block the pi extension goes in `$PI_CODING_AGENT_DIR` when the shell
sets it, else `~/.pi/agent`. A profile home sees the generated file only when the base entry
holding it, `hooks.json` for Codex or `extensions` for pi, is in the shared list that home
uses;
`hooks plan` and `hooks apply` name each profile home that misses it and exit 1.

A script in `<claude base>/hooks/` leaves Claude only when one of its first 20 lines is a
tag; a script with none is claude-only:

| Tag | Means |
| --- | --- |
| `# kennel-hook: portable` | runs unchanged on Codex's and pi's payloads |
| `# kennel-hook: adapter` | an edit guard that reads only `tool_input.file_path`; under Codex each file of an `apply_patch` is checked as its own call |

| Command | Does |
| --- | --- |
| `kennel hooks plan` | prints every wired Claude hook, its tag and where it goes, and whether each file would change |
| `kennel hooks apply` | writes the files the opt-in names, only when their bytes change |
| `kennel hooks list codex\|pi` | the shared hooks as JSON; the pi extension reads this when pi starts |
| `kennel hook codex\|pi -- <cmd>` | runs one hook on that tool's payload; every generated entry calls it |

In Codex's `hooks.json`, apply replaces only handlers whose command points into Claude's
hooks directory; every other tool's group stays where it was, and generated groups follow
in `settings.json` order. Codex skips a new or changed entry until it is trusted in `/hooks`,
and keys trust by file, event, group index and handler index, so the first apply asks for
trust once and an unchanged apply rewrites nothing.

`kennel hook` sets `CLAUDE_PROJECT_DIR` to the payload's `cwd` and `CLAUDE_HOME` to the Claude
home kennel routes that directory to. Under Codex it turns an `ask`, which Codex treats as a
failed hook that lets the call run, into a `deny` with the same reason, and a rewrite
(`updatedInput`), which Codex honours only beside `allow`, into a `deny` naming the
rewritten command. Under pi, `ask` opens a confirm dialog, `deny` blocks the call and a
rewrite changes the call's input.

## Vault

The vault keeps skills and snippets on this Mac, with every change committed. It lives in
`~/.local/share/kennel/` (`$XDG_DATA_HOME/kennel/` when set): the files in `vault/`
(`skills/<name>/`, `snippets/<name>.md`) and their history in `vault.git/` beside it, so
the folder holds no `.git`. The repository has no remote.

| Command | Does |
| --- | --- |
| `kennel vault add <path>` | copies a skill folder or snippet `.md` into the vault, commits it, moves the original to `~/.Trash` and puts a link to the vault copy where it was |
| `kennel vault log <item>` | the item's commits: short sha, time, message |
| `kennel vault show <item>[@<rev>]` | the item at a commit: a file's text, or each file of a skill under a `==> path <==` header |
| `kennel vault commit --session <id> --harness <h> <file>` | commits one file an agent edited, naming the harness and session |

An item is a path inside the vault (`skills/recomp`, `skills/recomp/SKILL.md`) or a bare
name (`recomp`). Every vault command first commits any change made outside kennel as one
`sweep` commit. `vault add` is the one kennel command that moves a file of yours; it
refuses a link, a name the vault already holds and a folder that is its own git repository.

## Reading what a harness gets

| Command | Does |
| --- | --- |
| `kennel harness show <skills\|guidance\|permissions\|hooks\|mcp> [--scope global\|<dir>] [--harness claude\|codex\|pi] [--json]` | lists one section for one harness: a Project block for the folder's git root, then a Global block from the home its route picks |
| `kennel file log <path> [--json]` | the commits that touched the file, from the vault's history when the file is in the vault, else from its git repository; `[]` when no repository tracks it |
| `kennel file show <path>[@<rev>]` | the file's text at a commit |

`--scope global` reads each harness's base home with no profile. Codex hooks that run one of
Claude's hook scripts are marked as generated from Claude's hooks.

| Command | Does |
| --- | --- |
| `kennel harness recent [--json]` | the folders Claude sessions ran in, newest first, from every profile's Claude `projects` folder; worktrees, temp folders and `~/Library` are left out |
| `kennel mcp` | the harness app as a stdio MCP server |

### The harness app

`kennel mcp` serves one MCP App, `ui://kennel/harness`, and a `harness` tool that opens it.
The tool takes an optional `directory` (left out: the Global scope) and `harness` (`claude`,
`codex` or `pi`; left out: Claude). The app shows one scope and one harness at a time: a
file tree, the selected file's text and its commits. It reads everything by running kennel
commands through the server's app-only `kennel` tool, which runs only `harness show`,
`harness recent`, `file log` and `file show`, and reads with `file log` and `file show` only
files a `harness show` it ran has listed.

To add it to Claude Desktop, put this in
`~/Library/Application Support/Claude/claude_desktop_config.json` and restart Claude Desktop:

```json
{
  "mcpServers": {
    "kennel": { "command": "/Users/you/go/bin/kennel", "args": ["mcp"] }
  }
}
```

## Routes

`~/.config/kennel/routes`, one `<path> <profile> [YYYY-MM-DD]` per line:

- The longest matching path wins.
- Inside a git repo or linked worktree, kennel matches the main repo's path rather than
  the current directory, so a worktree anywhere on disk follows its repo's route.
- A row whose date has passed is deleted on the next `cd`.
- A row naming a profile the config lacks is ignored, and `route list` and `doctor` flag it.

```sh
kennel route add ~/Work work --permanent
kennel route add ~/Projects/client-x work next fri   # expires; default is tomorrow
kennel route rm ~/Projects/client-x
kennel route list
```

`when` accepts `today`, `tomorrow`, `3d`, `2 weeks`, `1m`, `wed`, `next fri`, `eow`,
`weekend`, `eom` or `2026-08-15`. Weekday names survive one or two typos.

## Checking it

| Command | Prints |
| --- | --- |
| `kennel which [dir]` | the profile a directory gets and each tool's home |
| `kennel homes <tool>` | each profile's home for one tool, tab-separated, for scripts |
| `kennel doctor` | config, every shared symlink, every share link, whether opted-in hook files are current, ignored routes, and whether this shell's variables match the route for `$PWD`; exits 1 on any problem |

Every change kennel makes on disk (route edits, expiry drops, homes and links it
creates) is logged with a timestamp to `~/.local/state/kennel/kennel.log`. The share links
it made are also kept, one per line, in `~/.local/state/kennel/links`; `kennel env` reads
neither file.

## What each tool keeps per account

**Claude Code.** The login is a keychain entry named from the hash of `CLAUDE_CONFIG_DIR`,
so one profile's token does not work in another. The account file `.claude.json` (account,
user and machine ids, onboarding state) sits inside each profile home, except for the
default home, whose copy is `~/.claude.json`, beside it. Keep `.claude.json`,
`settings.local.json` and usage caches out of `shared`. The `env` block in `settings.json`
reaches only Claude's child processes, never Claude itself, which is why the variable has
to come from the shell.

**Codex.** The login is `auth.json` inside `CODEX_HOME`. An empty home reports
`Not logged in` from `codex login status`. The sqlite databases (`state_*`, `logs_*`,
`thread_history_*`, `memories_*`), `sessions/`, `history.jsonl`, `installation_id` and
`.codex-global-state.json` belong to one install and stay unshared. Codex's own
`--profile` flag is unrelated: it picks a settings overlay inside one home and never
changes `CODEX_HOME`.

**Pi.** The login is `auth.json` (API keys and OAuth credentials) inside
`PI_CODING_AGENT_DIR`. `sessions/` and `trust.json` (the projects trusted to load their
`.pi` config) belong to one account, and `models-store.json` and `crashes.json` are caches;
keep them all out of `shared`. `settings.json`, `keybindings.json`, `models.json`,
`mcp.json`, `AGENTS.md`, `SYSTEM.md`, `APPEND_SYSTEM.md`, `extensions/`, `skills/`,
`prompts/` and `themes/` are user configuration and can be shared; `npm/` and `git/` hold
the Pi packages `settings.json` declares, so share them with it, and `bin/` holds the `fd`
and `rg` Pi downloads. `install/` holds Pi's own releases, which the `pi` launcher finds from
its own path, never from `PI_CODING_AGENT_DIR`; leave it in `~/.pi/agent`, unshared.

Log a new profile in once with its variable set, for example
`CODEX_HOME=~/.codex-work codex login`.
