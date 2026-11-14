#!/usr/bin/env bash
# Drives the hook wrapper over this Mac's real Claude hook scripts and checks apply in a
# throwaway HOME seeded with copies of the real hooks.json and settings.json. Real files are only read.
set -uo pipefail
REPO=$(cd "$(dirname "$0")/../../../.." && pwd); B=$(mktemp -d); trap 'rm -rf "$B"' EXIT; K=$B/kennel; (cd "$REPO" && go build -o "$K" .) || { echo "FAIL build"; exit 1; }
H=$HOME
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; }

echo "== 1. no-ai-attribution under a Codex PreToolUse payload (real script, real config)"
p=$(jq -nc --arg cwd "$H/Projects/kennel" '{hook_event_name:"PreToolUse",tool_name:"Bash",cwd:$cwd,session_id:"x",turn_id:"t",tool_input:{command:"git commit -m \"fix it\n\nCo-Authored-By: Claude <noreply@anthropic.com>\""}}')
out=$(printf '%s' "$p" | "$K" hook codex -- bash "$H/.claude/hooks/no-ai-attribution.sh"); rc=$?
echo "$out" | head -c 300; echo; echo "exit $rc"
echo "$out" | jq -e '.hookSpecificOutput.permissionDecision=="deny"' >/dev/null && pass "attribution denied" || fail "attribution not denied"
p2=$(jq -nc --arg cwd "$H/Projects/kennel" '{hook_event_name:"PreToolUse",tool_name:"Bash",cwd:$cwd,tool_input:{command:"git commit -m \"fix it\""}}')
out=$(printf '%s' "$p2" | "$K" hook codex -- bash "$H/.claude/hooks/no-ai-attribution.sh")
[ -z "$out" ] && pass "clean commit passes (empty output)" || fail "clean commit got: $out"

echo "== 2. real ask hook under Codex becomes deny"
p=$(jq -nc --arg cwd "$H/Projects/kennel" '{hook_event_name:"PreToolUse",tool_name:"Bash",cwd:$cwd,tool_input:{command:"npm install left-pad"}}')
raw=$(printf '%s' "$p" | bash "$H/.claude/hooks/new-dependency-ask.sh")
out=$(printf '%s' "$p" | "$K" hook codex -- bash "$H/.claude/hooks/new-dependency-ask.sh")
echo "raw:     $(echo "$raw" | jq -c .hookSpecificOutput 2>/dev/null)"
echo "wrapped: $(echo "$out" | jq -c .hookSpecificOutput 2>/dev/null)"
[ "$(echo "$raw" | jq -r .hookSpecificOutput.permissionDecisionReason)" = "$(echo "$out" | jq -r .hookSpecificOutput.permissionDecisionReason)" ] && \
  [ "$(echo "$out" | jq -r .hookSpecificOutput.permissionDecision)" = deny ] && pass "ask -> deny, same reason" || fail "ask conversion"

echo "== 3. rewrite hook under Codex becomes deny naming the rewrite"
p=$(jq -nc --arg cwd "$H/Projects/kennel" '{hook_event_name:"PreToolUse",tool_name:"Bash",cwd:$cwd,tool_input:{command:"cd /tmp && ls"}}')
out=$(printf '%s' "$p" | "$K" hook codex -- bash "$H/.claude/hooks/no-escaping-cd-guard.sh")
echo "$out" | jq -c .hookSpecificOutput
echo "$out" | jq -e '.hookSpecificOutput.permissionDecision=="deny" and (.hookSpecificOutput.updatedInput==null)' >/dev/null && pass "rewrite -> deny" || fail "rewrite"

echo "== 4. apply_patch over two files with the real admin-skill-pointer (adapter)"
patch=$'*** Begin Patch\n*** Update File: README.md\n@@\n-a\n+b\n*** Update File: admin.toml\n@@\n-a\n+b\n*** End Patch\n'
p=$(jq -nc --arg cwd "$H/Projects/kennel" --arg c "$patch" '{hook_event_name:"PreToolUse",tool_name:"apply_patch",cwd:$cwd,tool_input:{command:$c}}')
out=$(printf '%s' "$p" | "$K" hook codex -- bash "$H/.claude/hooks/admin-skill-pointer.sh")
echo "$out" | jq -c .hookSpecificOutput
echo "$out" | grep -q 'admin.toml' && pass "second file of the patch reached the path hook" || fail "patch split"

echo "== 5. pi: bash payload through the real no-brace-refspec guard"
p=$(jq -nc --arg cwd "$H/Projects/kennel" '{hook_event_name:"PreToolUse",tool_name:"bash",cwd:$cwd,tool_input:{command:"git log @{u}"}}')
out=$(printf '%s' "$p" | "$K" hook pi -- bash "$H/.claude/hooks/no-brace-refspec-guard.sh")
echo "$out" | head -c 200; echo
echo "$out" | jq -e '.decision=="deny"' >/dev/null && pass "pi deny" || fail "pi deny"

echo "== 6. env: CLAUDE_PROJECT_DIR and CLAUDE_HOME from the payload cwd"
for d in "$H/Projects/kennel" "$H/Work"; do
  p=$(jq -nc --arg cwd "$d" '{hook_event_name:"PreToolUse",tool_name:"Bash",cwd:$cwd,tool_input:{command:"x"}}')
  printf '%s' "$p" | "$K" hook codex -- bash -c 'echo "PROJECT=$CLAUDE_PROJECT_DIR HOME_=$CLAUDE_HOME" >&2' 2>&1
done

