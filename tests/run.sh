#!/usr/bin/env bash
# Integration tests for muxa. Needs tmux + python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PATH="$ROOT/bin:$PATH"

for skill in muxa-parent muxa-worker; do
  src="$ROOT/skills/$skill/SKILL.md"
  [ -f "$src" ] || { echo "missing $src" >&2; exit 1; }
done
SOCK="muxatest-$$"
export MUXA_TMUX_SOCKET="$SOCK"
export MUXA_ENTER_DELAY=0.05
unset TMUX MUXA_NAME MUXA_PARENT MUXA_ID MUXA_BIN || true

pass=0
fail=0
tmpdir="$(mktemp -d /tmp/muxa-test.XXXXXX)"
alice_out="$tmpdir/alice.out"

cleanup() {
  tmux -L "$SOCK" kill-server 2>/dev/null || true
  rm -rf "$tmpdir"
}
trap cleanup EXIT

ok() { pass=$((pass + 1)); printf 'ok %s %s\n' "$pass" "$1"; }
bad() { fail=$((fail + 1)); printf 'not ok %s %s\n' "$((pass + fail))" "$1"; printf '  %s\n' "$2" >&2; }

assert_contains() {
  local hay="$1" needle="$2" label="$3"
  case "$hay" in
    *"$needle"*) ok "$label" ;;
    *) bad "$label" "expected to find: $needle"$'\n'"got: $hay" ;;
  esac
}

who_status_for() {
  local who="$1" name="$2" s
  s="$(printf '%s\n' "$who" | awk -v n="$name" '$1==n { print substr($0, 105, 8); exit }')"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  printf '%s' "$s"
}

assert_who_status() {
  local who="$1" name="$2" want="$3" label="$4"
  local got
  got="$(who_status_for "$who" "$name")"
  [ "$got" = "$want" ] && ok "$label" || bad "$label" "name=$name want=$want got=$got"
}

muxa_box() {
  local name="$1" pid base
  pid="$(tmux -L "$SOCK" list-sessions -F '#{pid}' | head -1)"
  base="${XDG_RUNTIME_DIR:-/tmp/muxa-${UID:-$(id -u)}}"
  printf '%s/muxa/%s/mail/%s' "$base" "$pid" "$name"
}

place_mail() {
  local box="$1" id="$2" body="$3" extra="${4:-}"
  mkdir -p "$box/tmp" "$box/new" "$box/cur" "$box/done" "$box/dead"
  {
    printf 'From: bob\nTo: alice\nId: %s\nTime: 2026-01-01T00:00:00Z\nFlags: \n' "$id"
    [ -n "$extra" ] && printf '%s\n' "$extra"
    printf '\n%s\n' "$body"
  } >"$box/tmp/$id"
  mv "$box/tmp/$id" "$box/new/$id"
}

tmux -L "$SOCK" new-session -d -s muxa -n alice "exec cat > '$alice_out'"
tmux -L "$SOCK" split-window -h -t muxa:alice "exec sleep 3600"
sleep 0.2

alice_pane="$(tmux -L "$SOCK" list-panes -t muxa:alice -F '#{pane_id} #{pane_current_command}' | awk '$2=="cat" {print $1; exit}')"
bob_pane="$(tmux -L "$SOCK" list-panes -t muxa:alice -F '#{pane_id} #{pane_current_command}' | awk '$2=="sleep" {print $1; exit}')"

[ -n "$alice_pane" ] && [ -n "$bob_pane" ] || {
  echo "failed to create panes" >&2
  tmux -L "$SOCK" list-panes -t muxa:alice -F '#{pane_id} #{pane_current_command}' >&2
  exit 1
}

muxa_as() {
  local pane="$1"
  shift
  TMUX_PANE="$pane" muxa "$@"
}

# Write a composer fixture to the pane tty so capture-pane -e sees it.
# Does not go through the pane process (cat), so alice.out stays clean.
paint_composer() {
  local pane="$1" fixture="$2"
  local tty
  tty="$(tmux -L "$SOCK" display-message -t "$pane" -p '#{pane_tty}')"
  printf '\033[H\033[2J' > "$tty"
  cat "$ROOT/tests/fixtures/composer/$fixture" > "$tty"
  sleep 0.05
}

muxa_runtime() {
  local pid base
  pid="$(tmux -L "$SOCK" list-sessions -F '#{pid}' | head -1)"
  base="${XDG_RUNTIME_DIR:-/tmp/muxa-${UID:-$(id -u)}}"
  printf '%s/muxa/%s' "$base" "$pid"
}

# --- register + who ---
reg_b="$(muxa_as "$bob_pane" register --name bob --kind generic --deliver inject)"
assert_contains "$reg_b" "registered bob" "register bob"

reg_a="$(muxa_as "$alice_pane" register --name alice --kind generic --deliver inject --parent bob)"
assert_contains "$reg_a" "registered alice" "register alice"
assert_contains "$reg_a" "parent=bob" "alice is bob's child"

who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "alice" "who lists alice"
assert_contains "$who" "bob" "who lists bob"

me="$(muxa_as "$bob_pane" whoami)"
assert_contains "$me" "bob" "whoami is bob"

dup="$(muxa_as "$bob_pane" register --name alice --kind generic --deliver inject 2>&1 || true)"
assert_contains "$dup" "already registered" "duplicate name refused"

# --- idle inject ---
sent="$(muxa_as "$bob_pane" send alice 'review src/auth.ts')"
assert_contains "$sent" "delivered bob → alice" "idle send delivers"
sleep 0.3
got="$(cat "$alice_out" 2>/dev/null || true)"
assert_contains "$got" "review src/auth.ts" "alice pane received body"
assert_contains "$got" "[muxa] from=bob" "alice pane received prefix"

# --- oversized inject refused; a single oversize parks in dead/ (E7) ---
muxa_as "$alice_pane" state idle
big="$(python3 -c "print('O' * 9000)")"
oversized="$(muxa_as "$bob_pane" send alice "$big" 2>&1)"
assert_contains "$oversized" "queued bob → alice" "oversized idle send queues"
assert_contains "$oversized" "exceeds inject limit" "oversized idle send warns"
peek_big="$(muxa_as "$bob_pane" peek alice)"
case "$peek_big" in
  *OOOO*) bad "oversized idle mail is not left in new/" "peek still has body: $peek_big" ;;
  *) ok "oversized idle mail is not left in new/" ;;
esac
alice_box="$(muxa_box alice)"
dead_n="$(find "$alice_box/dead" -type f 2>/dev/null | awk 'END { print NR }')"
[ "$dead_n" -ge 1 ] && ok "oversized idle mail parked in dead/" \
  || bad "oversized idle mail parked in dead/" "dead_n=$dead_n box=$alice_box"

muxa_as "$alice_pane" state busy
kick_big="$(muxa_as "$bob_pane" send alice "$big" 2>&1)"
assert_contains "$kick_big" "waiting for idle" "oversized busy send spawns kick_wait"
muxa_as "$alice_pane" state idle
sleep 0.8
peek_big3="$(muxa_as "$bob_pane" peek alice)"
case "$peek_big3" in
  *OOOO*) bad "oversized kick_wait mail is not left in new/" "peek still has body" ;;
  *) ok "oversized kick_wait mail is not left in new/" ;;
esac

# hooks still tolerate large bodies (never inject, so never park)
muxa_as "$alice_pane" register --name alice --kind claude --deliver hook >/dev/null
muxa_as "$alice_pane" state busy
muxa_as "$bob_pane" send alice "$big" >/dev/null
drain_big="$(muxa_as "$alice_pane" hook stop --format claude)"
assert_contains "$drain_big" "OOOO" "hook stop drains oversized body"
# Claimed oversized mail would otherwise sit in cur/ and inflate UNREAD below.
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- busy + hook drain (Claude JSON) ---
muxa_as "$alice_pane" register --name alice --kind claude --deliver hook >/dev/null
muxa_as "$alice_pane" state busy
queued="$(muxa_as "$bob_pane" send --no-reply alice 'queued while busy')"
assert_contains "$queued" "hook will drain" "busy+hook queues"

peek="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$peek" "queued while busy" "peek shows unread"

drain="$(muxa_as "$alice_pane" hook stop --format claude)"
assert_contains "$drain" "additionalContext" "claude stop JSON"
assert_contains "$drain" "queued while busy" "drain includes body"
assert_contains "$drain" "Do not reply" "no-reply flag honored"

peek2="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$peek2" "claimed but unconfirmed" "peek shows claimed mail after drain"
assert_contains "$peek2" "queued while busy" "peek still has body after drain"
who_drain="$(muxa_as "$bob_pane" who)"
who_drain_n="$(printf '%s\n' "$who_drain" | awk '$1=="alice" { print substr($0, 114, 8); exit }')"
who_drain_n="${who_drain_n#"${who_drain_n%%[![:space:]]*}"}"
who_drain_n="${who_drain_n%"${who_drain_n##*[![:space:]]}"}"
[ "$who_drain_n" = "1" ] && ok "who UNREAD counts claimed mail" \
  || bad "who UNREAD counts claimed mail" "got='$who_drain_n'"
python3 - "$(muxa_box alice)/cur" <<'PY'
import os, sys, time
d = sys.argv[1]
now = time.time()
for name in os.listdir(d):
    p = os.path.join(d, name)
    if os.path.isfile(p):
        os.utime(p, (now - 5, now - 5))
PY
empty_after="$(muxa_as "$alice_pane" hook stop --format claude)"
[ -z "$empty_after" ] && ok "next stop sweeps claimed mail silently" \
  || bad "next stop sweeps claimed mail silently" "got: $empty_after"
peek2b="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$peek2b" "(empty)" "mailbox empty after sweep"

# --- Cursor JSON ---
muxa_as "$alice_pane" state busy
muxa_as "$bob_pane" send alice 'cursor followup' >/dev/null
cjson="$(muxa_as "$alice_pane" hook stop --format cursor)"
assert_contains "$cjson" "followup_message" "cursor stop JSON"

# --- Pi JSON ---
muxa_as "$alice_pane" state busy
muxa_as "$bob_pane" send alice 'pi continue' >/dev/null
pjson="$(muxa_as "$alice_pane" hook stop --format pi)"
assert_contains "$pjson" '"continue": true' "pi stop JSON"

# --- empty stop is silent (prior cursor/pi drains must not leak) ---
python3 - "$(muxa_box alice)/cur" <<'PY'
import os, sys, time
d = sys.argv[1]
now = time.time()
for name in os.listdir(d):
    p = os.path.join(d, name)
    if os.path.isfile(p):
        os.utime(p, (now - 5, now - 5))
PY
muxa_as "$alice_pane" hook stop --format claude >/dev/null
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true
muxa_as "$alice_pane" state busy
empty="$(muxa_as "$alice_pane" hook stop --format claude)"
[ -z "$empty" ] && ok "empty stop prints nothing" || bad "empty stop prints nothing" "got: $empty"
# Fail-closed: fresh cursor/pi drains then immediate empty stop (Linux-fast path).
muxa_as "$alice_pane" state busy
muxa_as "$bob_pane" send alice 'cursor followup leak' >/dev/null
muxa_as "$alice_pane" hook stop --format cursor >/dev/null
muxa_as "$alice_pane" state busy
muxa_as "$bob_pane" send alice 'pi continue leak' >/dev/null
muxa_as "$alice_pane" hook stop --format pi >/dev/null
empty_leak="$(muxa_as "$alice_pane" hook stop --format claude)"
[ -z "$empty_leak" ] && ok "empty stop silent after prior cursor/pi drains" \
  || bad "empty stop silent after prior cursor/pi drains" "got: $empty_leak"
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- concurrent kick_wait while busy (inject) ---
muxa_as "$alice_pane" register --name alice --kind generic --deliver inject >/dev/null
muxa_as "$alice_pane" state busy
: >"$alice_out"
muxa_as "$bob_pane" send --no-reply alice 'wait-msg-alpha' &
muxa_as "$bob_pane" send --no-reply alice 'wait-msg-beta' &
wait
sleep 0.2
muxa_as "$alice_pane" state idle
sleep 1.0
got="$(cat "$alice_out" 2>/dev/null || true)"
assert_contains "$got" "wait-msg-alpha" "concurrent wait delivers alpha"
assert_contains "$got" "wait-msg-beta" "concurrent wait delivers beta"
assert_contains "$got" "[muxa] 2 messages" "concurrent wait batches into one inject"

# --- copy-mode defers (the reproduced incident) ---
muxa_as "$alice_pane" register --name alice --kind generic --deliver inject >/dev/null
muxa_as "$alice_pane" state idle
: >"$alice_out"
tmux -L "$SOCK" copy-mode -t "$alice_pane"
copy_sent="$(muxa_as "$bob_pane" send alice 'COPY_MODE_BODY' 2>&1)"
assert_contains "$copy_sent" "queued bob → alice" "copy-mode send queues"
sleep 0.2
got="$(cat "$alice_out" 2>/dev/null || true)"
case "$got" in
  *COPY_MODE_BODY*) bad "copy-mode does not deliver to the app" "got: $got" ;;
  *) ok "copy-mode does not deliver to the app" ;;
esac
peek_copy="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$peek_copy" "COPY_MODE_BODY" "peek still shows copy-mode mail"
tmux -L "$SOCK" send-keys -t "$alice_pane" -X cancel 2>/dev/null || true
sleep 0.1
# Stash the deferred mail so the next inject is a clean paste (claim_new would
# otherwise coalesce it). Ghost-flush is a tmux paste-buffer property.
copy_box="$(muxa_box alice)"
stash="$(mktemp -d "$tmpdir/stash.XXXXXX")"
find "$copy_box/new" -type f -exec mv {} "$stash/" \; 2>/dev/null || true
muxa_as "$alice_pane" state idle
later="$(muxa_as "$bob_pane" send alice 'AFTER_CANCEL_MSG' 2>&1)"
assert_contains "$later" "delivered" "send after leaving copy-mode delivers"
sleep 0.3
got="$(cat "$alice_out" 2>/dev/null || true)"
assert_contains "$got" "AFTER_CANCEL_MSG" "pane received the later message"
case "$got" in
  *COPY_MODE_BODYAFTER_CANCEL_MSG*) bad "copy-mode ghost flush is prevented" "got concatenated: $got" ;;
  *) ok "copy-mode ghost flush is prevented" ;;
