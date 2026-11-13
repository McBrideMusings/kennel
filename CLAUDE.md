# kennel

Go CLI that routes directories to coding-agent profiles; see README.md for the model.

- One package, `main`. `config.go` loads and validates config.toml, `routes.go` owns the
  routes file and resolution, `when.go` parses expiry phrases, `link.go` checks and creates
  profile symlinks and share links, `record.go` keeps the links record of share links kennel
  made, `shareedit.go` holds `share add` and `share rm`, which
  rewrite config.toml through `tomledit.go`'s editor of one share array, `hooks.go` generates Codex and pi hook files from Claude's hook list,
  `hookrun.go` is the `kennel hook` wrapper, `vault.go` holds the vault store and `kennel vault`,
  `harness.go` is `kennel harness show`, which reads what Claude, Codex or pi gets in one scope,
  `recent.go` is `kennel harness recent`, the project picker's folders,
  `mcp.go` is `kennel mcp`, the stdio MCP server serving `app/harness.html` (marked and DOMPurify inlined from
  `app/marked.umd.js` and `app/purify.min.js`) and running every app action as a kennel command,
  `file.go` is `kennel file log` and `file show` and the one git runner `runGit`,
  `main.go` holds the commands and the zsh hook.
- `kennel env` runs on every `cd` in every shell. Keep it to one config read, one routes
  read and one `git rev-parse`.
- `link` never moves or deletes anything; it reports and exits 1. `vault add` is the one
  exception: it moves the original to the Trash and links the vault copy in its place.
- The vault's git calls always pass `--git-dir` and `--work-tree`; the vault has no `.git`
  and no remote.
- Tests run against a throwaway `HOME` and `KENNEL_CONFIG_DIR`, never the real config.
- Verify with the `project-verify` skill; `admin test` is the build check.
