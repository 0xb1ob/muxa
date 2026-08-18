#!/usr/bin/env bash
# Integration tests for muxa. Needs tmux + python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PATH="$ROOT/bin:$PATH"

for skill in muxa-parent muxa-worker muxa-orchestrator; do
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

# --- oversized inject refused; mail stays in new/ for peek ---
muxa_as "$alice_pane" state idle
big="$(python3 -c "print('O' * 9000)")"
oversized="$(muxa_as "$bob_pane" send alice "$big" 2>&1)"
assert_contains "$oversized" "queued bob → alice" "oversized idle send queues"
assert_contains "$oversized" "exceeds inject limit" "oversized idle send warns"
peek_big="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$peek_big" "OOOO" "peek recovers oversized idle mail"

deliver_big="$(muxa_as "$alice_pane" deliver alice 2>&1 || true)"
assert_contains "$deliver_big" "exceeds inject limit" "deliver refuses oversized"
peek_big2="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$peek_big2" "OOOO" "peek recovers after failed deliver"

muxa_as "$alice_pane" state busy
kick_big="$(muxa_as "$bob_pane" send alice "$big" 2>&1)"
assert_contains "$kick_big" "waiting for idle" "oversized busy send spawns kick_wait"
muxa_as "$alice_pane" state idle
sleep 0.6
peek_big3="$(muxa_as "$bob_pane" peek alice)"
assert_contains "$peek_big3" "OOOO" "peek recovers after kick_wait inject failure"

# drain oversized mail before hook tests (hooks tolerate large bodies)
muxa_as "$alice_pane" register --name alice --kind claude --deliver hook >/dev/null
muxa_as "$alice_pane" hook stop --format claude >/dev/null || true

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
from_pwd="$(cd "$spawn_wt" && muxa_as "$bob_pane" spawn --name frompwd -- sleep 3600)"
assert_contains "$from_pwd" "cwd=$spawn_wt" "spawn stdout includes process PWD"
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

# --- CLI session id mapping ---
printf '%s' '{"session_id":"cli-sess-123"}' | muxa_as "$alice_pane" hook session-start --kind generic
sid="$(tmux -L "$SOCK" display-message -p -t "$alice_pane" '#{@muxa_session}')"
[ "$sid" = "cli-sess-123" ] && ok "session-start stores @muxa_session" \
  || bad "session-start stores @muxa_session" "got: $sid"

who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "cli-sess-123" "who shows session id"

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

# --- jobs backlog (no tmux, survives restarts) ---
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

jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "(no jobs)" "empty backlog lists nothing"
assert_contains "$jobs_out" "UPDATED" "jobs list header has UPDATED"

jobs_out="$(jobs_cli add api kind=ship delivery=pr worker=bob branch=feat/api note='wire the endpoint')"
assert_contains "$jobs_out" "added api" "jobs add confirms"
jobs_cli add notes kind=research delivery=local >/dev/null

jobs_out="$(jobs_cli list)"
assert_contains "$jobs_out" "api" "jobs list shows added job"
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
[ -n "$tsv" ] && ok "jobs backlog persists to XDG state dir" \
  || bad "jobs backlog persists to XDG state dir" "no tsv under $jobs_state/muxa/jobs"

jobs_code set nope status=open
[ "$jobs_status" -eq 2 ] && ok "jobs set unknown job exits 2" \
  || bad "jobs set unknown job exits 2" "exit=$jobs_status"
jobs_code done nope
[ "$jobs_status" -eq 2 ] && ok "jobs done unknown job exits 2" \
  || bad "jobs done unknown job exits 2" "exit=$jobs_status"
jobs_code add api kind=ship delivery=pr
[ "$jobs_status" -eq 2 ] && ok "duplicate jobs add exits 2" \
  || bad "duplicate jobs add exits 2" "exit=$jobs_status"
jobs_code add other kind=nope delivery=pr
[ "$jobs_status" -eq 2 ] && ok "jobs add bad kind exits 2" \
  || bad "jobs add bad kind exits 2" "exit=$jobs_status"
jobs_code add other kind=ship
[ "$jobs_status" -eq 2 ] && ok "jobs add without delivery exits 2" \
  || bad "jobs add without delivery exits 2" "exit=$jobs_status"
jobs_code set api bogus=1
[ "$jobs_status" -eq 2 ] && ok "jobs set unknown key exits 2" \
  || bad "jobs set unknown key exits 2" "exit=$jobs_status"

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
