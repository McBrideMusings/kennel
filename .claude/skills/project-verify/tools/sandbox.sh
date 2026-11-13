#!/bin/bash
# Drives every kennel command against a throwaway HOME and checks the output.
set -u
REPO=$(cd "$(dirname "$0")/../../../.." && pwd)
S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
BIN=$S/kennel
(cd "$REPO" && go build -o "$BIN" .) || { echo "FAIL build"; exit 1; }
export HOME=$S KENNEL_CONFIG_DIR=$S/.config/kennel
unset CLAUDE_CONFIG_DIR CODEX_HOME PI_CODING_AGENT_DIR XDG_DATA_HOME
fails=0
check() { # check <name> <command...>: passes when the command exits 0
  local name=$1; shift
  if "$@" >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fails=$((fails + 1)); fi
}

mkdir -p "$S/.claude/skills" "$S/.codex/skills" "$S/.pi/agent/skills" "$S/Work/repo" "$KENNEL_CONFIG_DIR"
touch "$S/.claude/CLAUDE.md"
cat > "$KENNEL_CONFIG_DIR/config.toml" <<'EOF'
[tools.claude]
base   = "~/.claude"
shared = ["skills", "CLAUDE.md"]
[tools.codex]
base   = "~/.codex"
shared = ["skills"]
[tools.pi]
base   = "~/.pi/agent"
shared = ["skills"]
[profiles.personal]
[profiles.work]
claude = "~/.claude-work"
codex  = "~/.codex-work"
pi     = "~/.pi-work"
EOF