esac
find "$stash" -type f -exec mv {} "$copy_box/new/" \; 2>/dev/null || true
# original body still queued; deliver now that copy-mode is off
del_copy="$(muxa_as "$bob_pane" deliver alice 2>&1)"
assert_contains "$del_copy" "injected into alice" "deliver after cancel injects"
sleep 0.2
got="$(cat "$alice_out" 2>/dev/null || true)"
assert_contains "$got" "COPY_MODE_BODY" "deliver lands the deferred copy-mode body"

# --- dead pane: send queues, mail stays in new/, peek finds it ---
tmux -L "$SOCK" set-window-option -g remain-on-exit off 2>/dev/null || true
tmux -L "$SOCK" new-window -t muxa -n deadpane "exec sleep 3600"
sleep 0.2
dead_pane="$(tmux -L "$SOCK" list-panes -t muxa:deadpane -F '#{pane_id}' | head -1)"
muxa_as "$dead_pane" register --name deadagent --kind generic --deliver inject --parent bob >/dev/null
tmux -L "$SOCK" set-window-option -t muxa:deadpane remain-on-exit on
dead_pid="$(tmux -L "$SOCK" display-message -t "$dead_pane" -p '#{pane_pid}')"
kill "$dead_pid" 2>/dev/null || true
sleep 0.3
dead_flag="$(tmux -L "$SOCK" display-message -t "$dead_pane" -p '#{pane_dead}' 2>/dev/null || echo 1)"
[ "$dead_flag" = "1" ] && ok "dead pane fixture is pane_dead=1" \
  || bad "dead pane fixture is pane_dead=1" "pane_dead=$dead_flag"
dead_sent="$(muxa_as "$bob_pane" send deadagent 'DEAD_PANE_BODY' 2>&1)"
assert_contains "$dead_sent" "queued bob → deadagent" "dead pane send queues"
peek_dead="$(muxa_as "$bob_pane" peek deadagent)"
assert_contains "$peek_dead" "DEAD_PANE_BODY" "peek finds mail for a dead pane"
case "$peek_dead" in
  *claimed\ but\ unconfirmed*) bad "dead pane mail is not stuck in cur/" "$peek_dead" ;;
  *) ok "dead pane mail is not stuck in cur/" ;;
esac

# --- alternate screen, generic pane ---
tmux -L "$SOCK" new-window -t muxa -n altpane "printf '\\033[?1049h'; exec cat"
sleep 0.3
alt_pane="$(tmux -L "$SOCK" list-panes -t muxa:altpane -F '#{pane_id}' | head -1)"
muxa_as "$alt_pane" register --name altagent --kind generic --deliver inject --parent bob >/dev/null
alt_on="$(tmux -L "$SOCK" display-message -t "$alt_pane" -p '#{alternate_on}')"
if [ "$alt_on" = "1" ]; then
  alt_sent="$(muxa_as "$bob_pane" send altagent 'ALT_SCREEN_BODY' 2>&1)"
  assert_contains "$alt_sent" "queued bob → altagent" "generic alt-screen send queues"
  peek_alt="$(muxa_as "$bob_pane" peek altagent)"
  assert_contains "$peek_alt" "ALT_SCREEN_BODY" "alt-screen mail stays in mailbox"
else
  ok "generic alt-screen send queues (skip: alternate_on=$alt_on)"
  ok "alt-screen mail stays in mailbox (skip)"
fi

# --- peek shows cur/ ---
box="$(muxa_box alice)"
mkdir -p "$box/cur"
{
  printf 'From: bob\nTo: alice\nId: claimed-1\nTime: 2026-01-01T00:00:00Z\nFlags: \n\nSTUCK_CLAIMED\n'
} >"$box/cur/claimed-1"
peek_cur="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$peek_cur" "claimed but unconfirmed" "peek shows cur/ section"
assert_contains "$peek_cur" "age=" "peek shows claimed age"
assert_contains "$peek_cur" "STUCK_CLAIMED" "peek shows claimed body"
rm -f "$box/cur/claimed-1"

# --- deliver re-injects unconfirmed cur/ when new/ is empty (S4) ---
muxa_as "$alice_pane" register --name alice --kind generic --deliver inject >/dev/null
muxa_as "$alice_pane" state idle
: >"$alice_out"
box="$(muxa_box alice)"
find "$box/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$box/cur" -type f -exec rm -f {} + 2>/dev/null || true
{
  printf 'From: bob\nTo: alice\nId: cur-redeliver\nTime: 2026-01-01T00:00:00Z\nFlags: \n\nCUR_REDELIVER_BODY\n'
} >"$box/cur/cur-redeliver"
del_cur="$(muxa_as "$bob_pane" deliver alice 2>&1)"
assert_contains "$del_cur" "injected into alice" "deliver re-injects from cur/"
sleep 0.3
got="$(cat "$alice_out" 2>/dev/null || true)"
assert_contains "$got" "CUR_REDELIVER_BODY" "deliver from cur/ lands the body"
find "$box/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$box/cur" -type f -exec rm -f {} + 2>/dev/null || true
find "$box/done" -type f -exec rm -f {} + 2>/dev/null || true

# --- deliver cur/ fallback must not touch hook mail awaiting Stop ---
muxa_as "$alice_pane" register --name alice --kind claude --deliver hook >/dev/null
tmux -L "$SOCK" set-option -p -t "$alice_pane" @muxa_hook_ok 1 2>/dev/null || true
muxa_as "$alice_pane" state busy
box="$(muxa_box alice)"
find "$box/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$box/cur" -type f -exec rm -f {} + 2>/dev/null || true
{
  printf 'From: bob\nTo: alice\nId: hook-cur-wait\nTime: 2026-01-01T00:00:00Z\nFlags: \n\nHOOK_CUR_AWAIT_STOP\n'
} >"$box/cur/hook-cur-wait"
del_hook_cur="$(muxa_as "$bob_pane" deliver alice 2>&1)"
assert_contains "$del_hook_cur" "no mail" "deliver skips hook cur/ awaiting Stop"
[ -f "$box/cur/hook-cur-wait" ] && ok "deliver leaves hook mail in cur/" \
  || bad "deliver leaves hook mail in cur/" "$(ls -la "$box/cur" "$box/new" 2>&1)"
hook_new_n="$(find "$box/new" -type f 2>/dev/null | awk 'END { print NR }')"
[ "$hook_new_n" -eq 0 ] && ok "deliver does not resurrect hook mail to new/" \
  || bad "deliver does not resurrect hook mail to new/" "new=$hook_new_n"
find "$box/cur" -type f -exec rm -f {} + 2>/dev/null || true
muxa_as "$alice_pane" state idle

# --- deliver cur/ inject fallback keeps claim on inject failure ---
muxa_as "$alice_pane" register --name alice --kind generic --deliver inject >/dev/null
muxa_as "$alice_pane" state idle
box="$(muxa_box alice)"
find "$box/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$box/cur" -type f -exec rm -f {} + 2>/dev/null || true
{
  printf 'From: bob\nTo: alice\nId: inject-cur-wait\nTime: 2026-01-01T00:00:00Z\nFlags: \n\nINJECT_CUR_KEEP\n'
} >"$box/cur/inject-cur-wait"
tmux -L "$SOCK" copy-mode -t "$alice_pane"
set +e
del_inj_cur="$(muxa_as "$bob_pane" deliver alice 2>&1)"
del_inj_rc=$?
set -e
[ "$del_inj_rc" -ne 0 ] && ok "deliver inject cur/ defers when not ready" \
  || bad "deliver inject cur/ defers when not ready" "exit=$del_inj_rc out=$del_inj_cur"
[ -f "$box/cur/inject-cur-wait" ] && ok "failed deliver inject cur/ leaves mail in cur/" \
  || bad "failed deliver inject cur/ leaves mail in cur/" "$(ls -la "$box/cur" "$box/new" 2>&1)"
inj_new_n="$(find "$box/new" -type f 2>/dev/null | awk 'END { print NR }')"
[ "$inj_new_n" -eq 0 ] && ok "failed deliver inject cur/ does not unclaim to new/" \
  || bad "failed deliver inject cur/ does not unclaim to new/" "new=$inj_new_n"
tmux -L "$SOCK" send-keys -t "$alice_pane" -X cancel 2>/dev/null || true
find "$box/cur" -type f -exec rm -f {} + 2>/dev/null || true
find "$box/new" -type f -exec rm -f {} + 2>/dev/null || true

# --- peek runs reaper on stale cur/ ---
muxa_as "$alice_pane" register --name alice --kind claude --deliver hook >/dev/null
box="$(muxa_box alice)"
{
  printf 'From: bob\nTo: alice\nId: peek-reap\nTime: 2026-01-01T00:00:00Z\nFlags: \n\nPEEK_REAP\n'
} >"$box/cur/peek-reap"
touch -t 202001010000 "$box/cur/peek-reap"
muxa_as "$bob_pane" peek alice >/dev/null
[ -f "$box/new/peek-reap" ] && ok "peek runs reaper on stale cur/" \
  || bad "peek runs reaper on stale cur/" "$(ls -la "$box/cur" "$box/new" 2>&1)"
assert_contains "$(cat "$box/new/peek-reap")" "Redelivered: 1" "peek reaper writes Redelivered: 1"
find "$box/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$box/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- reaper redelivers stale cur/ and resets mtime (E24) ---
muxa_as "$alice_pane" register --name alice --kind claude --deliver hook >/dev/null
muxa_as "$alice_pane" state busy
box="$(muxa_box alice)"
mkdir -p "$box/cur" "$box/new" "$box/dead"
{
  printf 'From: bob\nTo: alice\nId: stale-1\nTime: 2026-01-01T00:00:00Z\nFlags: \n\nREAP_ME\n'
} >"$box/cur/stale-1"
touch -t 202001010000 "$box/cur/stale-1"
muxa_as "$bob_pane" send alice 'reaper-trigger' >/dev/null
[ -f "$box/new/stale-1" ] && ok "reaper moves stale cur/ to new/" \
  || bad "reaper moves stale cur/ to new/" "missing $box/new/stale-1"
assert_contains "$(cat "$box/new/stale-1")" "Redelivered: 1" "reaper writes Redelivered: 1"
age_s="$(python3 - "$box/new/stale-1" <<'PY'
import os, sys, time
print(int(time.time() - os.stat(sys.argv[1]).st_mtime))
PY
)"
[ "$age_s" -lt 30 ] && ok "reaper rewrite resets mtime" \
  || bad "reaper rewrite resets mtime" "age=${age_s}s"
find "$box/new" -type f -exec rm -f {} + 2>/dev/null || true

# --- reaper parks after N ---
{
  printf 'From: bob\nTo: alice\nId: stale-max\nTime: 2026-01-01T00:00:00Z\nFlags: \nRedelivered: 3\n\nPARK_ME\n'
} >"$box/cur/stale-max"
touch -t 202001010000 "$box/cur/stale-max"
park_err="$(muxa_as "$bob_pane" send alice 'park-trigger' 2>&1)"
assert_contains "$park_err" "undeliverable" "reaper warns when parking"
[ -f "$box/dead/stale-max" ] && ok "reaper parks after max attempts" \
  || bad "reaper parks after max attempts" "$(ls -la "$box/cur" "$box/dead" 2>&1)"
find "$box/new" -type f -exec rm -f {} + 2>/dev/null || true

# --- kick_wait does not poison state (E21) ---
muxa_as "$alice_pane" register --name alice --kind generic --deliver inject >/dev/null
muxa_as "$alice_pane" state busy
tmux -L "$SOCK" copy-mode -t "$alice_pane"
muxa_as "$bob_pane" send alice 'POISON_CHECK' >/dev/null
muxa_as "$alice_pane" state idle
sleep 0.8
st="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_state}')"
[ "$st" = "idle" ] && ok "kick_wait restores idle after deferral" \
  || bad "kick_wait restores idle after deferral" "state=$st"
tmux -L "$SOCK" send-keys -t "$alice_pane" -X cancel 2>/dev/null || true
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- batch cap ---
muxa_as "$alice_pane" register --name alice --kind claude --deliver hook >/dev/null
muxa_as "$alice_pane" state busy
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
i=1
while [ "$i" -le 20 ]; do
  muxa_as "$bob_pane" send alice "batch-msg-$i" >/dev/null
  i=$((i + 1))
done
new_n="$(find "$(muxa_box alice)/new" -type f | awk 'END { print NR }')"
[ "$new_n" -eq 20 ] && ok "queued 20 messages before claim" \
  || bad "queued 20 messages before claim" "new_n=$new_n"
MUXA_BATCH_MAX=8 muxa_as "$alice_pane" hook stop --format claude >/dev/null
left_n="$(find "$(muxa_box alice)/new" -type f | awk 'END { print NR }')"
[ "$left_n" -eq 12 ] && ok "claim caps at MUXA_BATCH_MAX" \
  || bad "claim caps at MUXA_BATCH_MAX" "left_in_new=$left_n"
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/done" -type f -exec rm -f {} + 2>/dev/null || true

# --- ordering: lex-unsafe ids still deliver in mtime/send order (E8) ---
muxa_as "$alice_pane" register --name alice --kind generic --deliver inject >/dev/null
muxa_as "$alice_pane" state idle
: >"$alice_out"
obox="$(muxa_box alice)"
find "$obox/new" -type f -exec rm -f {} + 2>/dev/null || true
epoch="$(date +%s)"
# Same epoch, lex order would be -40 < -400 < -5 if unpadded; send order is 5, 40, 400.
place_mail "$obox" "${epoch}-1-5" "ORDER_FIRST"
sleep 0.05
place_mail "$obox" "${epoch}-1-40" "ORDER_SECOND"
sleep 0.05
place_mail "$obox" "${epoch}-1-400" "ORDER_THIRD"
muxa_as "$bob_pane" deliver alice >/dev/null
sleep 0.3
got="$(cat "$alice_out" 2>/dev/null || true)"
python3 - "$got" <<'PY' && ok "coalesced payload preserves send order" || bad "coalesced payload preserves send order" "got: $got"
import sys
text = sys.argv[1]
i1, i2, i3 = text.find("ORDER_FIRST"), text.find("ORDER_SECOND"), text.find("ORDER_THIRD")
sys.exit(0 if -1 not in (i1, i2, i3) and i1 < i2 < i3 else 1)
PY

