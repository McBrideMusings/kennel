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
mkdir -p "$S/.claude/skills/diagnose" "$S/.claude/skills/other"
touch "$S/.claude/AGENTS.md"
rm -rf "$S/.codex-work/skills"
check "share plan with no [shares] changes nothing" bash -c "'$BIN' share plan | grep -q 'nothing to do'"
cat >> "$AGENTHOME_CONFIG_DIR/config.toml" <<'EOF'
[shares.instructions]
source = "~/.claude/AGENTS.md"
links  = ["~/.pi/agent/AGENTS.md"]
[shares.skills]
source = "~/.claude/skills"
into   = "~/.agents/skills"
items  = ["diagnose"]
EOF
check "share plan reports missing" bash -c "'$BIN' share plan | grep -c '^missing' | grep -q '^2$'"
check "share apply links" "$BIN" share apply
check "share linked the file" test "$(readlink "$S/.pi/agent/AGENTS.md")" = "$S/.claude/AGENTS.md"
check "share linked only the listed skill" bash -c "test -L '$S/.agents/skills/diagnose' && ! test -e '$S/.agents/skills/other'"
check "doctor lists shares" bash -c "cd '$S' && '$BIN' doctor | grep -q 'share skills'"
check "share undo removes links" bash -c "'$BIN' share undo && ! test -e '$S/.pi/agent/AGENTS.md'"
echo mine > "$S/.pi/agent/AGENTS.md"
check "share apply reports a blocked file" bash -c "! '$BIN' share apply"
check "blocked file left alone" test "$(cat "$S/.pi/agent/AGENTS.md")" = mine
check "doctor fails on a blocked share" bash -c "cd '$S' && ! '$BIN' doctor"
check "log written" test -s "$S/.local/state/agenthome/agenthome.log"

[ "$fails" -eq 0 ]
