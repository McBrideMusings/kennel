#!/bin/bash
# Times `kennel hook codex -- <hook>`, which wraps every shared Claude hook on every Codex and
# pi tool call, beside the same hook run bare, in a throwaway HOME with 2000 routes.
# Usage: hook-latency.sh [limit-ms]. The limit applies to the wrapper's added ms per call.
# KENNEL_BIN overrides the binary (to time another build).
set -u
LIMIT=${1:-}
N=50
REPO=$(cd "$(dirname "$0")/../../../.." && pwd)
S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
BIN=${KENNEL_BIN:-$S/kennel}
[ -n "${KENNEL_BIN:-}" ] || (cd "$REPO" && go build -o "$BIN" .) || { echo "FAIL build"; exit 1; }
export HOME=$S KENNEL_CONFIG_DIR=$S/.config/kennel
mkdir -p "$KENNEL_CONFIG_DIR" "$S/Work/repo"
printf '[tools.claude]\nbase = "~/.claude"\nshared = []\n[profiles.work]\nclaude = "~/.claude-work"\n' > "$KENNEL_CONFIG_DIR/config.toml"
for i in $(seq 1 2000); do printf '~/Projects/p%d work\n' "$i"; done > "$KENNEL_CONFIG_DIR/routes"
printf '~/Work work\n' >> "$KENNEL_CONFIG_DIR/routes"
git -C "$S/Work/repo" init -q
printf '#!/bin/bash\ncat >/dev/null\n' > "$S/hook.sh"
P=$(printf '{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"%s","tool_input":{"command":"ls"}}' "$S/Work/repo")
ms() { python3 -c 'import time; print(time.monotonic_ns() // 1000000)'; }
t0=$(ms); for _ in $(seq $N); do printf '%s' "$P" | bash "$S/hook.sh"; done; t1=$(ms)
for _ in $(seq $N); do printf '%s' "$P" | "$BIN" hook codex -- bash "$S/hook.sh"; done; t2=$(ms)
bare=$(( (t1 - t0) / N )); wrapped=$(( (t2 - t1) / N )); added=$(( wrapped - bare ))
echo "bare hook ${bare} ms, wrapped ${wrapped} ms, added ${added} ms per call (N=$N, load $(sysctl -n vm.loadavg))"
if [ -n "$LIMIT" ] && [ "$added" -gt "$LIMIT" ]; then echo "FAIL added ${added} ms > ${LIMIT} ms"; exit 1; fi