# --- idle hook inject after Stop (CLI parent sitting idle) ---
muxa_as "$alice_pane" register --name alice --kind claude --deliver hook --parent bob >/dev/null
tmux -L "$SOCK" set-option -p -t "$alice_pane" -u @muxa_hook_ok 2>/dev/null || true
muxa_as "$alice_pane" state idle
: >"$alice_out"
# hook_ok unset: skip composer so splash/typed input cannot deadlock spawn.
paint_composer "$alice_pane" cursor-typed.ansi
boot="$(muxa_as "$bob_pane" send alice 'BOOTSTRAP_BRIEF')"
assert_contains "$boot" "delivered bob → alice" "idle hook pane injects before hook_ok"
sleep 0.3
got="$(cat "$alice_out" 2>/dev/null || true)"
assert_contains "$got" "BOOTSTRAP_BRIEF" "bootstrap brief reached the pane"

# --- first brief: alt-screen / blank capture queues (not stuck in cur/) ---
tmux -L "$SOCK" new-window -t muxa -n splash "printf '\\033[?1049h'; exec cat > '$tmpdir/splash.out'"
sleep 0.3
splash_pane="$(tmux -L "$SOCK" list-panes -t muxa:splash -F '#{pane_id}' | head -1)"
splash_out="$tmpdir/splash.out"
muxa_as "$splash_pane" register --name splash --kind cursor --deliver hook --parent bob >/dev/null
tmux -L "$SOCK" set-option -p -t "$splash_pane" -u @muxa_hook_ok 2>/dev/null || true
muxa_as "$splash_pane" state idle
: >"$splash_out"
splash_on="$(tmux -L "$SOCK" display-message -t "$splash_pane" -p '#{alternate_on}')"
if [ "$splash_on" = "1" ]; then
  splash_sent="$(muxa_as "$bob_pane" send splash 'SPLASH_FIRST_BRIEF' 2>&1)"
  assert_contains "$splash_sent" "queued bob → splash" "first brief on alt-screen queues"
  assert_contains "$splash_sent" "waiting for idle" "first brief on alt-screen spawns kick_wait"
  sleep 0.2
  got="$(cat "$splash_out" 2>/dev/null || true)"
  case "$got" in
    *SPLASH_FIRST_BRIEF*) bad "first brief on alt-screen does not paste" "got: $got" ;;
    *) ok "first brief on alt-screen does not paste" ;;
  esac
  peek_splash="$(muxa_as "$bob_pane" peek splash)"
  assert_contains "$peek_splash" "SPLASH_FIRST_BRIEF" "peek shows queued first brief"
  case "$peek_splash" in
    *claimed\ but\ unconfirmed*) bad "first brief on alt-screen is not stuck in cur/" "$peek_splash" ;;
    *) ok "first brief on alt-screen is not stuck in cur/" ;;
  esac
  splash_new="$(find "$(muxa_box splash)/new" -type f 2>/dev/null | awk 'END { print NR }')"
  splash_cur="$(find "$(muxa_box splash)/cur" -type f 2>/dev/null | awk 'END { print NR }')"
  [ "$splash_new" -ge 1 ] && [ "$splash_cur" -eq 0 ] && ok "first brief on alt-screen left mail in new/" \
    || bad "first brief on alt-screen left mail in new/" "new=$splash_new cur=$splash_cur"
  paint_composer "$splash_pane" cursor-idle.ansi
  sleep 1.5
  got="$(cat "$splash_out" 2>/dev/null || true)"
  assert_contains "$got" "SPLASH_FIRST_BRIEF" "kick_wait delivers first brief on drawn alt-screen"
  splash_pidfile="$(muxa_runtime)/kick-wait-${splash_pane}.pid"
  if [ -f "$splash_pidfile" ] && kill -0 "$(cat "$splash_pidfile" 2>/dev/null)" 2>/dev/null; then
    kill "$(cat "$splash_pidfile")" 2>/dev/null || true
    rm -f "$splash_pidfile"
  fi
  find "$(muxa_box splash)/new" -type f -exec rm -f {} + 2>/dev/null || true
  find "$(muxa_box splash)/cur" -type f -exec rm -f {} + 2>/dev/null || true
else
  ok "first brief on alt-screen queues (skip: alternate_on=$splash_on)"
  ok "first brief on alt-screen spawns kick_wait (skip)"
  ok "first brief on alt-screen does not paste (skip)"
  ok "peek shows queued first brief (skip)"
  ok "first brief on alt-screen is not stuck in cur/ (skip)"
  ok "first brief on alt-screen left mail in new/ (skip)"
  ok "kick_wait delivers first brief on drawn alt-screen (skip)"
fi
tmux -L "$SOCK" kill-window -t "muxa:splash" 2>/dev/null || true

# --- first brief: kick_wait survives boot busy (nested S7) ---
nested_out="$tmpdir/nested.out"
tmux -L "$SOCK" split-window -h -t "$bob_pane" "exec cat > '$nested_out'"
sleep 0.2
nested_pane="$(tmux -L "$SOCK" list-panes -t muxa:alice -F '#{pane_id} #{pane_current_command}' \
  | awk -v skip="$alice_pane" '$2=="cat" && $1!=skip { print $1; exit }')"
[ -n "$nested_pane" ] || { bad "nested first-brief pane created" "missing pane"; nested_pane="$alice_pane"; }
muxa_as "$nested_pane" register --name nested --kind claude --deliver hook --parent bob >/dev/null
tmux -L "$SOCK" set-option -p -t "$nested_pane" -u @muxa_hook_ok 2>/dev/null || true
muxa_as "$nested_pane" state idle
: >"$nested_out"
nested_sent="$(muxa_as "$bob_pane" send nested 'NESTED_FIRST_BRIEF' 2>&1)"
assert_contains "$nested_sent" "queued bob → nested" "nested first brief queues on blank capture"
assert_contains "$nested_sent" "waiting for idle" "nested first brief spawns kick_wait"
muxa_as "$nested_pane" state busy
sleep 0.3
nested_pidfile="$(muxa_runtime)/kick-wait-${nested_pane}.pid"
if [ -f "$nested_pidfile" ] && kill -0 "$(cat "$nested_pidfile" 2>/dev/null)" 2>/dev/null; then
  ok "first brief kick_wait survives boot busy"
else
  bad "first brief kick_wait survives boot busy" "pidfile=$nested_pidfile"
fi
nested_new="$(find "$(muxa_box nested)/new" -type f 2>/dev/null | awk 'END { print NR }')"
[ "${nested_new:-0}" -ge 1 ] && ok "first brief mail stays in new/ through boot busy" \
  || bad "first brief mail stays in new/ through boot busy" "new=$nested_new"
muxa_as "$nested_pane" state idle
paint_composer "$nested_pane" claude-idle.ansi
sleep 1.5
got="$(cat "$nested_out" 2>/dev/null || true)"
assert_contains "$got" "NESTED_FIRST_BRIEF" "kick_wait delivers nested first brief after boot busy"
if [ -f "$nested_pidfile" ] && kill -0 "$(cat "$nested_pidfile" 2>/dev/null)" 2>/dev/null; then
  kill "$(cat "$nested_pidfile")" 2>/dev/null || true
  rm -f "$nested_pidfile"
fi
find "$(muxa_box nested)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box nested)/cur" -type f -exec rm -f {} + 2>/dev/null || true

empty_stop="$(muxa_as "$alice_pane" hook stop --format claude)"
[ -z "$empty_stop" ] && ok "first hook stop with empty mailbox is silent" \
  || bad "first hook stop with empty mailbox is silent" "got: $empty_stop"
hook_ok="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_hook_ok}')"
[ "$hook_ok" = "1" ] && ok "@muxa_hook_ok set on first hook stop" \
  || bad "@muxa_hook_ok set on first hook stop" "got: $hook_ok"
# Bootstrap inject left a young cur/ claim; sweep it so UNREAD below is
# only the idle inject (Stop skips claims younger than 1s).
python3 - "$(muxa_box alice)/cur" <<'PY'
import os, sys, time
d = sys.argv[1]
if not os.path.isdir(d):
    raise SystemExit(0)
now = time.time()
for name in os.listdir(d):
    p = os.path.join(d, name)
    if os.path.isfile(p):
        os.utime(p, (now - 5, now - 5))
PY
muxa_as "$alice_pane" hook stop --format claude >/dev/null
muxa_as "$alice_pane" state idle
: >"$alice_out"
paint_composer "$alice_pane" claude-idle.ansi
idle_inj="$(muxa_as "$bob_pane" send alice 'HOOK_IDLE_INJECT')"
assert_contains "$idle_inj" "delivered bob → alice" "idle hook pane injects after hook_ok"
sleep 0.3
got="$(cat "$alice_out" 2>/dev/null || true)"
assert_contains "$got" "HOOK_IDLE_INJECT" "idle hook pane is pasted after hook_ok"
unread="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_unread}')"
[ "$unread" = "1" ] && ok "@muxa_unread=1 after idle hook inject (cur/ until Stop)" \
  || bad "@muxa_unread=1 after idle hook inject (cur/ until Stop)" "got: $unread"
who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "UNREAD" "who header has UNREAD"
# UNREAD column is 8 chars starting at 114 (after STATUS)
who_unread="$(printf '%s\n' "$who" | awk '$1=="alice" { print substr($0, 114, 8); exit }')"
who_unread="${who_unread#"${who_unread%%[![:space:]]*}"}"
who_unread="${who_unread%"${who_unread##*[![:space:]]}"}"
[ "$who_unread" = "1" ] && ok "who shows UNREAD=1" \
  || bad "who shows UNREAD=1" "got='$who_unread' line=$(printf '%s\n' "$who" | awk '$1=="alice"')"
python3 - "$(muxa_box alice)/cur" <<'PY'
import os, sys, time
d = sys.argv[1]
now = time.time()
for name in os.listdir(d):
    p = os.path.join(d, name)
    if os.path.isfile(p):
        os.utime(p, (now - 5, now - 5))
PY
muxa_as "$alice_pane" hook stop --format claude >/dev/null
unread3="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_unread}')"
[ -z "$unread3" ] && ok "@muxa_unread cleared after sweep" \
  || bad "@muxa_unread cleared after sweep" "got: $unread3"

# --- hook_ok + idle + non-empty composer: no paste, queue + kick_wait ---
muxa_as "$alice_pane" state idle
: >"$alice_out"
paint_composer "$alice_pane" cursor-typed.ansi
typed_sent="$(muxa_as "$bob_pane" send alice 'HOOK_TYPED_COMPOSER' 2>&1)"
assert_contains "$typed_sent" "queued bob → alice" "typed composer hook send queues"
assert_contains "$typed_sent" "waiting for idle" "typed composer hook send spawns kick_wait"
assert_contains "$typed_sent" "not ready to receive" "typed composer hook send names the verdict"
sleep 0.2
got="$(cat "$alice_out" 2>/dev/null || true)"
case "$got" in
  *HOOK_TYPED_COMPOSER*) bad "typed composer hook send does not paste" "got: $got" ;;
  *) ok "typed composer hook send does not paste" ;;
esac
peek_typed="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$peek_typed" "HOOK_TYPED_COMPOSER" "peek still shows typed-composer mail"
case "$peek_typed" in
  *claimed\ but\ unconfirmed*) bad "typed composer hook send unclaims (not stuck in cur/)" "$peek_typed" ;;
  *) ok "typed composer hook send unclaims (not stuck in cur/)" ;;
esac
typed_new="$(find "$(muxa_box alice)/new" -type f 2>/dev/null | awk 'END { print NR }')"
typed_cur="$(find "$(muxa_box alice)/cur" -type f 2>/dev/null | awk 'END { print NR }')"
[ "$typed_new" -ge 1 ] && [ "$typed_cur" -eq 0 ] && ok "typed composer hook send left mail in new/" \
  || bad "typed composer hook send left mail in new/" "new=$typed_new cur=$typed_cur"
typed_pidfile="$(muxa_runtime)/kick-wait-${alice_pane}.pid"
if [ -f "$typed_pidfile" ] && kill -0 "$(cat "$typed_pidfile" 2>/dev/null)" 2>/dev/null; then
  ok "typed composer hook send left a live kick_wait"
else
  bad "typed composer hook send left a live kick_wait" "pidfile=$typed_pidfile"
fi
# Composer empty: waiter should paste. Copy-mode inject failure unclaim is
# covered separately below.
: >"$alice_out"
paint_composer "$alice_pane" claude-idle.ansi
sleep 1.5
got="$(cat "$alice_out" 2>/dev/null || true)"
assert_contains "$got" "HOOK_TYPED_COMPOSER" "kick_wait pastes once composer is empty"
if [ -f "$typed_pidfile" ] && kill -0 "$(cat "$typed_pidfile" 2>/dev/null)" 2>/dev/null; then
  kill "$(cat "$typed_pidfile")" 2>/dev/null || true
  rm -f "$typed_pidfile"
fi
python3 - "$(muxa_box alice)/cur" <<'PY'
import os, sys, time
d = sys.argv[1]
if not os.path.isdir(d):
    raise SystemExit(0)
now = time.time()
for name in os.listdir(d):
    p = os.path.join(d, name)
    if os.path.isfile(p):
        os.utime(p, (now - 5, now - 5))
PY
muxa_as "$alice_pane" hook stop --format claude >/dev/null
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- kick_wait outlives MUXA_KICK_WAIT_MAX and is observable (E27) ---
kw_pidfile="$(muxa_runtime)/kick-wait-${alice_pane}.pid"
kw_log="$(muxa_runtime)/kick-wait-${alice_pane}.log"
rm -f "$kw_log"
muxa_as "$alice_pane" state idle
: >"$alice_out"
paint_composer "$alice_pane" cursor-typed.ansi
long_sent="$(TMUX_PANE="$bob_pane" MUXA_KICK_WAIT_MAX=2 MUXA_KICK_WAIT_DEADLINE=60 \
  muxa send alice 'KICK_WAIT_OUTLIVES_MAX' 2>&1)"
assert_contains "$long_sent" "waiter pid=" "send names the kick_wait waiter pid"
sleep 3   # 6 polls, three times the MUXA_KICK_WAIT_MAX=2 that used to end it
if [ -f "$kw_pidfile" ] && kill -0 "$(cat "$kw_pidfile" 2>/dev/null)" 2>/dev/null; then
  ok "kick_wait outlives MUXA_KICK_WAIT_MAX while mail is queued"
