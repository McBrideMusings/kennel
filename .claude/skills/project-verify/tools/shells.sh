#!/bin/bash
# Drives `kennel init bash` and `kennel init fish` in real interactive shells
# against a throwaway HOME: cd into a routed directory, then out of it, and
# reads the variables each shell holds after each cd.
#   KENNEL_FISH  the fish binary (default: fish on PATH). With none, every fish
#                check prints SKIP and the script exits 3, never a silent pass;
#                the same holds for the PROMPT_COMMAND array cases with no bash 5.
#   KENNEL_BASHES  space-separated bash binaries (default: /bin/bash and bash on PATH)
set -u
REPO=$(cd "$(dirname "$0")/../../../.." && pwd)
S=$(mktemp -d)
trap 'rm -rf "$S"' EXIT
BIN=$S/kennel
(cd "$REPO" && go build -o "$BIN" .) || { echo "FAIL build"; exit 1; }
export HOME=$S KENNEL_CONFIG_DIR=$S/.config/kennel
unset CLAUDE_CONFIG_DIR CODEX_HOME PI_CODING_AGENT_DIR
mkdir -p "$S/Work/it's here" "$KENNEL_CONFIG_DIR"
cat > "$KENNEL_CONFIG_DIR/config.toml" <<'EOF'
[tools.claude]
base   = "~/.claude"
shared = []
[profiles.personal]
[profiles.work]
claude = "~/.claude-work"
[profiles.odd]
claude = "~/it's a \\ home"
EOF
"$BIN" route add "$S/Work" work --permanent >/dev/null
"$BIN" route add "$S/Work/it's here" odd --permanent >/dev/null
fails=0 skips=0
expect() { # expect <name> <file> <wanted output>
  if [ "$(cat "$2")" = "$3" ]; then echo "PASS $1"; else
    echo "FAIL $1"; echo "  got:"; sed 's/^/    /' "$2"; echo "  want:"; printf '%s\n' "$3" | sed 's/^/    /'
    fails=$((fails + 1))
  fi
}
WANT="in Work: $S/.claude-work
in odd: $S/it's a \\ home
in HOME: unset"

# bash: an interactive shell reading its commands from stdin prints a prompt,
# and so runs PROMPT_COMMAND, before each line, like a person at a terminal.
# Every interactive run sets -u, so the hook must not read an unset variable.
ibash() { # ibash <bash> <out> <script>: run script in an interactive bash
  "$1" --norc --noprofile -i > "$2" 2>/dev/null <<<"PS1=''
set -u
$3"
}
init="eval \"\$('$BIN' init bash)\""
bashes=${KENNEL_BASHES:-"/bin/bash $(command -v bash)"}
arrays=0
for b in $bashes; do
  v=$("$b" -c 'echo ${BASH_VERSINFO[0]}.${BASH_VERSINFO[1]}')
  for pc in none string array; do
    case $pc in
      none) setup='unset PROMPT_COMMAND' ;;
      string) setup='PROMPT_COMMAND="echo -n >/dev/null"' ;;
      array) [ "${v%%.*}" -ge 5 ] || continue; arrays=$((arrays + 1)); setup='PROMPT_COMMAND=("echo -n >/dev/null")' ;;
    esac
    ibash "$b" "$S/o" "$setup
$init
$init
cd \"$S/Work\"
echo \"in Work: \${CLAUDE_CONFIG_DIR-unset}\"
pushd \"$S/Work/it's here\" >/dev/null
echo \"in odd: \${CLAUDE_CONFIG_DIR-unset}\"
cd \"$S\"
echo \"in HOME: \${CLAUDE_CONFIG_DIR-unset}\""
    expect "bash $v hook sets and clears, set -u (PROMPT_COMMAND $pc)" "$S/o" "$WANT"
    ibash "$b" "$S/o" "$setup
$init
$init
echo \"hooks: \$(printf '%s;' \"\${PROMPT_COMMAND[@]}\" | grep -o _kennel_hook | wc -l | tr -d ' ')\""
    expect "bash $v hook added once when sourced twice (PROMPT_COMMAND $pc)" "$S/o" "hooks: 1"
  done
  ibash "$b" "$S/o" "$init
builtin cd \"$S/Work\"
echo \"prompt only: \${CLAUDE_CONFIG_DIR-unset}\""
  expect "bash $v PROMPT_COMMAND applies a cd the wrappers did not see" "$S/o" "prompt only: $S/.claude-work"
  ibash "$b" "$S/o" "$init
false
echo \"rc: \$?\"
cd /nonexistent-kennel-dir
echo \"rc after failed cd: \$?\""
  expect "bash $v hook and cd keep the exit status" "$S/o" "rc: 1
rc after failed cd: 1"
  "$b" --norc --noprofile -c "$init; cd \"$S/Work\" && echo \"one line: \${CLAUDE_CONFIG_DIR-unset}\"; pushd \"$S\" >/dev/null && echo \"pushd: \${CLAUDE_CONFIG_DIR-unset}\"; popd >/dev/null && echo \"popd: \${CLAUDE_CONFIG_DIR-unset}\"" > "$S/o" 2>&1
  expect "bash $v non-interactive cd, pushd, popd && cmd see the route at once" "$S/o" "one line: $S/.claude-work
pushd: unset
popd: $S/.claude-work"
  ibash "$b" "$S/o" "cd() { builtin cd \"\$@\" && echo mine; }
$init
cd \"$S/Work\"
echo \"own cd: \${CLAUDE_CONFIG_DIR-unset}\""
  expect "bash $v an existing cd function is kept, the prompt still routes" "$S/o" "mine
own cd: $S/.claude-work"
  ibash "$b" "$S/o" "$init
cd \"$S\"
kennel route add \"$S\" work >/dev/null
echo \"after route add: \${CLAUDE_CONFIG_DIR-unset}\""
  expect "bash $v kennel wrapper applies a new route at once" "$S/o" "after route add: $S/.claude-work"
  "$BIN" route rm "$S" >/dev/null
done
if [ $arrays -eq 0 ]; then
  echo "SKIP bash PROMPT_COMMAND array cases (no bash 5.1+ in: $bashes)"
  skips=$((skips + 1))
fi

FISH=${KENNEL_FISH:-$(command -v fish)}
if [ -z "$FISH" ]; then
  echo "SKIP fish hook sets and clears (no fish binary; set KENNEL_FISH)"
  echo "SKIP fish kennel wrapper applies a new route at once (no fish binary)"
  skips=$((skips + 2))
else
  v=$("$FISH" --version)
  out=$S/fish.out
  "$FISH" --no-config -c "
'$BIN' init fish | source
cd '$S/Work'
echo \"in Work: \$CLAUDE_CONFIG_DIR\"
cd \"$S/Work/it's here\"
echo \"in odd: \$CLAUDE_CONFIG_DIR\"
cd '$S'
set -q CLAUDE_CONFIG_DIR; and echo \"in HOME: \$CLAUDE_CONFIG_DIR\"; or echo 'in HOME: unset'
" > "$out" 2>&1
  expect "fish hook sets and clears ($v)" "$out" "$WANT"
  out=$S/fish-wrap.out
  "$FISH" --no-config -c "
'$BIN' init fish | source
cd '$S'
kennel route add '$S' work >/dev/null
echo \"after route add: \$CLAUDE_CONFIG_DIR\"
" > "$out" 2>&1
  expect "fish kennel wrapper applies a new route at once ($v)" "$out" "after route add: $S/.claude-work"
fi

[ $fails -eq 0 ] || exit 1
[ $skips -eq 0 ] || exit 3