check "route add" "$BIN" route add "$S/Work" work --permanent
check "route add with date" "$BIN" route add "$S/Work/repo/sub" personal next wed
check "unknown profile refused" bash -c "! '$BIN' route add '$S/Work' nope"
printf '~/old personal 2000-01-01\n' >> "$KENNEL_CONFIG_DIR/routes"
git -C "$S/Work/repo" init -q && git -C "$S/Work/repo" -c user.name=t -c user.email=t@t commit -q --allow-empty -m init
git -C "$S/Work/repo" worktree add -q "$S/wt"
check "worktree follows its repo" bash -c "cd '$S/wt' && '$BIN' which | grep -q '^profile	work'"
check "env exports work homes" bash -c "cd '$S/Work' && '$BIN' env | grep -q \"CODEX_HOME='$S/.codex-work'\""
check "env exports pi's built-in variable" bash -c "cd '$S/Work' && '$BIN' env | grep -q \"PI_CODING_AGENT_DIR='$S/.pi-work'\""
check "expired route pruned" bash -c "! grep -q '~/old' '$KENNEL_CONFIG_DIR/routes'"
check "outside any route unsets" bash -c "cd '$S' && '$BIN' env | grep -q '^unset CLAUDE_CONFIG_DIR'"
check "outside any route unsets pi" bash -c "cd '$S' && '$BIN' env | grep -q '^unset PI_CODING_AGENT_DIR'"
check "link creates homes" "$BIN" link
check "link made the symlink" test "$(readlink "$S/.claude-work/skills")" = "$S/.claude/skills"
check "link made pi's symlink" test "$(readlink "$S/.pi-work/skills")" = "$S/.pi/agent/skills"
check "homes lists each profile" bash -c "'$BIN' homes codex | grep -q '^personal	$S/.codex$'"
check "zsh hook sets and clears" bash -c "zsh -f -c 'export HOME=$S KENNEL_CONFIG_DIR=$KENNEL_CONFIG_DIR; eval \"\$($BIN init zsh)\"; cd $S/Work; [[ \$CLAUDE_CONFIG_DIR == $S/.claude-work ]] || exit 1; cd $S; [[ -z \${CLAUDE_CONFIG_DIR-} ]]'"
check "doctor passes with hook vars" bash -c "cd '$S/Work' && eval \"\$('$BIN' env)\" && '$BIN' doctor"
rm "$S/.codex-work/skills" && mkdir "$S/.codex-work/skills"
check "link reports a blocked item" bash -c "! '$BIN' link"
mkdir -p "$S/.claude/skills/diagnose" "$S/.claude/skills/other"
touch "$S/.claude/AGENTS.md"
rm -rf "$S/.codex-work/skills"
check "share plan with no [shares] changes nothing" bash -c "'$BIN' share plan | grep -q 'nothing to do'"
cat >> "$KENNEL_CONFIG_DIR/config.toml" <<'EOF'
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
rm "$S/.pi/agent/AGENTS.md"
"$BIN" share apply >/dev/null
mkdir -p "$S/.claude/skills/dropped"
ln -s "$S/.claude/skills/dropped" "$S/.agents/skills/dropped"
ln -s /elsewhere "$S/.agents/skills/foreign"
echo mine > "$S/.agents/skills/realfile"
check "share plan reports the orphan" bash -c "'$BIN' share plan | grep -q '^orphan    ~/.agents/skills/dropped '"
check "share plan leaves foreign links and real files out" bash -c "! '$BIN' share plan | grep -E '^orphan .*(foreign|realfile)'"
check "doctor fails on an orphan" bash -c "cd '$S' && '$BIN' doctor | grep -q '^FAIL  share skills orphan'"
check "share prune removes the orphan" bash -c "'$BIN' share prune | grep -q '^pruned    ~/.agents/skills/dropped' && ! test -L '$S/.agents/skills/dropped'"
check "prune left the listed link, foreign link and real file" bash -c "test -L '$S/.agents/skills/diagnose' && test \"\$(readlink '$S/.agents/skills/foreign')\" = /elsewhere && test \"\$(cat '$S/.agents/skills/realfile')\" = mine"
check "prune logged" grep -q 'share: pruned .*/.agents/skills/dropped' "$S/.local/state/kennel/kennel.log"
check "share plan <share> shows only that share" bash -c "'$BIN' share plan skills | grep -q diagnose && ! '$BIN' share plan skills | grep -q AGENTS.md"
check "share undo <share> <item> removes only that link" bash -c "'$BIN' share undo skills diagnose && ! test -e '$S/.agents/skills/diagnose' && test -L '$S/.pi/agent/AGENTS.md'"
check "share apply <share> relinks only that share" bash -c "'$BIN' share undo instructions && '$BIN' share apply skills && test -L '$S/.agents/skills/diagnose' && ! test -e '$S/.pi/agent/AGENTS.md'"
check "share undo <unknown> exits 1 naming it" bash -c "'$BIN' share undo nope 2>&1 | grep -q 'shares.nope' && ! '$BIN' share undo nope 2>/dev/null"
check "share undo <share> <unknown item> changes nothing" bash -c "! '$BIN' share undo skills nope 2>/dev/null && test -L '$S/.agents/skills/diagnose'"
cp "$KENNEL_CONFIG_DIR/config.toml" "$S/config.before"
check "share add <share> <item> edits items and links it" bash -c "'$BIN' share add skills dropped && grep -qx 'items  = \[\"diagnose\", \"dropped\"\]' '$KENNEL_CONFIG_DIR/config.toml' && test \"\$(readlink '$S/.agents/skills/dropped')\" = '$S/.claude/skills/dropped'"
check "share rm <share> <item> removes the link, then the item" bash -c "'$BIN' share rm skills dropped && ! test -e '$S/.agents/skills/dropped' && cmp -s '$S/config.before' '$KENNEL_CONFIG_DIR/config.toml'"
check "share add --link links the new path" bash -c "'$BIN' share add instructions --link '~/.codex/AGENTS.md' && grep -qx 'links  = \[\"~/.pi/agent/AGENTS.md\", \"~/.codex/AGENTS.md\"\]' '$KENNEL_CONFIG_DIR/config.toml' && test \"\$(readlink '$S/.codex/AGENTS.md')\" = '$S/.claude/AGENTS.md'"
check "share rm --link removes it" bash -c "'$BIN' share rm instructions --link '$S/.codex/AGENTS.md' && ! test -e '$S/.codex/AGENTS.md' && cmp -s '$S/config.before' '$KENNEL_CONFIG_DIR/config.toml'"
check "share rm refuses to empty a share" bash -c "! '$BIN' share rm skills diagnose 2>/dev/null && test -L '$S/.agents/skills/diagnose' && cmp -s '$S/config.before' '$KENNEL_CONFIG_DIR/config.toml'"
check "share add and rm logged" bash -c "grep -q 'share: added dropped to \[shares.skills\] items' '$S/.local/state/kennel/kennel.log' && grep -q 'share: removed .*/.codex/AGENTS.md -> ' '$S/.local/state/kennel/kennel.log'"
sed -i '' 's/^items  = \["diagnose"\]$/items  = "*"/' "$KENNEL_CONFIG_DIR/config.toml"
check "items = \"*\" links every source entry" bash -c "'$BIN' share apply skills && test -L '$S/.agents/skills/other' && test -L '$S/.agents/skills/dropped'"
mkdir -p "$S/.claude/skills/fresh"
check "items = \"*\" links a new entry with no config edit" bash -c "'$BIN' share apply skills | grep -q '^linked    ~/.agents/skills/fresh '"
check "share rm on a \"*\" share writes exclude and removes the link" bash -c "'$BIN' share rm skills other && grep -qx 'exclude = \[\"other\"\]' '$KENNEL_CONFIG_DIR/config.toml' && ! test -e '$S/.agents/skills/other'"
rm -rf "$S/.claude/skills/fresh"
check "doctor flags a link whose source entry is gone" bash -c "cd '$S' && '$BIN' doctor | grep -q '^FAIL  share skills orphan    ~/.agents/skills/fresh '"
check "share prune removes it" bash -c "'$BIN' share prune skills && ! test -L '$S/.agents/skills/fresh' && test -L '$S/.agents/skills/diagnose'"
check "log written" test -s "$S/.local/state/kennel/kennel.log"