else
  bad "kick_wait outlives MUXA_KICK_WAIT_MAX while mail is queued" "pidfile=$kw_pidfile"
fi
kw_state="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_kick_wait}')"
case "$kw_state" in
  waiting*) ok "live waiter is visible in @muxa_kick_wait" ;;
  *) bad "live waiter is visible in @muxa_kick_wait" "got: $kw_state" ;;
esac
who_wait="$(muxa_as "$bob_pane" who)"
assert_contains "$who_wait" "WAIT" "who header has WAIT"
assert_contains "$who_wait" "waiting" "who shows the live waiter"
assert_contains "$(cat "$kw_log" 2>/dev/null || true)" "still not ready after" \
  "kick_wait logs progress instead of discarding it"
: >"$alice_out"
paint_composer "$alice_pane" claude-idle.ansi
sleep 1.5
got="$(cat "$alice_out" 2>/dev/null || true)"
assert_contains "$got" "KICK_WAIT_OUTLIVES_MAX" "long-lived kick_wait delivers once composer clears"
kw_state="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_kick_wait}')"
[ -z "$kw_state" ] && ok "waiter clears @muxa_kick_wait after delivering" \
  || bad "waiter clears @muxa_kick_wait after delivering" "got: $kw_state"
assert_contains "$(cat "$kw_log" 2>/dev/null || true)" "delivered to alice" \
  "kick_wait logs the delivery"

# --- the bounded deadline is reported, not swallowed (E27) ---
rm -f "$kw_log"
muxa_as "$alice_pane" state idle
: >"$alice_out"
paint_composer "$alice_pane" cursor-typed.ansi
TMUX_PANE="$bob_pane" MUXA_KICK_WAIT_MAX=2 MUXA_KICK_WAIT_DEADLINE=1 \
  muxa send alice 'KICK_WAIT_DEADLINE' >/dev/null 2>&1
sleep 2.5
kw_state="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_kick_wait}')"
case "$kw_state" in
  expired*) ok "expired waiter is visible in @muxa_kick_wait" ;;
  *) bad "expired waiter is visible in @muxa_kick_wait" "got: $kw_state" ;;
esac
assert_contains "$(cat "$kw_log" 2>/dev/null || true)" "gave up after 1s" \
  "kick_wait deadline is logged"
tmux -L "$SOCK" set-option -p -t "$alice_pane" -u @muxa_kick_wait 2>/dev/null || true
rm -f "$kw_pidfile" "$kw_log"
paint_composer "$alice_pane" claude-idle.ansi
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- busy hook pane queues for Stop ---
muxa_as "$alice_pane" state busy
: >"$alice_out"
qbusy="$(muxa_as "$bob_pane" send alice 'HOOK_BUSY_QUEUE')"
assert_contains "$qbusy" "queued bob → alice id=" "busy hook pane queues after hook_ok"
assert_contains "$qbusy" "hook will drain" "busy hook queue reason"
sleep 0.2
got="$(cat "$alice_out" 2>/dev/null || true)"
case "$got" in
  *HOOK_BUSY_QUEUE*) bad "busy hook pane is not pasted" "got: $got" ;;
  *) ok "busy hook pane is not pasted" ;;
esac
drain_busy="$(muxa_as "$alice_pane" hook stop --format claude)"
assert_contains "$drain_busy" "HOOK_BUSY_QUEUE" "hook stop drains queued busy mail"
python3 - "$(muxa_box alice)/cur" <<'PY'
import os, sys, time
d = sys.argv[1]
now = time.time()
for name in os.listdir(d):
    p = os.path.join(d, name)
    if os.path.isfile(p):
        os.utime(p, (now - 5, now - 5))
PY
muxa_as "$alice_pane" hook stop --format claude >/dev/null
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- cursor busy hook pane queues for Stop (S6) ---
muxa_as "$alice_pane" register --name alice --kind cursor --deliver hook --parent bob >/dev/null
tmux -L "$SOCK" set-option -p -t "$alice_pane" @muxa_hook_ok 1 2>/dev/null || true
muxa_as "$alice_pane" state busy
: >"$alice_out"
qcurbusy="$(muxa_as "$bob_pane" send alice 'CURSOR_BUSY_STOP')"
assert_contains "$qcurbusy" "queued bob → alice id=" "cursor busy hook pane queues"
assert_contains "$qcurbusy" "hook will drain" "cursor busy hook queue reason"
sleep 0.2
got="$(cat "$alice_out" 2>/dev/null || true)"
case "$got" in
  *CURSOR_BUSY_STOP*) bad "cursor busy hook pane is not pasted" "got: $got" ;;
  *) ok "cursor busy hook pane is not pasted" ;;
esac
drain_curbusy="$(muxa_as "$alice_pane" hook stop --format cursor)"
assert_contains "$drain_curbusy" "followup_message" "cursor stop JSON on busy drain"
assert_contains "$drain_curbusy" "CURSOR_BUSY_STOP" "cursor stop drains queued busy mail"
python3 - "$(muxa_box alice)/cur" <<'PY'
import os, sys, time
d = sys.argv[1]
now = time.time()
for name in os.listdir(d):
    p = os.path.join(d, name)
    if os.path.isfile(p):
        os.utime(p, (now - 5, now - 5))
PY
muxa_as "$alice_pane" hook stop --format cursor >/dev/null
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true
tmux -L "$SOCK" set-option -p -t "$alice_pane" -u @muxa_unread 2>/dev/null || true
muxa_as "$alice_pane" state idle

# --- hook inject failure unclaims ---
muxa_as "$alice_pane" state idle
: >"$alice_out"
tmux -L "$SOCK" copy-mode -t "$alice_pane"
fail_sent="$(muxa_as "$bob_pane" send alice 'HOOK_FAIL_UNCLAIM' 2>&1)"
assert_contains "$fail_sent" "queued bob → alice" "copy-mode hook send queues"
sleep 0.2
got="$(cat "$alice_out" 2>/dev/null || true)"
case "$got" in
  *HOOK_FAIL_UNCLAIM*) bad "copy-mode hook send does not paste" "got: $got" ;;
  *) ok "copy-mode hook send does not paste" ;;
esac
peek_fail="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$peek_fail" "HOOK_FAIL_UNCLAIM" "peek still shows failed hook inject"
case "$peek_fail" in
  *claimed\ but\ unconfirmed*) bad "failed hook inject unclaims (not stuck in cur/)" "$peek_fail" ;;
  *) ok "failed hook inject unclaims (not stuck in cur/)" ;;
esac
new_n="$(find "$(muxa_box alice)/new" -type f 2>/dev/null | awk 'END { print NR }')"
cur_n="$(find "$(muxa_box alice)/cur" -type f 2>/dev/null | awk 'END { print NR }')"
[ "$new_n" -ge 1 ] && [ "$cur_n" -eq 0 ] && ok "failed hook inject left mail in new/" \
  || bad "failed hook inject left mail in new/" "new=$new_n cur=$cur_n"
tmux -L "$SOCK" send-keys -t "$alice_pane" -X cancel 2>/dev/null || true
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- unread recompute under concurrency ---
muxa_as "$alice_pane" state busy
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true
muxa_as "$bob_pane" send alice 'conc-unread-a' >/dev/null &
muxa_as "$bob_pane" send alice 'conc-unread-b' >/dev/null &
wait
sleep 0.2
opt="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_unread}')"
files="$(find "$(muxa_box alice)/new" -type f | awk 'END { print NR }')"
[ "$opt" = "$files" ] && [ "$files" = "2" ] && ok "unread option equals mailbox count under concurrency" \
  || bad "unread option equals mailbox count under concurrency" "opt=$opt files=$files"
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true
tmux -L "$SOCK" set-option -p -t "$alice_pane" -u @muxa_unread 2>/dev/null || true

# restore alice as bob's child for later ACL tests
muxa_as "$alice_pane" register --name alice --kind generic --deliver inject --parent bob >/dev/null
muxa_as "$alice_pane" state idle
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true

# --- deliver prechecks vs --force (Q3) ---
: >"$alice_out"
tmux -L "$SOCK" copy-mode -t "$alice_pane"
muxa_as "$bob_pane" send alice 'FORCE_BODY' >/dev/null
set +e
del_plain="$(muxa_as "$bob_pane" deliver alice 2>&1)"
del_rc=$?
set -e
[ "$del_rc" -ne 0 ] && ok "deliver in copy-mode exits non-zero" \
  || bad "deliver in copy-mode exits non-zero" "exit=$del_rc out=$del_plain"
assert_contains "$del_plain" "not ready to receive" "deliver in copy-mode prints a reason"
got="$(cat "$alice_out" 2>/dev/null || true)"
case "$got" in
  *FORCE_BODY*) bad "plain deliver does not paste in copy-mode" "got: $got" ;;
  *) ok "plain deliver does not paste in copy-mode" ;;
esac
del_force="$(muxa_as "$bob_pane" deliver --force alice 2>&1)"
assert_contains "$del_force" "skipping readiness prechecks" "deliver --force warns"
assert_contains "$del_force" "injected into alice" "deliver --force injects"
sleep 0.2
got="$(cat "$alice_out" 2>/dev/null || true)"
assert_contains "$got" "FORCE_BODY" "deliver --force lands the body"
tmux -L "$SOCK" send-keys -t "$alice_pane" -X cancel 2>/dev/null || true

# --- unknown target ---
err="$(muxa_as "$bob_pane" send nobody hi 2>&1 || true)"
assert_contains "$err" "unknown agent" "unknown name errors"

# --- unique ids ---
id_a="$(muxa_as "$alice_pane" id)"
id_b="$(muxa_as "$bob_pane" id)"
[ -n "$id_a" ] && [ -n "$id_b" ] && [ "$id_a" != "$id_b" ] && ok "ids unique" || bad "ids unique" "a=$id_a b=$id_b"

# --- parent/child ACL ---
tmux -L "$SOCK" new-window -t muxa -n carol "exec sleep 3600"
tmux -L "$SOCK" new-window -t muxa -n dave "exec sleep 3600"
sleep 0.2
carol_pane="$(tmux -L "$SOCK" list-panes -t muxa:carol -F '#{pane_id}')"
dave_pane="$(tmux -L "$SOCK" list-panes -t muxa:dave -F '#{pane_id}')"

muxa_as "$carol_pane" register --name carol --kind generic --deliver inject --parent bob >/dev/null
muxa_as "$dave_pane" register --name dave --kind generic --deliver inject --parent bob >/dev/null

who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "carol" "who lists child carol"
par="$(muxa_as "$carol_pane" parent)"
assert_contains "$par" "bob" "carol parent is bob"
kids="$(muxa_as "$bob_pane" children)"
assert_contains "$kids" "carol" "bob children include carol"
assert_contains "$kids" "dave" "bob children include dave"

sent="$(muxa_as "$bob_pane" send carol 'from-parent')"
assert_contains "$sent" "delivered bob → carol" "parent → child allowed"

sent="$(muxa_as "$carol_pane" send bob 'from-child')"
assert_contains "$sent" "delivered carol → bob" "child → parent allowed"

sib="$(muxa_as "$carol_pane" send dave 'nope' 2>&1 || true)"
assert_contains "$sib" "forbidden" "sibling send refused"

sib2="$(muxa_as "$alice_pane" send carol 'nope' 2>&1 || true)"
assert_contains "$sib2" "forbidden" "sibling alice → carol refused"

# --- send --all dedupes duplicate roster names ---
saved_dave_name="$(tmux -L "$SOCK" display-message -p -t "$dave_pane" '#{@muxa_name}')"
tmux -L "$SOCK" set-option -p -t "$dave_pane" @muxa_name carol
marker="send-all-dedupe-$$"
muxa_as "$bob_pane" send --no-reply --all "$marker" >/dev/null
peek_carol="$(muxa_as "$bob_pane" peek carol)"
count="$(printf '%s\n' "$peek_carol" | grep -cF "$marker" || true)"
tmux -L "$SOCK" set-option -p -t "$dave_pane" @muxa_name "$saved_dave_name"
[ "$count" -eq 1 ] && ok "send --all dedupes duplicate roster names" \
  || bad "send --all dedupes duplicate roster names" "expected 1 message, count=$count peek=$peek_carol"

tmux -L "$SOCK" new-window -t muxa -n eve "exec sleep 3600"
sleep 0.2
eve_pane="$(tmux -L "$SOCK" list-panes -t muxa:eve -F '#{pane_id}')"
muxa_as "$eve_pane" register --name eve --kind generic --deliver inject >/dev/null

r2r="$(muxa_as "$eve_pane" send bob 'nope' 2>&1 || true)"
assert_contains "$r2r" "forbidden" "root → root refused"

r2r2="$(muxa_as "$bob_pane" send eve 'nope' 2>&1 || true)"
assert_contains "$r2r2" "forbidden" "root → other root refused"

root_to_child="$(muxa_as "$eve_pane" send carol 'nope' 2>&1 || true)"
assert_contains "$root_to_child" "forbidden" "unrelated root → child refused"

none="$(muxa_as "$eve_pane" send --all 'nope')"
assert_contains "$none" "no reachable peers" "root --all has no peers"

# --- spawn (default: split into parent window, tiled grid) ---
bob_win="$(tmux -L "$SOCK" display-message -t "$bob_pane" -p '#{session_name}:#{window_index}')"
n0="$(tmux -L "$SOCK" list-panes -t "$bob_win" -F '#{pane_id}' | awk 'END { print NR }')"

spawn_pane_id() {
  awk '{ for (i = 1; i <= NF; i++) if ($i ~ /^pane=/) { sub(/^pane=/, "", $i); print $i; exit } }'
}

spawned="$(muxa_as "$bob_pane" spawn --name grid1 -- sleep 3600)"
assert_contains "$spawned" "spawned grid1" "default spawn creates child pane"
assert_contains "$spawned" "parent=bob" "spawn records parent"
grid1_pane="$(printf '%s\n' "$spawned" | spawn_pane_id)"
grid1_win="$(tmux -L "$SOCK" display-message -t "$grid1_pane" -p '#{session_name}:#{window_index}')"
[ "$grid1_win" = "$bob_win" ] && ok "default spawn stays in parent window" \
  || bad "default spawn stays in parent window" "parent=$bob_win child=$grid1_win"
