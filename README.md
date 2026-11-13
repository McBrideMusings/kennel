# kennel

Routes directories to profiles for coding-agent CLIs. A profile gives each tool its own home
directory, and with it its own login, so `claude` and `codex` started anywhere under `~/Work`
use the work accounts while everything else uses the personal ones.

Each CLI reads its home from one environment variable, read once at startup:

| Tool | Variable | Default home |
| --- | --- | --- |
| Claude Code | `CLAUDE_CONFIG_DIR` | `~/.claude` |
| Codex | `CODEX_HOME` | `~/.codex` |

A zsh hook runs `kennel env` on every `cd` and exports or unsets those variables for the
new directory, so a CLI started from that shell, or from anything it launches, gets the
right home.

## Install

```sh
go install github.com/McBrideMusings/kennel@latest   # or `go install .` in a checkout
echo 'command -v kennel >/dev/null && eval "$(kennel init zsh)"' >> ~/.zshrc
```

## Config

`~/.config/kennel/config.toml` (`KENNEL_CONFIG_DIR` moves the directory):

```toml
[tools.claude]
# env = "CLAUDE_CONFIG_DIR"   built in for claude and codex; set it to override
base   = "~/.claude"
shared = ["skills", "CLAUDE.md", "settings.json"]

[tools.codex]
base   = "~/.codex"
shared = ["skills", "AGENTS.md", "config.toml"]

[profiles.personal]           # no homes: every tool uses its default

[profiles.work]
claude = "~/.claude-work"
codex  = "~/.codex-work"
```

- `base` and `shared` are required for every tool. `env` is required only for a tool
  kennel has no built-in variable for.
- A tool left out of a profile gets its variable unset, so it runs on its default home.
- `shared` names items inside `base`. `kennel link` creates each profile home and
  symlinks every shared item in it back to the base, so skills, instructions and settings
  are edited once. Everything not shared stays per profile: the login and the account's
  own state.

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

An orphan is a symlink in a folder share's `into` that points inside its `source` under a
name no share lists, as when an item is dropped from `items` after `apply`. `plan` and
`doctor` report each one, `doctor` as a FAIL.

Like `link`, `share apply` never moves or deletes a file: a real file or directory where a
link goes is reported as blocked and left alone. Profile homes need nothing extra, since
`~/.codex-work/AGENTS.md` links to `~/.codex/AGENTS.md`, which follows the share.

## Hooks

An optional `[hooks]` table runs Claude Code's hook scripts under Codex and pi too. With
neither key set, `kennel hooks apply` writes nothing.

```toml
[hooks]
codex = true    # entries in <codex base>/hooks.json
pi    = true    # ~/.pi/agent/extensions/kennel-hooks.ts ($PI_CODING_AGENT_DIR moves it)
```

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
creates) is logged with a timestamp to `~/.local/state/kennel/kennel.log`.

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

Log a new profile in once with its variable set, for example
`CODEX_HOME=~/.codex-work codex login`.
