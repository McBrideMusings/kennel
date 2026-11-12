---
name: project-verify
description: "kennel: prove a change works on the real CLI — routing, expiry, worktrees, profile links, shares, doctor and the live zsh hook — against a throwaway HOME, then read this Mac's real state. Read before reporting any change to kennel works."
---

# Verify kennel

1. `admin test` — vet and the Go tests pass.
2. `bash .claude/skills/project-verify/tools/sandbox.sh` — builds the binary and drives every
   command against a throwaway `HOME`. A pass prints `PASS` on every line and exits 0; it
   deletes its sandbox.
3. Real machine, read-only:
   - `kennel doctor` from `~/Work` exits 0, every line `ok`.
   - `kennel which ~/Work/<any repo>` prints `profile work` and both work homes;
     `kennel which ~/Projects` prints `(no route)`.
   - `zsh -i -c 'cd ~/Work; echo $CODEX_HOME'` prints `~/.codex-work` expanded.
   - `CODEX_HOME=~/.codex-work codex login status` reports the work login (or
     `Not logged in` before the first login), and plain `codex login status` the personal one.

Logs: the `project-logs` skill.