n1="$(tmux -L "$SOCK" list-panes -t "$bob_win" -F '#{pane_id}' | awk 'END { print NR }')"
[ "$n1" -gt "$n0" ] && ok "default spawn adds a pane" \
  || bad "default spawn adds a pane" "before=$n0 after=$n1"
sp_par="$(muxa_as "$grid1_pane" parent)"
assert_contains "$sp_par" "bob" "spawned pane parent option"

muxa_as "$bob_pane" spawn --name grid2 -- sleep 3600 >/dev/null
muxa_as "$bob_pane" spawn --name grid3 -- sleep 3600 >/dev/null
n3="$(tmux -L "$SOCK" list-panes -t "$bob_win" -F '#{pane_id}' | awk 'END { print NR }')"
expect=$((n0 + 3))
[ "$n3" -eq "$expect" ] && ok "three default spawns stay in parent window" \
  || bad "three default spawns stay in parent window" "expected $expect panes, got $n3"

win_sp="$(muxa_as "$bob_pane" spawn --name spawned --window -- sleep 3600)"
assert_contains "$win_sp" "spawned spawned" "spawn --window creates named window"
win_list="$(tmux -L "$SOCK" list-windows -F '#{window_name}')"
assert_contains "$win_list" "spawned" "spawn --window names the window"
sp_par="$(muxa_as "$(tmux -L "$SOCK" list-panes -t muxa:spawned -F '#{pane_id}')" parent)"
assert_contains "$sp_par" "bob" "spawn --window pane parent option"

split_sp="$(muxa_as "$bob_pane" spawn --name splitkid --split -- sleep 3600)"
assert_contains "$split_sp" "spawned splitkid" "spawn --split still works"
split_pane="$(printf '%s\n' "$split_sp" | spawn_pane_id)"
split_win="$(tmux -L "$SOCK" display-message -t "$split_pane" -p '#{session_name}:#{window_index}')"
[ "$split_win" = "$bob_win" ] && ok "spawn --split stays in parent window" \
  || bad "spawn --split stays in parent window" "parent=$bob_win child=$split_win"
n_split="$(tmux -L "$SOCK" list-panes -t "$bob_win" -F '#{pane_id}' | awk 'END { print NR }')"
[ "$n_split" -eq $((expect + 1)) ] && ok "spawn --split adds a pane in the grid" \
  || bad "spawn --split adds a pane in the grid" "expected $((expect + 1)) panes, got $n_split"

# Dedicated wide window: 4 default spawns must be 2D (not a single row/column).
tmux -L "$SOCK" new-window -t muxa -n gridhost "exec sleep 3600"
sleep 0.2
grid_pane="$(tmux -L "$SOCK" list-panes -t muxa:gridhost -F '#{pane_id}')"
tmux -L "$SOCK" resize-window -t muxa:gridhost -x 200 -y 40 2>/dev/null || true
muxa_as "$grid_pane" register --name gridhost --kind generic --deliver inject >/dev/null
grid_win="$(tmux -L "$SOCK" display-message -t "$grid_pane" -p '#{session_name}:#{window_index}')"

muxa_as "$grid_pane" spawn --name g1 -- sleep 3600 >/dev/null
muxa_as "$grid_pane" spawn --name g2 -- sleep 3600 >/dev/null
n2="$(tmux -L "$SOCK" list-panes -t "$grid_win" -F '#{pane_id}' | awk 'END { print NR }')"
[ "$n2" -eq 3 ] && ok "two default spawns stay in grid window" \
  || bad "two default spawns stay in grid window" "expected 3 panes, got $n2"

muxa_as "$grid_pane" spawn --name g3 -- sleep 3600 >/dev/null
muxa_as "$grid_pane" spawn --name g4 -- sleep 3600 >/dev/null
n4="$(tmux -L "$SOCK" list-panes -t "$grid_win" -F '#{pane_id}' | awk 'END { print NR }')"
[ "$n4" -eq 5 ] && ok "four default spawns stay in grid window" \
  || bad "four default spawns stay in grid window" "expected 5 panes, got $n4"
nleft="$(tmux -L "$SOCK" list-panes -t "$grid_win" -F '#{pane_left}' | sort -u | awk 'END { print NR }')"
ntop="$(tmux -L "$SOCK" list-panes -t "$grid_win" -F '#{pane_top}' | sort -u | awk 'END { print NR }')"
[ "$nleft" -ge 2 ] && [ "$ntop" -ge 2 ] && ok "four default spawns form a 2D grid" \
  || bad "four default spawns form a 2D grid" "distinct pane_left=$nleft pane_top=$ntop (need >=2 each)"

# --- generated names ---
gen1="$(muxa_as "$bob_pane" spawn -- sleep 3600)"
gen1_name="$(printf '%s\n' "$gen1" | awk '{print $2}')"
if printf '%s' "$gen1_name" | grep -Eq '^[a-z]+-[a-z]+(-[0-9]+)?$'; then
  ok "nameless spawn is adjective-noun"
else
  bad "nameless spawn is adjective-noun" "got: $gen1_name from $gen1"
fi

gen2="$(muxa_as "$bob_pane" spawn -- sleep 3600)"
gen2_name="$(printf '%s\n' "$gen2" | awk '{print $2}')"
if printf '%s' "$gen2_name" | grep -Eq '^[a-z]+-[a-z]+(-[0-9]+)?$'; then
  ok "second nameless spawn is adjective-noun"
else
  bad "second nameless spawn is adjective-noun" "got: $gen2_name from $gen2"
fi
[ "$gen1_name" != "$gen2_name" ] && ok "nameless spawns get different names" \
  || bad "nameless spawns get different names" "both=$gen1_name"

# --- spawn cwd: process PWD and --cwd, not parent pane path ---
same_dir() {
  python3 -c 'import os,sys; sys.exit(0 if os.path.realpath(sys.argv[1])==os.path.realpath(sys.argv[2]) else 1)' "$1" "$2"
}
spawn_wt="$tmpdir/spawn-wt"
spawn_flag="$tmpdir/spawn-flag"
mkdir -p "$spawn_wt" "$spawn_flag"
# Parent pane stays at its original path; only the muxa process cds.
from_pwd="$(cd "$spawn_wt" && muxa_as "$bob_pane" spawn --name frompwd -- sleep 3600 2>"$tmpdir/frompwd.err")"
assert_contains "$from_pwd" "cwd=$spawn_wt" "spawn stdout includes process PWD"
case "$(cat "$tmpdir/frompwd.err")" in
  *"already has live worker"*) bad "spawn from unique PWD is silent" "err=$(cat "$tmpdir/frompwd.err")" ;;
  *) ok "spawn from unique PWD is silent" ;;
esac
frompwd_pane="$(printf '%s\n' "$from_pwd" | spawn_pane_id)"
frompwd_cwd="$(tmux -L "$SOCK" display-message -t "$frompwd_pane" -p '#{pane_current_path}')"
same_dir "$frompwd_cwd" "$spawn_wt" && ok "spawn from cd'd PWD starts child there" \
  || bad "spawn from cd'd PWD starts child there" "want=$spawn_wt got=$frompwd_cwd"

from_flag="$(muxa_as "$bob_pane" spawn --cwd "$spawn_flag" --name fromflag -- sleep 3600)"
fromflag_abs="$(cd "$spawn_flag" && pwd)"
assert_contains "$from_flag" "cwd=$fromflag_abs" "spawn --cwd in stdout"
fromflag_pane="$(printf '%s\n' "$from_flag" | spawn_pane_id)"
fromflag_cwd="$(tmux -L "$SOCK" display-message -t "$fromflag_pane" -p '#{pane_current_path}')"
same_dir "$fromflag_cwd" "$spawn_flag" && ok "spawn --cwd starts child there" \
  || bad "spawn --cwd starts child there" "want=$spawn_flag got=$fromflag_cwd"

set +e
muxa_as "$bob_pane" spawn --cwd "$tmpdir/no-such-cwd" --name badcwd -- sleep 3600 >/dev/null 2>&1
cwd_code=$?
set -e
[ "$cwd_code" -eq 2 ] && ok "spawn --cwd missing dir exits 2" \
  || bad "spawn --cwd missing dir exits 2" "exit=$cwd_code"

# --- spawn flag order: muxa flags after the command must fail closed ---
set +e
flag_order_err="$(muxa_as "$bob_pane" spawn sleep --cwd "$spawn_flag" --name flag-order-probe 2>&1)"
flag_order_code=$?
set -e
[ "$flag_order_code" -eq 2 ] && ok "spawn flags after command exit 2" \
  || bad "spawn flags after command exit 2" "exit=$flag_order_code out=$flag_order_err"
assert_contains "$flag_order_err" "must precede the command" "spawn flags after command explains fix"

flag_ok="$(muxa_as "$bob_pane" spawn --name flag-order-ok --cwd "$spawn_flag" -- sleep 3600)"
assert_contains "$flag_ok" "spawned flag-order-ok" "spawn flags before -- succeed"
assert_contains "$flag_ok" "cwd=$fromflag_abs" "spawn -- before command honors --cwd"

flag_child="$(muxa_as "$bob_pane" spawn --name flag-child-flags -- sh -c 'exec sleep 3600' -- --cwd "$spawn_flag")"
assert_contains "$flag_child" "spawned flag-child-flags" "child flags after -- are allowed"

flag_child_sep="$(muxa_as "$bob_pane" spawn --name flag-child-sep sh -c 'exec sleep 3600' -- --cwd "$spawn_flag")"
assert_contains "$flag_child_sep" "spawned flag-child-sep" "child -- separator allows later muxa-like flags"

wait_args_file() {
  local file="$1" i=0
  while [ $i -lt 30 ] && [ ! -s "$file" ]; do
    sleep 0.1
    i=$((i + 1))
  done
}

# --- spawn claude: do not append -p/--print (muxa#29) ---
fake_bin="$tmpdir/fake-bin"
mkdir -p "$fake_bin"
claude_args="$tmpdir/claude-args.txt"
: > "$claude_args"
cat > "$fake_bin/claude" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" > "$claude_args"
exec sleep 3600
EOF
chmod +x "$fake_bin/claude"
claude_sp="$(muxa_as "$bob_pane" spawn --name claude-test -- "$fake_bin/claude" --model haiku)"
assert_contains "$claude_sp" "spawned claude-test" "spawn claude creates child"
assert_contains "$claude_sp" "kind=claude" "spawn claude infers kind=claude"
wait_args_file "$claude_args"
claude_argv="$(cat "$claude_args" 2>/dev/null || true)"
case " $claude_argv " in
  *" -p "*|*" --print "*) bad "spawn claude does not append -p/--print" "argv=$claude_argv" ;;
  *) ok "spawn claude does not append -p/--print" ;;
esac
assert_contains "$claude_argv" "--model" "spawn claude passes caller flags"
assert_contains "$claude_argv" "haiku" "spawn claude passes --model value"

: > "$claude_args"
muxa_as "$bob_pane" spawn --name claude-print -- "$fake_bin/claude" --print hello >/dev/null
wait_args_file "$claude_args"
print_argv="$(cat "$claude_args" 2>/dev/null || true)"
case " $print_argv " in
  *" --print "*) ok "spawn claude preserves caller --print" ;;
  *) bad "spawn claude preserves caller --print" "argv=$print_argv" ;;
esac

cursor_args="$tmpdir/cursor-args.txt"
: > "$cursor_args"
cat > "$fake_bin/agent" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" > "$cursor_args"
exec sleep 3600
EOF
chmod +x "$fake_bin/agent"
muxa_as "$bob_pane" spawn --name cursor-test --kind cursor -- "$fake_bin/agent" --model foo >/dev/null
wait_args_file "$cursor_args"
cursor_argv="$(cat "$cursor_args" 2>/dev/null || true)"
case " $cursor_argv " in
  *" --trust "*) ok "spawn cursor appends --trust" ;;
  *) bad "spawn cursor appends --trust" "argv=$cursor_argv" ;;
esac

fail_sp="$(muxa_as "$bob_pane" spawn --name failfast -- false)"
fail_pane="$(printf '%s\n' "$fail_sp" | spawn_pane_id)"
sleep 0.3
fail_win="$(tmux -L "$SOCK" display-message -t "$fail_pane" -p '#{session_name}:#{window_index}' 2>/dev/null || true)"
fail_roe="$(tmux -L "$SOCK" show-window-options -t "$fail_win" -v remain-on-exit 2>/dev/null || true)"
[ "$fail_roe" = "on" ] && ok "spawn sets remain-on-exit on child window" \
  || bad "spawn sets remain-on-exit on child window" "remain-on-exit=$fail_roe"
fail_dead="$(tmux -L "$SOCK" display-message -t "$fail_pane" -p '#{pane_dead}' 2>/dev/null || echo 0)"
[ "$fail_dead" = "1" ] && ok "failed spawn child pane stays visible as dead" \
  || bad "failed spawn child pane stays visible as dead" "pane_dead=$fail_dead"

# --- spawn cwd occupancy: warn on stderr, still create the pane ---
occ_dir="$tmpdir/occ-cwd"
occ_git="$tmpdir/occ-git"
occ_linked="$tmpdir/occ-linked"
occ_root="$tmpdir/occ-root"
mkdir -p "$occ_dir" "$occ_root"
ln -s "$occ_dir" "$tmpdir/occ-link"
mkdir -p "$occ_git"
git init -q "$occ_git" >/dev/null 2>&1
git -C "$occ_git" symbolic-ref HEAD refs/heads/main
git -C "$occ_git" -c user.email=muxa@example.com -c user.name=muxa -c commit.gpgsign=false \
  commit -q --allow-empty -m init
git -C "$occ_git" worktree add -q -b occ-feat "$occ_linked" >/dev/null 2>&1

occ_spawn() {
  local errf="$1"
  shift
  set +e
  occ_out="$(muxa_as "$bob_pane" spawn "$@" 2>"$errf")"
  occ_code=$?
  set -e
  occ_err="$(cat "$errf")"
}

occ_spawn "$tmpdir/occ1.err" --cwd "$occ_dir" --name occ1 -- sleep 3600
[ "$occ_code" -eq 0 ] && ok "first spawn into free cwd exits 0" \
  || bad "first spawn into free cwd exits 0" "exit=$occ_code out=$occ_out err=$occ_err"
