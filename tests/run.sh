#!/usr/bin/env bash
# Integration tests for muxa. Needs tmux + python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PATH="$ROOT/bin:$PATH"
SOCK="muxatest-$$"
export MUXA_TMUX_SOCKET="$SOCK"
export MUXA_ENTER_DELAY=0.05
unset TMUX || true

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

tmux -L "$SOCK" new-session -d -s muxa -n alice "cat > '$alice_out'"
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
assert_contains "$peek2" "(empty)" "mailbox empty after drain"

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

# --- empty stop is silent ---
muxa_as "$alice_pane" state busy
empty="$(muxa_as "$alice_pane" hook stop --format claude)"
[ -z "$empty" ] && ok "empty stop prints nothing" || bad "empty stop prints nothing" "got: $empty"

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

# --- spawn ---
spawned="$(muxa_as "$bob_pane" spawn --name spawned -- sleep 3600)"
assert_contains "$spawned" "spawned spawned" "spawn creates child pane"
assert_contains "$spawned" "parent=bob" "spawn records parent"
sp_par="$(muxa_as "$(tmux -L "$SOCK" list-panes -t muxa:spawned -F '#{pane_id}')" parent)"
assert_contains "$sp_par" "bob" "spawned pane parent option"

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
