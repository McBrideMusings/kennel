---
name: project-verify
description: "kennel: prove a change works on the real CLI — routing, expiry, worktrees, profile links, shares, doctor and the live zsh, bash and fish hooks — against a throwaway HOME, then read this Mac's real state. Read before reporting any change to kennel works."
---

# Verify kennel

1. `admin test` — vet and the Go tests pass.
2. `bash .claude/skills/project-verify/tools/sandbox.sh` — builds the binary and drives every
   command against a throwaway `HOME`. A pass prints `PASS` on every line and exits 0; it
   deletes its sandbox.
3. `bash .claude/skills/project-verify/tools/hooks.sh` — runs this Mac's real hook scripts through
   `kennel hook codex|pi` (attribution deny, ask→deny, rewrite→deny, a two-file apply_patch,
   a pi deny), then `kennel hooks apply` in a throwaway HOME seeded with copies of the real
   `~/.codex/hooks.json` and `~/.claude/settings.json`: opt-in off changes nothing, a second
   apply is byte-identical, and the herdr and github-owner entries survive; last it loads the
   generated pi extension with pi's own jiti and fires its handlers. Every line `PASS`.
4. `KENNEL_FISH=<fish binary> bash .claude/skills/project-verify/tools/shells.sh` — sources
   `kennel init bash` in `/bin/bash` (3.2) and Homebrew bash and `kennel init fish` in fish,
   cds into two routed directories (one home holding `'` and `\`) and out, and checks the
   variable after each cd and after a `kennel route add` through the wrapper. bash runs
   interactive under `set -u` with PROMPT_COMMAND unset, a string and an array, plus
   `cd dir && cmd` in `bash -c`, a `builtin cd` only the prompt sees, `$?` kept across the
   hook and a failed cd, and an existing `cd` function left alone. Every line `PASS`, exit 0.
   With no fish binary, or no bash 5.1+ for the array cases, those lines print `SKIP` and it
   exits 3: that is an unrun case, not a pass. fish is not
   installed on this Mac; extract Homebrew's bottle without installing it:
   `brew fetch --force-bottle fish`, untar `brew --cache --force-bottle fish` into
   `/private/tmp/claude/kennel/fish`, `install_name_tool -change` both
   `@@HOMEBREW_PREFIX@@/opt/pcre2/lib/libpcre2-{8,32}.0.dylib` to `/opt/homebrew/opt/pcre2/lib/…`,
   then `codesign -f -s -` the binary.
5. Real machine, read-only:
   - `kennel doctor` from `~/Work` exits 0, every line `ok`.
   - `kennel hooks plan` lists every wired Claude hook with its tag and where it goes, and
     exits 0 (1 means a script is unreadable or carries an unknown tag).
   - `kennel which ~/Work/<any repo>` prints `profile work` and both work homes;
     `kennel which ~/Projects` prints `(no route)`.
   - `zsh -i -c 'cd ~/Work; echo $CODEX_HOME'` prints `~/.codex-work` expanded.
   - `CODEX_HOME=~/.codex-work codex login status` reports the work login (or
     `Not logged in` before the first login), and plain `codex login status` the personal one.

Logs: the `project-logs` skill.