assert_contains "$occ_out" "spawned occ1" "first spawn into free cwd succeeds"
case "$occ_err" in
  *"already has live worker"*) bad "first spawn into free cwd is silent" "err=$occ_err" ;;
  *) ok "first spawn into free cwd is silent" ;;
esac

occ_spawn "$tmpdir/occ2.err" --cwd "$occ_dir" --name occ2 -- sleep 3600
[ "$occ_code" -eq 0 ] && ok "occupied --cwd spawn still exits 0" \
  || bad "occupied --cwd spawn still exits 0" "exit=$occ_code out=$occ_out err=$occ_err"
assert_contains "$occ_out" "spawned occ2" "occupied --cwd spawn still creates the pane"
assert_contains "$occ_err" "already has live worker occ1" "occupied --cwd warns on stderr"

occ_spawn "$tmpdir/occ-link.err" --cwd "$tmpdir/occ-link" --name occ3 -- sleep 3600
[ "$occ_code" -eq 0 ] && ok "symlink --cwd spawn still exits 0" \
  || bad "symlink --cwd spawn still exits 0" "exit=$occ_code out=$occ_out err=$occ_err"
assert_contains "$occ_out" "spawned occ3" "symlink --cwd spawn still creates the pane"
assert_contains "$occ_err" "already has live worker" "symlink to occupied cwd warns"

tmux -L "$SOCK" new-window -d -t muxa -n occroot -c "$occ_root" "exec sleep 3600"
sleep 0.2
occroot_pane="$(tmux -L "$SOCK" list-panes -t muxa:occroot -F '#{pane_id}' | head -1)"
muxa_as "$occroot_pane" register --name occroot --kind generic --deliver inject >/dev/null
occ_spawn "$tmpdir/occ-root.err" --cwd "$occ_root" --name occ-fromroot -- sleep 3600
[ "$occ_code" -eq 0 ] && ok "spawn into a root's cwd exits 0" \
  || bad "spawn into a root's cwd exits 0" "exit=$occ_code out=$occ_out err=$occ_err"
assert_contains "$occ_out" "spawned occ-fromroot" "spawn into a root's cwd creates the pane"
case "$occ_err" in
  *"already has live worker"*) bad "root occupying cwd does not warn" "err=$occ_err" ;;
  *) ok "root occupying cwd does not warn" ;;
esac

ghost_dir="$tmpdir/occ-ghost"
mkdir -p "$ghost_dir"
tmux -L "$SOCK" new-window -d -t muxa -n occghost -c "$ghost_dir" "exec zsh"
sleep 0.2
occghost_pane="$(tmux -L "$SOCK" list-panes -t muxa:occghost -F '#{pane_id}' | head -1)"
muxa_as "$occghost_pane" register --name occghost --kind cursor --deliver hook --parent bob >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_who_status "$who" "occghost" "ghost" "cursor+shell occupant is ghost"
occ_spawn "$tmpdir/occ-ghost.err" --cwd "$ghost_dir" --name occ-afterghost -- sleep 3600
[ "$occ_code" -eq 0 ] && ok "spawn into a ghost worker cwd exits 0" \
  || bad "spawn into a ghost worker cwd exits 0" "exit=$occ_code out=$occ_out err=$occ_err"
assert_contains "$occ_out" "spawned occ-afterghost" "spawn into a ghost worker cwd creates the pane"
case "$occ_err" in
  *"already has live worker"*) bad "ghost occupying cwd does not warn" "err=$occ_err" ;;
  *) ok "ghost occupying cwd does not warn" ;;
esac

occ_spawn "$tmpdir/occ-wt1.err" --cwd "$occ_linked" --name occ-wt1 -- sleep 3600
[ "$occ_code" -eq 0 ] && ok "first spawn into a worktree exits 0" \
  || bad "first spawn into a worktree exits 0" "exit=$occ_code out=$occ_out err=$occ_err"
assert_contains "$occ_out" "spawned occ-wt1" "first spawn into a worktree succeeds"
case "$occ_err" in
  *"already has live worker"*) bad "first spawn into a worktree is silent" "err=$occ_err" ;;
  *) ok "first spawn into a worktree is silent" ;;
esac

occ_spawn "$tmpdir/occ-wt2.err" --cwd "$occ_linked" --name occ-wt2 -- sleep 3600
[ "$occ_code" -eq 0 ] && ok "occupied worktree spawn still exits 0" \
  || bad "occupied worktree spawn still exits 0" "exit=$occ_code out=$occ_out err=$occ_err"
assert_contains "$occ_out" "spawned occ-wt2" "occupied worktree spawn still creates the pane"
assert_contains "$occ_err" "already has live worker occ-wt1" "occupied worktree warns on stderr"

occ_spawn "$tmpdir/occ-primary.err" --cwd "$occ_git" --name occ-primary -- sleep 3600
[ "$occ_code" -eq 0 ] && ok "spawn into a sibling worktree exits 0" \
  || bad "spawn into a sibling worktree exits 0" "exit=$occ_code out=$occ_out err=$occ_err"
assert_contains "$occ_out" "spawned occ-primary" "spawn into a sibling worktree creates the pane"
case "$occ_err" in
  *"already has live worker"*) bad "sibling worktree does not warn" "err=$occ_err" ;;
  *) ok "sibling worktree does not warn" ;;
esac

set +e
pf_occ="$(cd "$occ_git" && "$ROOT/bin/muxa" preflight "$occ_linked" 2>&1)"
pf_occ_code=$?
set -e
[ "$pf_occ_code" -eq 0 ] && ok "preflight ignores occupied worktree roster" \
  || bad "preflight ignores occupied worktree roster" "exit=$pf_occ_code out=$pf_occ"
case "$pf_occ" in
  *"live worker"*) bad "preflight stays git-only (no cwd occupancy warning)" "out=$pf_occ" ;;
  *) ok "preflight stays git-only (no cwd occupancy warning)" ;;
esac

# --- kind detection: cursor-agent node vs claude SessionStart ---
who_kind_for() {
  local who="$1" name="$2" k
  k="$(printf '%s\n' "$who" | awk -v n="$name" '$1==n { print $5; exit }')"
  k="${k#"${k%%[![:space:]]*}"}"
  k="${k%"${k##*[![:space:]]}"}"
  printf '%s' "$k"
}

kind_fakebin="$tmpdir/kind-fakebin"
mkdir -p "$kind_fakebin/cursor-agent"
cat > "$kind_fakebin/cursor-agent/node" <<'EOF'
#!/bin/sh
# Keep this shell (and its script path) in argv; exec sleep drops cursor-agent on Linux.
while sleep 3600; do :; done
EOF
chmod +x "$kind_fakebin/cursor-agent/node"

cat > "$kind_fakebin/claude" <<'EOF'
#!/bin/sh
exec sleep 3600
EOF
chmod +x "$kind_fakebin/claude"

cat > "$kind_fakebin/agent" <<'EOF'
#!/bin/sh
exec sleep 3600
EOF
chmod +x "$kind_fakebin/agent"

tmux -L "$SOCK" new-window -t muxa -n kindnode \
  "$kind_fakebin/cursor-agent/node"
sleep 0.2
kindnode_pane="$(tmux -L "$SOCK" list-panes -t muxa:kindnode -F '#{pane_id}' | head -1)"
kindnode_cmd="$(tmux -L "$SOCK" display-message -t "$kindnode_pane" -p '#{pane_current_command}')"
case "$kindnode_cmd" in
  node) ok "cursor-agent stub shows as node in tmux" ;;
  *)
    # Some platforms report the script basename; argv detection still applies.
    ok "cursor-agent stub foreground is $kindnode_cmd (argv classifies cursor)"
    ;;
esac
printf '%s' '{"session_id":"node-cursor-1"}' \
  | muxa_as "$kindnode_pane" hook session-start --kind claude >/dev/null
kindnode_kind="$(tmux -L "$SOCK" display-message -t "$kindnode_pane" -p '#{@muxa_kind}')"
[ "$kindnode_kind" = "cursor" ] && ok "node cursor-agent path registers as cursor" \
  || bad "node cursor-agent path registers as cursor" "got=$kindnode_kind"

tmux -L "$SOCK" new-window -t muxa -n kindclaude \
  "$kind_fakebin/claude"
sleep 0.2
kindclaude_pane="$(tmux -L "$SOCK" list-panes -t muxa:kindclaude -F '#{pane_id}' | head -1)"
printf '%s' '{"session_id":"claude-sess-1"}' \
  | muxa_as "$kindclaude_pane" hook session-start --kind claude >/dev/null
kindclaude_kind="$(tmux -L "$SOCK" display-message -t "$kindclaude_pane" -p '#{@muxa_kind}')"
[ "$kindclaude_kind" = "claude" ] && ok "claude pane stays claude on SessionStart" \
  || bad "claude pane stays claude on SessionStart" "got=$kindclaude_kind"

# Spawned cursor child must not flip to claude when a stray claude hook fires.
spawn_agent="$(muxa_as "$bob_pane" spawn --name kind-agent -- "$kind_fakebin/agent")"
assert_contains "$spawn_agent" "kind=cursor" "spawn agent infers cursor kind"
kind_agent_pane="$(printf '%s\n' "$spawn_agent" | spawn_pane_id)"
printf '%s' '{"session_id":"spawn-agent-1"}' \
  | muxa_as "$kind_agent_pane" hook session-start --kind claude >/dev/null
kind_agent_kind="$(tmux -L "$SOCK" display-message -t "$kind_agent_pane" -p '#{@muxa_kind}')"
[ "$kind_agent_kind" = "cursor" ] && ok "spawned cursor survives claude SessionStart" \
  || bad "spawned cursor survives claude SessionStart" "got=$kind_agent_kind"
who_kind="$(muxa_as "$bob_pane" who)"
[ "$(who_kind_for "$who_kind" "kind-agent")" = "cursor" ] \
  && ok "who KIND matches cursor foreground after stray hook" \
  || bad "who KIND matches cursor foreground after stray hook" \
     "got=$(who_kind_for "$who_kind" "kind-agent")"

mkdir -p "$kind_fakebin/has-component" "$kind_fakebin/docker-compose"
cat > "$kind_fakebin/has-component/run" <<'EOF'
#!/bin/sh
exec sleep 3600
EOF
cat > "$kind_fakebin/docker-compose/run" <<'EOF'
#!/bin/sh
exec sleep 3600
EOF
chmod +x "$kind_fakebin/has-component/run" "$kind_fakebin/docker-compose/run"

tmux -L "$SOCK" new-window -t muxa -n kindcomp \
  "$kind_fakebin/has-component/run"
sleep 0.2
kindcomp_pane="$(tmux -L "$SOCK" list-panes -t muxa:kindcomp -F '#{pane_id}' | head -1)"
printf '%s' '{"session_id":"comp-1"}' \
  | muxa_as "$kindcomp_pane" hook session-start --kind generic >/dev/null
kindcomp_kind="$(tmux -L "$SOCK" display-message -t "$kindcomp_pane" -p '#{@muxa_kind}')"
[ "$kindcomp_kind" = "generic" ] && ok "component path does not classify as pi" \
  || bad "component path does not classify as pi" "got=$kindcomp_kind"

tmux -L "$SOCK" new-window -t muxa -n kindcompose \
  "$kind_fakebin/docker-compose/run"
sleep 0.2
kindcompose_pane="$(tmux -L "$SOCK" list-panes -t muxa:kindcompose -F '#{pane_id}' | head -1)"
printf '%s' '{"session_id":"compose-1"}' \
  | muxa_as "$kindcompose_pane" hook session-start --kind generic >/dev/null
kindcompose_kind="$(tmux -L "$SOCK" display-message -t "$kindcompose_pane" -p '#{@muxa_kind}')"
[ "$kindcompose_kind" = "generic" ] && ok "compose path does not classify as pi" \
  || bad "compose path does not classify as pi" "got=$kindcompose_kind"

cat > "$kind_fakebin/omp" <<'EOF'
#!/bin/sh
exec sleep 3600
EOF
chmod +x "$kind_fakebin/omp"
tmux -L "$SOCK" new-window -t muxa -n kindomp \
  "$kind_fakebin/omp"
sleep 0.2
kindomp_pane="$(tmux -L "$SOCK" list-panes -t muxa:kindomp -F '#{pane_id}' | head -1)"
printf '%s' '{"session_id":"omp-1"}' \
  | muxa_as "$kindomp_pane" hook session-start --kind pi >/dev/null
kindomp_kind="$(tmux -L "$SOCK" display-message -t "$kindomp_pane" -p '#{@muxa_kind}')"
[ "$kindomp_kind" = "pi" ] && ok "omp executable still classifies as pi" \
  || bad "omp executable still classifies as pi" "got=$kindomp_kind"

mkdir -p "$kind_fakebin/claude-projects/cursor-agent"
cat > "$kind_fakebin/claude-projects/cursor-agent/node" <<'EOF'
#!/bin/sh
while sleep 3600; do :; done
EOF
chmod +x "$kind_fakebin/claude-projects/cursor-agent/node"

spawn_claudeproj="$(muxa_as "$bob_pane" spawn --name kind-claudeproj-spawn -- \
  "$kind_fakebin/claude-projects/cursor-agent/node")"
assert_contains "$spawn_claudeproj" "kind=cursor" \
  "spawn claude-projects/cursor-agent path infers cursor"
case "$spawn_claudeproj" in
  *kind=claude*) bad "spawn claude-projects path is not classified as claude" "$spawn_claudeproj" ;;
  *) ok "spawn claude-projects path is not classified as claude" ;;
esac
spawn_claudeproj_pane="$(printf '%s\n' "$spawn_claudeproj" | spawn_pane_id)"
spawn_claudeproj_kind="$(tmux -L "$SOCK" display-message -t "$spawn_claudeproj_pane" -p '#{@muxa_kind}')"
[ "$spawn_claudeproj_kind" = "cursor" ] \
  && ok "spawn sets @muxa_kind=cursor for claude-projects cursor path" \
  || bad "spawn sets @muxa_kind=cursor for claude-projects cursor path" \
     "got=$spawn_claudeproj_kind"

tmux -L "$SOCK" new-window -t muxa -n kindclaudeproj \
  "$kind_fakebin/claude-projects/cursor-agent/node"