V=$S/.local/share/kennel
mkdir -p "$S/.claude/skills/recomp" && echo original > "$S/.claude/skills/recomp/SKILL.md"
check "vault add links the original into the vault" bash -c "'$BIN' vault add '$S/.claude/skills/recomp' && test \"\$(readlink '$S/.claude/skills/recomp')\" = '$V/vault/skills/recomp'"
check "vault add put the original in the Trash" test "$(cat "$S/.Trash/recomp/SKILL.md")" = original
check "the vault folder holds no .git" bash -c "! test -e '$V/vault/.git' && test -f '$V/vault.git/HEAD'"
check "the vault has no remote" test -z "$(git --git-dir="$V/vault.git" remote)"
echo edited > "$S/.claude/skills/recomp/SKILL.md"
check "an outside edit is one sweep commit on the next command" bash -c "'$BIN' vault log recomp | head -1 | grep -q 'sweep: skills/recomp/SKILL.md changed outside kennel'"
check "log lists the add under the sweep" bash -c "test \$('$BIN' vault log recomp | wc -l) -eq 2 && '$BIN' vault log recomp | tail -1 | grep -q 'add skills/recomp from ~/.claude/skills/recomp'"
check "show at the add prints the original text" bash -c "test \"\$('$BIN' vault show skills/recomp/SKILL.md@\$('$BIN' vault log recomp | tail -1 | cut -f1))\" = original"
echo agent > "$S/.claude/skills/recomp/SKILL.md"
check "vault commit names the harness and session" bash -c "'$BIN' vault commit --session s1 --harness codex '$S/.claude/skills/recomp/SKILL.md' && '$BIN' vault log recomp | head -1 | grep -q 'edit skills/recomp/SKILL.md (codex session s1)'"
check "vault add refuses a link" bash -c "! '$BIN' vault add '$S/.claude/skills/recomp' 2>/dev/null"
check "vault add logged" grep -q 'vault add: linked .*/.claude/skills/recomp -> ' "$S/.local/state/kennel/kennel.log"

[ "$fails" -eq 0 ]
