#!/bin/bash
# Drives every agenthome command against a throwaway HOME and checks the output.
set -u
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
BIN=$S/agenthome
(cd "$REPO" && go build -o "$BIN" .) || { echo "FAIL build"; exit 1; }
export HOME=$S AGENTHOME_CONFIG_DIR=$S/.config/agenthome
unset CLAUDE_CONFIG_DIR CODEX_HOME
fails=0
check() { # check <name> <command...>: passes when the command exits 0
  local name=$1; shift
  if "$@" >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fails=$((fails + 1)); fi
}

mkdir -p "$S/.claude/skills" "$S/.codex/skills" "$S/Work/repo" "$AGENTHOME_CONFIG_DIR"
touch "$S/.claude/CLAUDE.md"
cat > "$AGENTHOME_CONFIG_DIR/config.toml" <<'EOF'
[tools.claude]
base   = "~/.claude"
shared = ["skills", "CLAUDE.md"]
[tools.codex]
base   = "~/.codex"
shared = ["skills"]
[profiles.personal]
[profiles.work]
claude = "~/.claude-work"
codex  = "~/.codex-work"
EOF

check "route add" "$BIN" route add "$S/Work" work --permanent
check "route add with date" "$BIN" route add "$S/Work/repo/sub" personal next wed
check "unknown profile refused" bash -c "! '$BIN' route add '$S/Work' nope"
printf '~/old personal 2000-01-01\n' >> "$AGENTHOME_CONFIG_DIR/routes"
git -C "$S/Work/repo" init -q && git -C "$S/Work/repo" -c user.name=t -c user.email=t@t commit -q --allow-empty -m init
git -C "$S/Work/repo" worktree add -q "$S/wt"
check "worktree follows its repo" bash -c "cd '$S/wt' && '$BIN' which | grep -q '^profile	work'"
check "env exports work homes" bash -c "cd '$S/Work' && '$BIN' env | grep -q \"CODEX_HOME='$S/.codex-work'\""
check "expired route pruned" bash -c "! grep -q '~/old' '$AGENTHOME_CONFIG_DIR/routes'"
check "outside any route unsets" bash -c "cd '$S' && '$BIN' env | grep -q '^unset CLAUDE_CONFIG_DIR'"
check "link creates homes" "$BIN" link
check "link made the symlink" test "$(readlink "$S/.claude-work/skills")" = "$S/.claude/skills"
check "homes lists each profile" bash -c "'$BIN' homes codex | grep -q '^personal	$S/.codex$'"
check "zsh hook sets and clears" bash -c "zsh -f -c 'export HOME=$S AGENTHOME_CONFIG_DIR=$AGENTHOME_CONFIG_DIR; eval \"\$($BIN init zsh)\"; cd $S/Work; [[ \$CLAUDE_CONFIG_DIR == $S/.claude-work ]] || exit 1; cd $S; [[ -z \${CLAUDE_CONFIG_DIR-} ]]'"
check "doctor passes with hook vars" bash -c "cd '$S/Work' && eval \"\$('$BIN' env)\" && '$BIN' doctor"
rm "$S/.codex-work/skills" && mkdir "$S/.codex-work/skills"
check "link reports a blocked item" bash -c "! '$BIN' link"
check "log written" test -s "$S/.local/state/agenthome/agenthome.log"

[ "$fails" -eq 0 ]
