# agenthome

Go CLI that routes directories to coding-agent profiles; see README.md for the model.

- One package, `main`. `config.go` loads and validates config.toml, `routes.go` owns the
  routes file and resolution, `when.go` parses expiry phrases, `link.go` checks and creates
  profile symlinks, `main.go` holds the commands and the zsh hook.
- `agenthome env` runs on every `cd` in every shell. Keep it to one config read, one routes
  read and one `git rev-parse`.
- `link` never moves or deletes anything; it reports and exits 1.
- Tests run against a throwaway `HOME` and `AGENTHOME_CONFIG_DIR`, never the real config.
- Verify with the `verify-project` skill; `admin test` is the build check.
