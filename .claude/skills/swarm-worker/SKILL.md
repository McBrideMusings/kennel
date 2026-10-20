---
name: swarm-worker
description: How a swarm worker builds and tests agenthome beside other workers. Read before the first build or test in a swarm worktree.
---

# agenthome swarm worker

- **Heavy commands:** none. `admin test` (`go vet` + `go test`) finishes in about a second;
  run it freely.
- **Shared, never touch:** the real `~/.config/agenthome/`, `~/.zshrc`, `~/go/bin/agenthome`
  and every profile home. `admin install` and `agenthome link` without a sandbox write to
  them, so a worker never runs either.
- **Each worker's own:** a throwaway `HOME` and `AGENTHOME_CONFIG_DIR`. The Go tests set
  both; `bash .claude/skills/verify-project/sandbox.sh` makes its own and deletes it.
- Build into the worktree or a temp dir (`go build -o <dir>/agenthome .`), never `go install`.