sleep 0.2
kindclaudeproj_pane="$(tmux -L "$SOCK" list-panes -t muxa:kindclaudeproj -F '#{pane_id}' | head -1)"
tmux -L "$SOCK" set-option -p -t "$kindclaudeproj_pane" @muxa_kind cursor
tmux -L "$SOCK" set-option -p -t "$kindclaudeproj_pane" @muxa_name kind-claudeproj
printf '%s' '{"session_id":"claudeproj-1"}' \
  | muxa_as "$kindclaudeproj_pane" hook session-start --kind claude >/dev/null
kindclaudeproj_kind="$(tmux -L "$SOCK" display-message -t "$kindclaudeproj_pane" -p '#{@muxa_kind}')"
[ "$kindclaudeproj_kind" = "cursor" ] \
  && ok "claude-projects cursor path is not misclassified as claude" \
  || bad "claude-projects cursor path is not misclassified as claude" \
     "got=$kindclaudeproj_kind"

# --- CLI session id mapping ---
printf '%s' '{"session_id":"cli-sess-123"}' | muxa_as "$alice_pane" hook session-start --kind generic
sid="$(tmux -L "$SOCK" display-message -p -t "$alice_pane" '#{@muxa_session}')"
[ "$sid" = "cli-sess-123" ] && ok "session-start stores @muxa_session" \
  || bad "session-start stores @muxa_session" "got: $sid"

who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "cli-sess-123" "who shows session id"

# --- Cursor session-start pins MUXA_PANE for later IDE hooks ---
cursor_env="$(printf '%s' '{"session_id":"cursor-conv-1"}' | muxa_as "$alice_pane" hook session-start --kind cursor)"
assert_contains "$cursor_env" '"MUXA_PANE"' "cursor session-start emits MUXA_PANE env"
assert_contains "$cursor_env" "$alice_pane" "cursor session-start env names this pane"

# --- IDE Stop: conversation_id wins over the caller's TMUX_PANE ---
muxa_as "$alice_pane" register --name alice --kind cursor --deliver hook --parent bob >/dev/null
printf '%s' '{"session_id":"ide-conv-9"}' | muxa_as "$alice_pane" hook session-start --kind cursor >/dev/null
muxa_as "$alice_pane" state busy
muxa_as "$bob_pane" state idle
muxa_as "$bob_pane" send alice 'IDE_SESSION_DRAIN' >/dev/null
ide_drain="$(printf '%s' '{"conversation_id":"ide-conv-9","status":"completed"}' | muxa_as "$bob_pane" hook stop --format cursor)"
assert_contains "$ide_drain" "followup_message" "stop from other pane still emits cursor JSON"
assert_contains "$ide_drain" "IDE_SESSION_DRAIN" "conversation_id drains the session mailbox"
alice_st="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_state}')"
[ "$alice_st" = "busy" ] && ok "session stop marks the target pane busy" \
  || bad "session stop marks the target pane busy" "state=$alice_st"
bob_st="$(tmux -L "$SOCK" display-message -t "$bob_pane" -p '#{@muxa_state}')"
[ "$bob_st" = "idle" ] && ok "session stop does not mark the caller pane busy" \
  || bad "session stop does not mark the caller pane busy" "bob_state=$bob_st"
ide_peek="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$ide_peek" "IDE_SESSION_DRAIN" "queued drain stays visible in peek"
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- IDE Stop with no TMUX_PANE (GUI hook) ---
# Busy so send queues for Stop; idle hook panes inject (tested above).
muxa_as "$alice_pane" state busy
muxa_as "$bob_pane" send alice 'NO_TMUX_PANE_DRAIN' >/dev/null
no_pane_drain="$(printf '%s' '{"conversation_id":"ide-conv-9","status":"completed"}' | muxa hook stop --format cursor)"
assert_contains "$no_pane_drain" "NO_TMUX_PANE_DRAIN" "stop without TMUX_PANE drains by conversation_id"
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- aborted stop leaves mail queued ---
muxa_as "$alice_pane" state busy
muxa_as "$bob_pane" send alice 'ABORT_KEEP' >/dev/null
abort_out="$(printf '%s' '{"conversation_id":"ide-conv-9","status":"aborted"}' | muxa_as "$alice_pane" hook stop --format cursor)"
[ -z "$abort_out" ] && ok "aborted stop prints nothing" \
  || bad "aborted stop prints nothing" "got: $abort_out"
abort_peek="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$abort_peek" "ABORT_KEEP" "aborted stop does not claim mail"
abort_st="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_state}')"
[ "$abort_st" = "idle" ] && ok "aborted stop sets idle" \
  || bad "aborted stop sets idle" "state=$abort_st"
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true

# --- afterAgentResponse clears stuck busy ---
muxa_as "$alice_pane" state busy
muxa_as "$alice_pane" hook afterAgentResponse
aar_st="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_state}')"
[ "$aar_st" = "idle" ] && ok "afterAgentResponse sets idle" \
  || bad "afterAgentResponse sets idle" "state=$aar_st"

# --- same-turn second Stop re-emits young cur/ (Cursor uses last hook stdout) ---
muxa_as "$alice_pane" register --name alice --kind cursor --deliver hook --parent bob >/dev/null
muxa_as "$alice_pane" state busy
muxa_as "$bob_pane" send alice 'DUAL_STOP_BODY' >/dev/null
muxa_as "$alice_pane" hook stop --format cursor >/dev/null
second_stop="$(MUXA_SWEEP_MIN_AGE=60 muxa_as "$alice_pane" hook stop --format cursor)"
assert_contains "$second_stop" "followup_message" "same-turn second stop re-emits cursor JSON"
assert_contains "$second_stop" "DUAL_STOP_BODY" "same-turn second stop re-emits young cur/"
dual_peek="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$dual_peek" "DUAL_STOP_BODY" "same-turn second stop does not sweep claim"
pending_cleared="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_stop_pending}')"
[ -z "$pending_cleared" ] && ok "same-turn second stop clears stop_pending" \
  || bad "same-turn second stop clears stop_pending" "got: $pending_cleared"
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

# --- empty stop must not re-emit idle-injected young cur/ ---
muxa_as "$alice_pane" register --name alice --kind cursor --deliver hook --parent bob >/dev/null
tmux -L "$SOCK" set-option -p -t "$alice_pane" @muxa_hook_ok 1 2>/dev/null || true
tmux -L "$SOCK" set-option -p -t "$alice_pane" -u @muxa_stop_pending 2>/dev/null || true
muxa_as "$alice_pane" state busy
noreemit_box="$(muxa_box alice)"
find "$noreemit_box/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$noreemit_box/cur" -type f -exec rm -f {} + 2>/dev/null || true
{
  printf 'From: bob\nTo: alice\nId: inject-young\nTime: 2026-01-01T00:00:00Z\nFlags: \n\nIDLE_INJECT_NO_REEMIT\n'
} >"$noreemit_box/cur/inject-young"
noreemit_stop="$(MUXA_SWEEP_MIN_AGE=60 muxa_as "$alice_pane" hook stop --format cursor)"
[ -z "$noreemit_stop" ] && ok "empty stop does not re-emit idle-injected cur/" \
  || bad "empty stop does not re-emit idle-injected cur/" "got: $noreemit_stop"
pending="$(tmux -L "$SOCK" display-message -t "$alice_pane" -p '#{@muxa_stop_pending}')"
[ -z "$pending" ] && ok "idle-injected cur/ does not set stop_pending" \
  || bad "idle-injected cur/ does not set stop_pending" "got: $pending"
muxa_as "$alice_pane" hook idle >/dev/null
python3 - "$(muxa_box alice)/cur" <<'PY'
import os, sys, time
d = sys.argv[1]
if not os.path.isdir(d):
    raise SystemExit(0)
now = time.time()
for name in os.listdir(d):
    p = os.path.join(d, name)
    if os.path.isfile(p):
        os.utime(p, (now - 5, now - 5))
PY
muxa_as "$alice_pane" hook stop --format cursor >/dev/null
find "$(muxa_box alice)/new" -type f -exec rm -f {} + 2>/dev/null || true
find "$(muxa_box alice)/cur" -type f -exec rm -f {} + 2>/dev/null || true

printf '%s' '{"session_id":"cli-sess-123"}' | muxa_as "$alice_pane" hook session-start --kind generic >/dev/null
muxa_as "$alice_pane" register --name alice --kind generic --deliver inject --parent bob >/dev/null

projdir="$tmpdir/acme-widgets"
mkdir -p "$projdir"
tmux -L "$SOCK" new-window -d -t muxa -n proj -c "$projdir" "exec sleep 3600"
sleep 0.2
proj_pane="$(tmux -L "$SOCK" list-panes -t muxa:proj -F '#{pane_id}' | head -1)"
muxa_as "$proj_pane" register --name projagent --kind generic --deliver inject >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "$projdir" "who shows pane cwd"
assert_contains "$who" "CWD" "who header has CWD"
assert_contains "$who" "STATUS" "who header has STATUS"

# --- who STATUS: ghost vs live ---
tmux -L "$SOCK" new-window -t muxa -n zshghost "exec zsh"
sleep 0.2
zsh_pane="$(tmux -L "$SOCK" list-panes -t muxa:zshghost -F '#{pane_id}')"
muxa_as "$zsh_pane" register --name zsh-cursor --kind cursor --deliver hook >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_who_status "$who" "zsh-cursor" "ghost" "cursor+shell is ghost"

tmux -L "$SOCK" new-window -t muxa -n zshpi "exec zsh"
sleep 0.2
zshpi_pane="$(tmux -L "$SOCK" list-panes -t muxa:zshpi -F '#{pane_id}')"
muxa_as "$zshpi_pane" register --name zsh-pi --kind pi --deliver hook >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_who_status "$who" "zsh-pi" "ghost" "pi+shell is ghost"

tmux -L "$SOCK" new-window -t muxa -n zshgen "exec zsh"
sleep 0.2
zshgen_pane="$(tmux -L "$SOCK" list-panes -t muxa:zshgen -F '#{pane_id}')"
muxa_as "$zshgen_pane" register --name zsh-generic --kind generic --deliver inject >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_who_status "$who" "zsh-generic" "live" "generic+shell is live"

gone_dir="$tmpdir/gone-cwd"
mkdir -p "$gone_dir"
tmux -L "$SOCK" new-window -t muxa -n badcwd -c "$gone_dir" "exec sleep 3600"
sleep 0.2
badcwd_pane="$(tmux -L "$SOCK" list-panes -t muxa:badcwd -F '#{pane_id}')"
rm -rf "$gone_dir"
muxa_as "$badcwd_pane" register --name bad-cwd --kind generic --deliver inject >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_who_status "$who" "bad-cwd" "ghost" "missing pane cwd is ghost"

# --- unregister ---
tmux -L "$SOCK" new-window -t muxa -n unreg "exec sleep 3600"
sleep 0.2
unreg_pane="$(tmux -L "$SOCK" list-panes -t muxa:unreg -F '#{pane_id}')"
muxa_as "$unreg_pane" register --name dropme --kind generic --deliver inject >/dev/null
unreg_out="$(muxa_as "$bob_pane" unregister dropme)"
assert_contains "$unreg_out" "unregistered dropme" "unregister by name confirms"
who="$(muxa_as "$bob_pane" who)"
case "$who" in
  *dropme*) bad "unregister by name removes from who" "still listed: $who" ;;
  *) ok "unregister by name removes from who" ;;
esac

tmux -L "$SOCK" new-window -t muxa -n unreg2 "exec sleep 3600"
sleep 0.2
unreg2_pane="$(tmux -L "$SOCK" list-panes -t muxa:unreg2 -F '#{pane_id}')"
muxa_as "$unreg2_pane" register --name dropid --kind generic --deliver inject >/dev/null
drop_id="$(muxa_as "$unreg2_pane" id)"
unreg_out="$(muxa_as "$bob_pane" unregister "$drop_id")"
assert_contains "$unreg_out" "unregistered dropid" "unregister by id confirms"
who="$(muxa_as "$bob_pane" who)"
case "$who" in
  *dropid*) bad "unregister by id removes from who" "still listed: $who" ;;
  *) ok "unregister by id removes from who" ;;
esac

set +e
muxa_as "$bob_pane" unregister nobody 2>/dev/null
unreg_code=$?
set -e
[ "$unreg_code" -eq 2 ] && ok "unregister unknown exits 2" \
  || bad "unregister unknown exits 2" "exit=$unreg_code"

got_sess="$(muxa_as "$alice_pane" session)"
assert_contains "$got_sess" "cli-sess-123" "muxa session prints CLI session id"

muxa_as "$alice_pane" hook session-end
sid2="$(tmux -L "$SOCK" display-message -p -t "$alice_pane" '#{@muxa_session}')"
[ -z "$sid2" ] && ok "session-end clears @muxa_session" \
  || bad "session-end clears @muxa_session" "got: $sid2"

# --- preflight (git only, no tmux) ---
git_init_repo() {
  local dir="$1"
  mkdir -p "$dir"
  git init -q "$dir" >/dev/null 2>&1
  git -C "$dir" symbolic-ref HEAD refs/heads/main
  git -C "$dir" -c user.email=muxa@example.com -c user.name=muxa -c commit.gpgsign=false \
    commit -q --allow-empty -m init
}

pf_repo="$tmpdir/pf-repo"
pf_wt="$tmpdir/pf-wt"
pf_other="$tmpdir/pf-other"
git_init_repo "$pf_repo"
git_init_repo "$pf_other"
git -C "$pf_repo" worktree add -q -b feat/pf "$pf_wt" >/dev/null 2>&1

pf() {
  local dir="$1"
  shift
  set +e
  pf_out="$(cd "$dir" && "$ROOT/bin/muxa" preflight "$@" 2>&1)"
  pf_code=$?
  set -e
}

pf "$pf_repo" "$pf_wt"
[ "$pf_code" -eq 0 ] && ok "preflight on default branch exits 0" \
  || bad "preflight on default branch exits 0" "exit=$pf_code out=$pf_out"
assert_contains "$pf_out" "ok   base branch main" "preflight defaults base to main"
assert_contains "$pf_out" "on main" "preflight reports primary on main"
assert_contains "$pf_out" "linked on feat/pf" "preflight accepts a linked worktree"
case "$pf_out" in
  *fail*) bad "preflight clean run prints no fail line" "out=$pf_out" ;;
  *) ok "preflight clean run prints no fail line" ;;
