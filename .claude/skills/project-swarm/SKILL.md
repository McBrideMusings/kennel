---
name: project-swarm
description: "kennel: how swarm workers build and test it side by side — heavy commands, the cheap check, shared caches, per-worker throwaway HOME and KENNEL_CONFIG_DIR. Read before the first build or test in a swarm worktree."
---

# kennel swarm worker

- **Heavy commands:** none. `admin test` (`go vet` + `go test`) finishes in about a second;
  run it freely.
- **Shared, never touch:** the real `~/.config/kennel/`, `~/.zshrc`, `~/go/bin/kennel`
  and every profile home. `admin install` and `kennel link` without a sandbox write to
  them, so a worker never runs either.
- **Each worker's own:** a throwaway `HOME` and `KENNEL_CONFIG_DIR`. The Go tests set
  both; `bash .claude/skills/project-verify/tools/sandbox.sh` makes its own and deletes it.
- Build into the worktree or a temp dir (`go build -o <dir>/kennel .`), never `go install`.
