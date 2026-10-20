---
name: verify-project
description: Verify agenthome works — routing, expiry, worktrees, links, doctor and the live zsh hook — against a throwaway HOME, then read the real machine's state.
---

# Verify agenthome

1. `admin test` — vet and the Go tests pass.
2. `bash .claude/skills/verify-project/sandbox.sh` — builds the binary and drives every
   command against a throwaway `HOME`. A pass prints `PASS` on every line and exits 0; it
   deletes its sandbox.
3. Real machine, read-only:
   - `agenthome doctor` from `~/Work` exits 0, every line `ok`.
   - `agenthome which ~/Work/<any repo>` prints `profile work` and both work homes;
     `agenthome which ~/Projects` prints `(no route)`.
   - `zsh -i -c 'cd ~/Work; echo $CODEX_HOME'` prints `~/.codex-work` expanded.
   - `CODEX_HOME=~/.codex-work codex login status` reports the work login (or
     `Not logged in` before the first login), and plain `codex login status` the personal one.

Logs: `~/.local/state/agenthome/agenthome.log`.