echo "== 7. sandbox HOME with copies of the real hooks.json and settings.json"
S=$(mktemp -d)
mkdir -p "$S/.claude" "$S/.codex" "$S/.cfg" "$S/.pi/agent/extensions"
cp "$H/.claude/settings.json" "$S/.claude/"; ln -s "$H/.claude/hooks" "$S/.claude/hooks"
cp "$H/.codex/hooks.json" "$S/.codex/"; cp "$H/.pi/agent/extensions/loop-guard.ts" "$S/.pi/agent/extensions/"
printf '[tools.claude]\nbase="~/.claude"\nshared=[]\n[tools.codex]\nbase="~/.codex"\nshared=[]\n' > "$S/.cfg/config.toml"
before=$(cd "$S" && find .codex .pi -type f -exec shasum {} +)
HOME=$S KENNEL_CONFIG_DIR=$S/.cfg "$K" hooks apply
after=$(cd "$S" && find .codex .pi -type f -exec shasum {} +)
[ "$before" = "$after" ] && pass "opt-in off: .codex and .pi byte-identical" || fail "opt-in off changed files"
printf '[hooks]\ncodex=true\npi=true\n' >> "$S/.cfg/config.toml"
HOME=$S KENNEL_CONFIG_DIR=$S/.cfg "$K" hooks apply
s1=$(shasum "$S/.codex/hooks.json")
HOME=$S KENNEL_CONFIG_DIR=$S/.cfg "$K" hooks apply
s2=$(shasum "$S/.codex/hooks.json")
[ "$s1" = "$s2" ] && pass "second apply byte-identical" || fail "second apply changed"
for f in 'herdr-agent-state.sh session' 'github-owner.py'; do
  a=$(jq -c --arg f "$f" '[.hooks[][] | .hooks[] | select(.command|contains($f))]' "$H/.codex/hooks.json")
  b=$(jq -c --arg f "$f" '[.hooks[][] | .hooks[] | select(.command|contains($f))]' "$S/.codex/hooks.json")
  [ "$a" = "$b" ] && pass "foreign entry unchanged: $f" || fail "foreign entry changed: $f"
done
echo "groups per event: $(jq -c '.hooks | map_values(length)' "$S/.codex/hooks.json")"
echo "handlers generated: $(jq '[.hooks[][] | .hooks[] | select(.command|contains(" hook codex -- "))] | length' "$S/.codex/hooks.json")"
echo "raw ~/.claude/hooks handlers left: $(jq '[.hooks[][] | .hooks[] | select((.command|contains("/.claude/hooks/")) and (.command|contains(" hook codex -- ")|not))] | length' "$S/.codex/hooks.json")"
ls "$S/.pi/agent/extensions"
HOME=$S KENNEL_CONFIG_DIR=$S/.cfg "$K" doctor 2>&1 | sed -n '/^Hooks/,/^Routes/p'
rm -rf "$S"

echo "== 8. the generated pi extension, loaded with pi's own jiti, against the real hook list"
NM=$H/.pi/agent/install/current-version; NM=$H/.pi/agent/install/releases/$(cat "$NM")/node_modules
S=$(mktemp -d); mkdir -p "$S/.claude" "$S/.cfg"; cp "$H/.claude/settings.json" "$S/.claude/"; ln -s "$H/.claude/hooks" "$S/.claude/hooks"
printf '[tools.claude]\nbase="~/.claude"\nshared=[]\n[hooks]\npi=true\n' > "$S/.cfg/config.toml"
HOME=$S KENNEL_CONFIG_DIR=$S/.cfg "$K" hooks apply >/dev/null
out=$(PI_NM=$NM node "$(dirname "$0")/pi-harness.mjs" "$S/.pi/agent/extensions/kennel-hooks.ts" 2>&1); echo "$out"
echo "$out" | grep -q '^brace refspec -> {"block":true' && echo "$out" | grep -q '^ls -> undefined' && pass "pi extension blocks a guarded call and passes ls" || fail "pi extension"
out=$(KENNEL_CONFIG_DIR=/nonexistent PI_NM=$NM node "$(dirname "$0")/pi-harness.mjs" "$S/.pi/agent/extensions/kennel-hooks.ts" 2>&1)
echo "$out" | grep -q '^ls -> {"block":true' && pass "pi extension blocks every call when the list fails" || fail "pi fail-closed"
rm -rf "$S"

echo "== 9. the generated pi extension puts pi's session id in the hook payload"
S=$(mktemp -d); mkdir -p "$S/.claude/hooks" "$S/.cfg" "$S/work"
printf '#!/bin/sh\n# kennel-hook: portable\ncat > "%s/payload.json"\n' "$S" > "$S/.claude/hooks/capture.sh"
jq -n --arg c "sh $S/.claude/hooks/capture.sh" '{hooks:{PreToolUse:[{matcher:"Bash",hooks:[{type:"command",command:$c}]}]}}' > "$S/.claude/settings.json"
printf '[tools.claude]\nbase="%s/.claude"\nshared=[]\n[hooks]\npi=true\n' "$S" > "$S/.cfg/config.toml"
HOME=$S KENNEL_CONFIG_DIR=$S/.cfg "$K" hooks apply >/dev/null
HOME=$S KENNEL_CONFIG_DIR=$S/.cfg PI_CWD=$S/work PI_NM=$NM node "$(dirname "$0")/pi-harness.mjs" "$S/.pi/agent/extensions/kennel-hooks.ts" >/dev/null 2>&1
echo "payload: $(jq -c '{session_id, tool_name, cwd}' "$S/payload.json" 2>&1)"
[ "$(jq -r .session_id "$S/payload.json" 2>/dev/null)" = "019a-pi-harness" ] && pass "pi payload carries the session id" || fail "pi payload session id"
rm -rf "$S"
