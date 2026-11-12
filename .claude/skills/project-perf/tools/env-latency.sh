#!/bin/bash
# Times `kennel env`, which the zsh hook runs on every cd, inside a git repo with 2000 routes.
# Usage: env-latency.sh [limit-ms]. With no limit it prints the reading and checks nothing.
# KENNEL_BIN overrides the binary (to time another build).
set -u
LIMIT=${1:-}
REPO=$(cd "$(dirname "$0")/../../../.." && pwd)
S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
BIN=${KENNEL_BIN:-$S/kennel}
[ -n "${KENNEL_BIN:-}" ] || (cd "$REPO" && go build -o "$BIN" .) || { echo "FAIL build"; exit 1; }
export HOME=$S KENNEL_CONFIG_DIR=$S/.config/kennel
mkdir -p "$KENNEL_CONFIG_DIR" "$S/Work/repo/sub"
cat > "$KENNEL_CONFIG_DIR/config.toml" <<'EOF'
[tools.claude]
base   = "~/.claude"
shared = []
[tools.codex]
base   = "~/.codex"
shared = []
[profiles.personal]
[profiles.work]
claude = "~/.claude-work"
codex  = "~/.codex-work"
EOF
for i in $(seq 1 2000); do printf '~/Projects/p%d personal\n' "$i"; done > "$KENNEL_CONFIG_DIR/routes"
printf '~/Work work\n' >> "$KENNEL_CONFIG_DIR/routes"
git -C "$S/Work/repo" init -q
cd "$S/Work/repo/sub" || exit 1
"$BIN" env | grep -q CODEX_HOME || { echo "FAIL env did not route the fixture"; exit 1; }
N=100
start=$(perl -MTime::HiRes=time -e 'printf "%.6f", time')
for _ in $(seq 1 $N); do "$BIN" env >/dev/null; done
end=$(perl -MTime::HiRes=time -e 'printf "%.6f", time')
ms=$(perl -e "printf '%.1f', ($end - $start) * 1000 / $N")
printf 'measure\tlimit\tvalue\n'
printf 'kennel env, ms per call (2000 routes, in a git repo)\t%s\t%s\n' "${LIMIT:-none}" "$ms"
[ -n "$LIMIT" ] || exit 0
if perl -e "exit !($ms <= $LIMIT)"; then echo "PASS env latency"; else echo "FAIL env latency"; exit 1; fi