esac

pf "$pf_wt" "$pf_wt"
[ "$pf_code" -eq 0 ] && ok "preflight works from inside a linked worktree" \
  || bad "preflight works from inside a linked worktree" "exit=$pf_code out=$pf_out"

git -C "$pf_repo" checkout -q -b feat/onprimary
pf "$pf_repo" "$pf_wt"
[ "$pf_code" -eq 1 ] && ok "preflight on a feature branch exits 1" \
  || bad "preflight on a feature branch exits 1" "exit=$pf_code out=$pf_out"
assert_contains "$pf_out" "fail primary" "preflight names the tangled primary checkout"
assert_contains "$pf_out" "(want main)" "preflight says which branch it wanted"

pf "$pf_repo" --base feat/onprimary "$pf_wt"
[ "$pf_code" -eq 0 ] && ok "preflight --base overrides the default branch" \
  || bad "preflight --base overrides the default branch" "exit=$pf_code out=$pf_out"
git -C "$pf_repo" checkout -q main

pf "$pf_repo" "$pf_repo"
[ "$pf_code" -eq 1 ] && ok "preflight rejects the primary as a worktree arg" \
  || bad "preflight rejects the primary as a worktree arg" "exit=$pf_code out=$pf_out"
assert_contains "$pf_out" "is the primary checkout" "preflight explains the primary-as-worktree failure"

pf "$pf_repo" "$pf_other"
[ "$pf_code" -eq 1 ] && ok "preflight rejects a worktree from another repo" \
  || bad "preflight rejects a worktree from another repo" "exit=$pf_code out=$pf_out"
assert_contains "$pf_out" "belongs to another repo" "preflight explains the foreign worktree"

pf "$pf_repo" "$tmpdir/no-such-worktree"
[ "$pf_code" -eq 1 ] && ok "preflight rejects a missing worktree" \
  || bad "preflight rejects a missing worktree" "exit=$pf_code out=$pf_out"
assert_contains "$pf_out" "does not exist" "preflight explains the missing worktree"

set +e
(cd "$pf_repo" && "$ROOT/bin/muxa" preflight --nope >/dev/null 2>&1)
pf_code=$?
set -e
[ "$pf_code" -eq 2 ] && ok "preflight unknown flag exits 2" \
  || bad "preflight unknown flag exits 2" "exit=$pf_code"

# --- jobs runtime map (br durable fields; TSV worker/worktree/branch; no tmux) ---
jobs_state="$tmpdir/state"
jobs_cli() {
  (cd "$pf_repo" && XDG_STATE_HOME="$jobs_state" "$ROOT/bin/muxa" jobs "$@")
}
jobs_code() {
  set +e
  jobs_cli "$@" >/dev/null 2>&1
  jobs_status=$?
  set -e
}
br_json() {
  (cd "$pf_repo" && br --actor testdriver --json --no-color "$@")
}
br_create() {
  br_json create "$@" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'
}
br_total() {
  br_json list --all | python3 -c 'import json,sys; d=json.load(sys.stdin); print(int(d.get("total") or 0))'
}
muxa_created_titles() {
  br_json list --all | python3 -c '
import json,sys
d=json.load(sys.stdin)
for i in d.get("issues") or []:
    if i.get("created_by") == "muxa":
        print(i.get("title") or "")
'
}

nobr_path=""
oldifs="$IFS"
IFS=:
for d in $PATH; do
  [ -n "$d" ] || continue
  [ -x "$d/br" ] && continue
  if [ -z "$nobr_path" ]; then
    nobr_path="$d"
  else
    nobr_path="$nobr_path:$d"
  fi
done
IFS="$oldifs"
set +e
jobs_nobr_err="$(cd "$pf_repo" && PATH="$nobr_path" XDG_STATE_HOME="$jobs_state" "$ROOT/bin/muxa" jobs list 2>&1)"
jobs_nobr_rc=$?
set -e
[ "$jobs_nobr_rc" -eq 2 ] && ok "jobs without br exits 2" \
  || bad "jobs without br exits 2" "exit=$jobs_nobr_rc err=$jobs_nobr_err"
assert_contains "$jobs_nobr_err" "br is required" "missing br names the hard requirement"
assert_contains "$jobs_nobr_err" "Dicklesworthstone/beads_rust" "missing br includes the install URL"
[ ! -d "$pf_repo/.beads" ] && ok "missing br does not init .beads" \
  || bad "missing br does not init .beads" "unexpected .beads under $pf_repo"

jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "(no jobs)" "empty runtime map lists nothing"
assert_contains "$jobs_out" "UPDATED" "jobs list header has UPDATED"
assert_contains "$jobs_out" "TITLE" "jobs list header has TITLE"
[ -d "$pf_repo/.beads" ] && ok "jobs list auto-inits .beads" \
  || bad "jobs list auto-inits .beads" "no .beads under $pf_repo after muxa jobs list"

api_id="$(br_create --title api -t task -l "kind:ship,delivery:pr" -d "wire the endpoint")"
br_create --title notes -t task -l "kind:research,delivery:local" >/dev/null
jobs_before="$(br_total)"
jobs_out="$(jobs_cli add api worker=bob branch=feat/api)"
assert_contains "$jobs_out" "added $api_id" "jobs add confirms the br id"
jobs_cli add notes >/dev/null
jobs_after="$(br_total)"
[ "$jobs_before" = "$jobs_after" ] && ok "jobs add does not create a br issue" \
  || bad "jobs add does not create a br issue" "before=$jobs_before after=$jobs_after"

jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "$api_id" "jobs list JOB column is the br id"
assert_contains "$jobs_out" "api" "jobs list shows the title"
assert_contains "$jobs_out" "open" "jobs add starts at open"
assert_contains "$jobs_out" "wire the endpoint" "jobs list shows the note"
assert_contains "$jobs_out" "notes" "jobs list shows the second job"
if printf '%s\n' "$jobs_out" | grep -Eq '[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z'; then
  ok "jobs list shows an updated timestamp"
else
  bad "jobs list shows an updated timestamp" "out=$jobs_out"
fi

jobs_cli set api status=running worktree="$pf_wt" >/dev/null
jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "running" "jobs set updates status"
assert_contains "$jobs_out" "$pf_wt" "jobs set stores the worktree"

jobs_out="$(jobs_cli done api pr=https://example.test/pr/1)"
assert_contains "$jobs_out" "done api" "jobs done confirms"
jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "done" "jobs done sets status=done"
assert_contains "$jobs_out" "https://example.test/pr/1" "jobs done stores the PR url"
assert_contains "$jobs_out" "notes" "unrelated job survives the update"

tsv="$(find "$jobs_state/muxa/jobs" -name '*.tsv' | head -1)"
if [ -n "$tsv" ]; then
  ok "jobs runtime ledger persists to XDG state dir"
else
  bad "jobs runtime ledger persists to XDG state dir" "no tsv under $jobs_state/muxa/jobs"
fi
if [ -n "$tsv" ]; then
  tsv_head="$(head -1 "$tsv")"
  case "$tsv_head" in
    $'#job\tworker\tworktree\tbranch') ok "runtime TSV has no durable columns" ;;
    *) bad "runtime TSV has no durable columns" "header=$tsv_head" ;;
  esac
  if grep -Eq $'\t(ship|research|open|running|done|https://example.test/pr/1)\t' "$tsv"; then
    bad "runtime TSV does not duplicate durable fields" "tsv=$(cat "$tsv")"
  else
    ok "runtime TSV does not duplicate durable fields"
  fi
  if grep -q "^${api_id}"$'\t' "$tsv"; then
    ok "runtime TSV is keyed by br id"
  else
    bad "runtime TSV is keyed by br id" "tsv=$(cat "$tsv")"
  fi
  if grep -q "^api"$'\t' "$tsv"; then
    bad "runtime TSV is not keyed by title" "tsv=$(cat "$tsv")"
  else
    ok "runtime TSV is not keyed by title"
  fi
fi

muxa_stubs="$(muxa_created_titles)"
[ -z "$muxa_stubs" ] && ok "jobs add leaves no created_by=muxa stub issues" \
  || bad "jobs add leaves no created_by=muxa stub issues" "titles=$muxa_stubs"

jobs_code set nope status=open
[ "$jobs_status" -eq 2 ] && ok "jobs set unknown job exits 2" \
  || bad "jobs set unknown job exits 2" "exit=$jobs_status"
jobs_code done nope
[ "$jobs_status" -eq 2 ] && ok "jobs done unknown job exits 2" \
  || bad "jobs done unknown job exits 2" "exit=$jobs_status"
jobs_code add api worker=bob
[ "$jobs_status" -eq 2 ] && ok "duplicate jobs add exits 2" \
  || bad "duplicate jobs add exits 2" "exit=$jobs_status"
jobs_before="$(br_total)"
jobs_code add missing-job worker=bob
[ "$jobs_status" -eq 2 ] && ok "jobs add unknown br id exits 2" \
  || bad "jobs add unknown br id exits 2" "exit=$jobs_status"
jobs_after="$(br_total)"
[ "$jobs_before" = "$jobs_after" ] && ok "unknown jobs add does not create a stub issue" \
  || bad "unknown jobs add does not create a stub issue" "before=$jobs_before after=$jobs_after"
jobs_code add other kind=nope delivery=pr
[ "$jobs_status" -eq 2 ] && ok "jobs add bad kind exits 2" \
  || bad "jobs add bad kind exits 2" "exit=$jobs_status"
jobs_code set api bogus=1
[ "$jobs_status" -eq 2 ] && ok "jobs set unknown key exits 2" \
  || bad "jobs set unknown key exits 2" "exit=$jobs_status"

(cd "$pf_repo" && br --actor testdriver create --title "stray-bead" -t task -d "not a muxa job" >/dev/null)
jobs_out="$(jobs_cli list)"
case "$jobs_out" in
  *stray-bead*) bad "jobs list is the runtime map, not the br backlog" "out=$jobs_out" ;;
  *) ok "jobs list is the runtime map, not the br backlog" ;;
esac

shared_id="$(br_create --title "shared-title" -t task -l "kind:research,delivery:local")"
jobs_before="$(br_total)"
jobs_cli add shared-title worker=pat >/dev/null
jobs_after="$(br_total)"
[ "$jobs_before" = "$jobs_after" ] && ok "jobs add attaches to an existing title without a duplicate issue" \
  || bad "jobs add attaches to an existing title without a duplicate issue" "before=$jobs_before after=$jobs_after"
jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "$shared_id" "jobs add by unique title records the br id"
assert_contains "$jobs_out" "pat" "jobs add by title stores the worker"

ws_id="$(br_create --title "command-post: foo" -t task -l "kind:ship,delivery:pr")"
jobs_before="$(br_total)"
jobs_out="$(jobs_cli add "$ws_id" worker=alice worktree="$pf_wt")"
jobs_after="$(br_total)"
[ "$jobs_before" = "$jobs_after" ] && ok "jobs add by id does not create a stub for a whitespace title" \
  || bad "jobs add by id does not create a stub for a whitespace title" "before=$jobs_before after=$jobs_after"
assert_contains "$jobs_out" "added $ws_id" "jobs add by id confirms"
jobs_code set "command-post: foo" worker=x
[ "$jobs_status" -eq 2 ] && ok "jobs set rejects a whitespace title" \
  || bad "jobs set rejects a whitespace title" "exit=$jobs_status"
jobs_cli set "$ws_id" worker=carol >/dev/null
jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "$ws_id" "jobs list shows the br id for a whitespace title"
assert_contains "$jobs_out" "command-post: foo" "jobs list shows the human title"
assert_contains "$jobs_out" "carol" "jobs set by br id updates runtime fields"

slug_id="$(br_create --title "ship the widget" --slug widget -t task -l "kind:ship,delivery:pr")"
jobs_cli add widget worker=slugger >/dev/null
jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "$slug_id" "jobs add by slug attaches the br id"
assert_contains "$jobs_out" "slugger" "jobs add by slug stores the worker"

bare_id="$(br_create --title bare -t task)"
jobs_cli add "$bare_id" kind=research delivery=local worker=kim >/dev/null
jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "research" "jobs add can stamp kind on an existing issue"
assert_contains "$jobs_out" "local" "jobs add can stamp delivery on an existing issue"
assert_contains "$jobs_out" "kim" "jobs add without prior kind still stores the worker"

jobs_cli set api status=open >/dev/null
jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "open" "jobs set reopens a closed job to open"
jobs_cli done api pr=https://example.test/pr/1 >/dev/null
jobs_cli set api status=running >/dev/null
jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "running" "jobs set reopens a closed job to running"

legacy_repo="$tmpdir/legacy-jobs"
legacy_state="$tmpdir/legacy-state"
git_init_repo "$legacy_repo"
legacy_key="$(cd "$legacy_repo" && pwd -P)"
legacy_slug="$(printf '%s' "$(basename "$legacy_key")" | tr -c 'A-Za-z0-9._-' '-' | sed 's/^\.*//')"
legacy_hash="$(printf '%s' "$legacy_key" | python3 -c 'import hashlib, sys; print(hashlib.sha1(sys.stdin.buffer.read()).hexdigest()[:8])')"
legacy_tsv="$legacy_state/muxa/jobs/${legacy_slug}-${legacy_hash}.tsv"
mkdir -p "$(dirname "$legacy_tsv")"
printf '%s\n' $'#job\tkind\tdelivery\tworker\tworktree\tbranch\tstatus\tpr\tnote\tupdated' >"$legacy_tsv"
printf '%s\n' $'oldjob\tship\tpr\t-\t-\t-\topen\t-\t-\t2026-01-01T00:00:00Z' >>"$legacy_tsv"
set +e
legacy_err="$(cd "$legacy_repo" && XDG_STATE_HOME="$legacy_state" "$ROOT/bin/muxa" jobs list 2>&1)"
legacy_rc=$?
set -e
[ "$legacy_rc" -eq 2 ] && ok "legacy open TSV refuses to start" \
  || bad "legacy open TSV refuses to start" "exit=$legacy_rc err=$legacy_err"
assert_contains "$legacy_err" "leftover open jobs" "legacy open TSV names the leftover file"

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
