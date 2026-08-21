#!/usr/bin/env bash
# Integration tests for muxa. Needs tmux. python3 is not required.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PATH="$ROOT/bin:$PATH"
GO="${GO:-/usr/local/go/bin/go}"
command -v "$GO" >/dev/null 2>&1 || GO="$(command -v go)"
ldflags=()
case "$(uname -s)" in
  Darwin) ldflags=(-ldflags=-linkmode=external) ;;
esac
# darwin 25+ aborts a test binary without LC_UUID; only the external linker emits it.
"$GO" test "${ldflags[@]}" "$ROOT/broker"
"$GO" build "${ldflags[@]}" -o "$ROOT/bin/muxa-broker" "$ROOT/broker"
if [ "$(uname -s)" = Darwin ]; then
  xattr -c "$ROOT/bin/muxa-broker" 2>/dev/null || true
  codesign -s - --force --timestamp=none "$ROOT/bin/muxa-broker" 2>/dev/null || true
fi

for skill in muxa-parent muxa-worker; do
  src="$ROOT/skills/$skill/SKILL.md"
  [ -f "$src" ] || { echo "missing $src" >&2; exit 1; }
  if grep -E 'tmux[[:space:]]+(capture-pane|kill-pane)' "$src" >/dev/null; then
    echo "skill $src still invokes tmux capture-pane or kill-pane" >&2
    exit 1
  fi
done
if grep -E 'unbriefed|Brief immediately with `muxa send`' "$ROOT/skills/muxa-parent/SKILL.md" >/dev/null; then
  echo "muxa-parent still carries spawn-then-brief choreography" >&2
  exit 1
fi
SOCK="muxatest-$$"
export MUXA_TMUX_SOCKET="$SOCK"
export MUXA_ENTER_DELAY=0.05
unset TMUX MUXA_NAME MUXA_PARENT MUXA_ID MUXA_BIN MUXA_HOME || true

pass=0
fail=0
tmpdir="$(mktemp -d /tmp/muxa-test.XXXXXX)"
alice_out="$tmpdir/alice.out"
export MUXA_BROKER_DIR="$tmpdir/broker"
export MUXA_BROKER_SOCK="$tmpdir/broker/broker.sock"
export MUXA_BROKER_PID="$tmpdir/broker/broker.pid"
export MUXA_BROKER_BIN="$ROOT/bin/muxa-broker"
export XDG_RUNTIME_DIR="$tmpdir/run"
mkdir -p "$tmpdir/run" "$tmpdir/broker"
case "$MUXA_BROKER_DIR" in
  "$tmpdir"/*) ;;
  *) echo "tests/run.sh: MUXA_BROKER_DIR must be under $tmpdir" >&2; exit 1 ;;
esac

cleanup() {
  # Fail closed: never SIGTERM a pidfile outside this run's tmpdir
  # (that would be the operator daemon on /tmp/muxa-UID/...).
  case "${MUXA_BROKER_PID:-}" in
    "$tmpdir"/*)
      if [ -f "$MUXA_BROKER_PID" ]; then
        kill "$(cat "$MUXA_BROKER_PID")" 2>/dev/null || true
      fi
      ;;
  esac
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

same_dir() {
  local a b
  a="$(cd "$1" && pwd -P)" || return 1
  b="$(cd "$2" && pwd -P)" || return 1
  [ "$a" = "$b" ]
}

who_json_get() {
  printf '%s' "$1" | muxa-broker json-get "$2" "$3"
}

who_json_is_null() {
  local rc
  set +e
  printf '%s' "$1" | muxa-broker json-get "$2" "$3" >/dev/null
  rc=$?
  set -e
  [ "$rc" -eq 3 ]
}

json_get() {
  printf '%s' "$1" | muxa-broker json-get "$2"
}

pyc="$(grep -c python3 "$ROOT/bin/muxa" || true)"
[ "$pyc" = "0" ] && ok "bin/muxa has no python3" \
  || bad "bin/muxa has no python3" "count=$pyc"

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

pane_exists() {
  tmux -L "$SOCK" list-panes -a -F '#{pane_id}' 2>/dev/null \
    | awk -v p="$1" '$1==p { found=1 } END { exit found ? 0 : 1 }'
}

# --- register + who ---
reg_b="$(muxa_as "$bob_pane" register --name bob --kind generic)"
assert_contains "$reg_b" "registered bob" "register bob"

reg_a="$(muxa_as "$alice_pane" register --name alice --kind generic --parent bob)"
assert_contains "$reg_a" "registered alice" "register alice"
assert_contains "$reg_a" "parent=bob" "alice is bob's child"

who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "alice" "who lists alice"
assert_contains "$who" "bob" "who lists bob"

me="$(muxa_as "$bob_pane" whoami)"
assert_contains "$me" "bob" "whoami is bob"

dup="$(muxa_as "$bob_pane" register --name alice --kind generic 2>&1 || true)"
assert_contains "$dup" "already registered" "duplicate name refused"

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

muxa_as "$carol_pane" register --name carol --kind generic --parent bob >/dev/null
muxa_as "$dave_pane" register --name dave --kind generic --parent bob >/dev/null

who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "carol" "who lists child carol"
par="$(muxa_as "$carol_pane" parent)"
assert_contains "$par" "bob" "carol parent is bob"
kids="$(muxa_as "$bob_pane" children)"
assert_contains "$kids" "carol" "bob children include carol"
assert_contains "$kids" "dave" "bob children include dave"

sent="$(muxa_as "$bob_pane" send carol 'from-parent')"
assert_contains "$sent" "queued bob → carol" "parent → child allowed"
assert_contains "$sent" "broker" "parent → child uses broker"

sent="$(muxa_as "$carol_pane" send bob 'from-child')"
assert_contains "$sent" "queued carol → bob" "child → parent allowed"
assert_contains "$sent" "broker" "child → parent uses broker"

sib="$(muxa_as "$carol_pane" send dave 'nope' 2>&1 || true)"
assert_contains "$sib" "forbidden" "sibling send refused"

sib2="$(muxa_as "$alice_pane" send carol 'nope' 2>&1 || true)"
assert_contains "$sib2" "forbidden" "sibling alice → carol refused"

# --- send --all dedupes duplicate roster names ---
saved_dave_name="$(tmux -L "$SOCK" display-message -p -t "$dave_pane" '#{@muxa_name}')"
tmux -L "$SOCK" set-option -p -t "$dave_pane" @muxa_name carol
marker="send-all-dedupe-$$"
all_out="$(muxa_as "$bob_pane" send --no-reply --all "$marker")"
count="$(printf '%s\n' "$all_out" | grep -c "queued bob → carol" || true)"
tmux -L "$SOCK" set-option -p -t "$dave_pane" @muxa_name "$saved_dave_name"
[ "$count" -eq 1 ] && ok "send --all dedupes duplicate roster names" \
  || bad "send --all dedupes duplicate roster names" "expected 1 enqueue, count=$count out=$all_out"

tmux -L "$SOCK" new-window -t muxa -n eve "exec sleep 3600"
sleep 0.2
eve_pane="$(tmux -L "$SOCK" list-panes -t muxa:eve -F '#{pane_id}')"
muxa_as "$eve_pane" register --name eve --kind generic >/dev/null

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

split_sp="$(muxa_as "$bob_pane" spawn --name splitkid -- sleep 3600)"
assert_contains "$split_sp" "spawned splitkid" "spawn splits into parent window"
split_pane="$(printf '%s\n' "$split_sp" | spawn_pane_id)"
split_win="$(tmux -L "$SOCK" display-message -t "$split_pane" -p '#{session_name}:#{window_index}')"
[ "$split_win" = "$bob_win" ] && ok "spawn stays in parent window" \
  || bad "spawn stays in parent window" "parent=$bob_win child=$split_win"
n_split="$(tmux -L "$SOCK" list-panes -t "$bob_win" -F '#{pane_id}' | awk 'END { print NR }')"
[ "$n_split" -eq $((expect + 1)) ] && ok "spawn adds a pane in the grid" \
  || bad "spawn adds a pane in the grid" "expected $((expect + 1)) panes, got $n_split"

# Dedicated wide window: 4 default spawns must be 2D (not a single row/column).
tmux -L "$SOCK" new-window -t muxa -n gridhost "exec sleep 3600"
sleep 0.2
grid_pane="$(tmux -L "$SOCK" list-panes -t muxa:gridhost -F '#{pane_id}')"
tmux -L "$SOCK" resize-window -t muxa:gridhost -x 200 -y 40 2>/dev/null || true
muxa_as "$grid_pane" register --name gridhost --kind generic >/dev/null
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
muxa_as "$occroot_pane" register --name occroot --kind generic >/dev/null
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
muxa_as "$occghost_pane" register --name occghost --kind cursor --parent bob >/dev/null
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

# --- kind detection: cursor-agent node vs claude SessionStart ---
who_kind_for() {
  local who="$1" name="$2" k
  k="$(printf '%s\n' "$who" | awk -v n="$name" '$1==n { print substr($0, 87, 8); exit }')"
  k="${k#"${k%%[![:space:]]*}"}"
  k="${k%"${k##*[![:space:]]}"}"
  printf '%s' "$k"
}

who_state_for() {
  local who="$1" name="$2" s
  s="$(printf '%s\n' "$who" | awk -v n="$name" '$1==n { print substr($0, 96, 8); exit }')"
  s="${s#"${s%%[![:space:]]*}"}"
  s="${s%"${s##*[![:space:]]}"}"
  printf '%s' "$s"
}

assert_who_state() {
  local who="$1" name="$2" want="$3" label="$4"
  local got
  got="$(who_state_for "$who" "$name")"
  [ "$got" = "$want" ] && ok "$label" || bad "$label" "name=$name want=$want got=$got"
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
muxa_as "$kindnode_pane" register --name kind-node >/dev/null
kindnode_kind="$(tmux -L "$SOCK" display-message -t "$kindnode_pane" -p '#{@muxa_kind}')"
[ "$kindnode_kind" = "cursor" ] && ok "node cursor-agent path registers as cursor" \
  || bad "node cursor-agent path registers as cursor" "got=$kindnode_kind"

tmux -L "$SOCK" new-window -t muxa -n kindclaude \
  "$kind_fakebin/claude"
sleep 0.2
kindclaude_pane="$(tmux -L "$SOCK" list-panes -t muxa:kindclaude -F '#{pane_id}' | head -1)"
muxa_as "$kindclaude_pane" hook session-start --kind claude >/dev/null
kindclaude_kind="$(tmux -L "$SOCK" display-message -t "$kindclaude_pane" -p '#{@muxa_kind}')"
[ "$kindclaude_kind" = "claude" ] && ok "session-start registers an unregistered pane" \
  || bad "session-start registers an unregistered pane" "got=$kindclaude_kind"
kindclaude_name="$(tmux -L "$SOCK" display-message -t "$kindclaude_pane" -p '#{@muxa_name}')"
[ -n "$kindclaude_name" ] && ok "session-start assigns a name" \
  || bad "session-start assigns a name" "name empty"

# Spawned cursor child must not flip when a leftover session-start fires.
spawn_agent="$(muxa_as "$bob_pane" spawn --name kind-agent -- "$kind_fakebin/agent")"
assert_contains "$spawn_agent" "kind=cursor" "spawn agent infers cursor kind"
kind_agent_pane="$(printf '%s\n' "$spawn_agent" | spawn_pane_id)"
muxa_as "$kind_agent_pane" hook session-start --kind claude >/dev/null
kind_agent_kind="$(tmux -L "$SOCK" display-message -t "$kind_agent_pane" -p '#{@muxa_kind}')"
[ "$kind_agent_kind" = "cursor" ] && ok "spawned cursor survives leftover session-start" \
  || bad "spawned cursor survives leftover session-start" "got=$kind_agent_kind"
who_kind="$(muxa_as "$bob_pane" who)"
[ "$(who_kind_for "$who_kind" "kind-agent")" = "cursor" ] \
  && ok "who KIND matches cursor foreground after leftover hook" \
  || bad "who KIND matches cursor foreground after leftover hook" \
     "got=$(who_kind_for "$who_kind" "kind-agent")"

# Cursor IDE: pane_pid stays a shell; foreground child is node whose argv
# contains cursor-agent. pane_pid argv must not mention cursor-agent, or
# looking only at #{pane_pid} would hide the regression.
printf '%s\n' "$kind_fakebin/cursor-agent/node" > "$kind_fakebin/ide-child-path"
cat > "$kind_fakebin/ide-shell" <<'EOF'
#!/bin/sh
dir=$(dirname "$0")
child=$(cat "$dir/ide-child-path")
"$child"
while sleep 3600; do :; done
EOF
chmod +x "$kind_fakebin/ide-shell"

wait_cursor_child() {
  local pane="$1" pid tries=0
  pid="$(tmux -L "$SOCK" display-message -t "$pane" -p '#{pane_pid}')"
  while [ "$tries" -lt 50 ]; do
    if ps -ax -o pid=,ppid=,args= 2>/dev/null | awk -v root="$pid" '
      BEGIN { p[root] = 1 }
      {
        if (p[$2]) {
          p[$1] = 1
          if ($0 ~ /cursor-agent/) found = 1
        }
      }
      END { exit found ? 0 : 1 }
    '; then
      return 0
    fi
    sleep 0.1
    tries=$((tries + 1))
  done
  return 1
}

tmux -L "$SOCK" new-window -t muxa -n kindide "$kind_fakebin/ide-shell"
sleep 0.2
kindide_pane="$(tmux -L "$SOCK" list-panes -t muxa:kindide -F '#{pane_id}' | head -1)"
wait_cursor_child "$kindide_pane" \
  && ok "Cursor IDE stub child is running before SessionStart" \
  || bad "Cursor IDE stub child is running before SessionStart" "no cursor-agent child of pane_pid"
kindide_pid="$(tmux -L "$SOCK" display-message -t "$kindide_pane" -p '#{pane_pid}')"
kindide_pid_args="$(ps -p "$kindide_pid" -o args= 2>/dev/null | head -1)"
case "$kindide_pid_args" in
  *cursor-agent*)
    bad "ide-shell pane_pid argv has no cursor-agent" "args=$kindide_pid_args"
    ;;
  *) ok "ide-shell pane_pid argv has no cursor-agent" ;;
esac
kindide_cmd="$(tmux -L "$SOCK" display-message -t "$kindide_pane" -p '#{pane_current_command}')"
case "$kindide_cmd" in
  node|sh|ide-shell|sleep)
    ok "Cursor IDE stub foreground is $kindide_cmd"
    ;;
  *)
    ok "Cursor IDE stub foreground is $kindide_cmd (tree still classifies)"
    ;;
esac
muxa_as "$kindide_pane" hook session-start --kind claude >/dev/null
kindide_kind="$(tmux -L "$SOCK" display-message -t "$kindide_pane" -p '#{@muxa_kind}')"
[ "$kindide_kind" = "cursor" ] \
  && ok "cmd=node Cursor IDE survives claude SessionStart" \
  || bad "cmd=node Cursor IDE survives claude SessionStart" "got=$kindide_kind"
kindide_name="$(tmux -L "$SOCK" display-message -t "$kindide_pane" -p '#{@muxa_name}')"
who_json="$(muxa_as "$bob_pane" who --json)"
kindide_json_kind="$(who_json_get "$who_json" "$kindide_name" kind)"
[ "$kindide_json_kind" = "cursor" ] \
  && ok "who --json kind stays cursor after claude SessionStart" \
  || bad "who --json kind stays cursor after claude SessionStart" \
     "got=$kindide_json_kind name=$kindide_name"
muxa_as "$kindide_pane" hook session-start --kind claude >/dev/null
kindide_kind2="$(tmux -L "$SOCK" display-message -t "$kindide_pane" -p '#{@muxa_kind}')"
[ "$kindide_kind2" = "cursor" ] && [ "$kindide_name" = "$(tmux -L "$SOCK" display-message -t "$kindide_pane" -p '#{@muxa_name}')" ] \
  && ok "second claude SessionStart does not rename or flip Cursor IDE" \
  || bad "second claude SessionStart does not rename or flip Cursor IDE" \
     "kind=$kindide_kind2"

tmux -L "$SOCK" new-window -t muxa -n kindide2 "$kind_fakebin/ide-shell"
sleep 0.2
kindide2_pane="$(tmux -L "$SOCK" list-panes -t muxa:kindide2 -F '#{pane_id}' | head -1)"
wait_cursor_child "$kindide2_pane" \
  && ok "kind-ide2 child is running before register" \
  || bad "kind-ide2 child is running before register" "no cursor-agent child of pane_pid"
muxa_as "$kindide2_pane" register --name kind-ide2 >/dev/null
kindide2_kind="$(tmux -L "$SOCK" display-message -t "$kindide2_pane" -p '#{@muxa_kind}')"
[ "$kindide2_kind" = "cursor" ] \
  && ok "register detects cursor from node child under a shell pane_pid" \
  || bad "register detects cursor from node child under a shell pane_pid" \
     "got=$kindide2_kind"
muxa_as "$kindide2_pane" hook session-start --kind claude >/dev/null
kindide2_kind="$(tmux -L "$SOCK" display-message -t "$kindide2_pane" -p '#{@muxa_kind}')"
[ "$kindide2_kind" = "cursor" ] \
  && ok "registered Cursor IDE kind stays cursor after claude SessionStart" \
  || bad "registered Cursor IDE kind stays cursor after claude SessionStart" \
     "got=$kindide2_kind"

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
muxa_as "$kindcomp_pane" register --name kind-comp >/dev/null
kindcomp_kind="$(tmux -L "$SOCK" display-message -t "$kindcomp_pane" -p '#{@muxa_kind}')"
[ "$kindcomp_kind" = "generic" ] && ok "component path does not classify as pi" \
  || bad "component path does not classify as pi" "got=$kindcomp_kind"

tmux -L "$SOCK" new-window -t muxa -n kindcompose \
  "$kind_fakebin/docker-compose/run"
sleep 0.2
kindcompose_pane="$(tmux -L "$SOCK" list-panes -t muxa:kindcompose -F '#{pane_id}' | head -1)"
muxa_as "$kindcompose_pane" register --name kind-compose >/dev/null
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
muxa_as "$kindomp_pane" hook session-start --kind pi >/dev/null
kindomp_kind="$(tmux -L "$SOCK" display-message -t "$kindomp_pane" -p '#{@muxa_kind}')"
[ "$kindomp_kind" = "pi" ] && ok "session-start --kind pi registers as pi" \
  || bad "session-start --kind pi registers as pi" "got=$kindomp_kind"

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
muxa_as "$kindclaudeproj_pane" register --name kind-claudeproj >/dev/null
kindclaudeproj_kind="$(tmux -L "$SOCK" display-message -t "$kindclaudeproj_pane" -p '#{@muxa_kind}')"
[ "$kindclaudeproj_kind" = "cursor" ] \
  && ok "claude-projects cursor path is not misclassified as claude" \
  || bad "claude-projects cursor path is not misclassified as claude" \
     "got=$kindclaudeproj_kind"

# Unknown hook events error; STATE comes from the broker.
set +e
busy_err="$(muxa_as "$alice_pane" hook busy 2>&1)"
busy_rc=$?
set -e
[ "$busy_rc" -ne 0 ] && ok "unknown hook event errors" \
  || bad "unknown hook event errors" "exit=$busy_rc out=$busy_err"
assert_contains "$busy_err" "unknown event" "hook busy is rejected"
who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "alice" "unknown hook does not unregister"
assert_who_state "$who" "alice" "idle" "who STATE is idle when the pane is not drawing"
got_sess="$(muxa_as "$alice_pane" session)"
[ -z "$got_sess" ] && ok "muxa session is empty (CLI session id is not tracked)" \
  || bad "muxa session is empty (CLI session id is not tracked)" "got: $got_sess"

projdir="$tmpdir/acme-widgets"
mkdir -p "$projdir"
tmux -L "$SOCK" new-window -d -t muxa -n proj -c "$projdir" "exec sleep 3600"
sleep 0.2
proj_pane="$(tmux -L "$SOCK" list-panes -t muxa:proj -F '#{pane_id}' | head -1)"
muxa_as "$proj_pane" register --name projagent --kind generic >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "$projdir" "who shows pane cwd"
assert_contains "$who" "CWD" "who header has CWD"
assert_contains "$who" "STATUS" "who header has STATUS"

# --- who STATUS: ghost vs live ---
tmux -L "$SOCK" new-window -t muxa -n zshghost "exec zsh"
sleep 0.2
zsh_pane="$(tmux -L "$SOCK" list-panes -t muxa:zshghost -F '#{pane_id}')"
muxa_as "$zsh_pane" register --name zsh-cursor --kind cursor >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_who_status "$who" "zsh-cursor" "ghost" "cursor+shell is ghost"

tmux -L "$SOCK" new-window -t muxa -n zshpi "exec zsh"
sleep 0.2
zshpi_pane="$(tmux -L "$SOCK" list-panes -t muxa:zshpi -F '#{pane_id}')"
muxa_as "$zshpi_pane" register --name zsh-pi --kind pi >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_who_status "$who" "zsh-pi" "ghost" "pi+shell is ghost"

tmux -L "$SOCK" new-window -t muxa -n zshgen "exec zsh"
sleep 0.2
zshgen_pane="$(tmux -L "$SOCK" list-panes -t muxa:zshgen -F '#{pane_id}')"
muxa_as "$zshgen_pane" register --name zsh-generic --kind generic >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_who_status "$who" "zsh-generic" "live" "generic+shell is live"

gone_dir="$tmpdir/gone-cwd"
mkdir -p "$gone_dir"
tmux -L "$SOCK" new-window -t muxa -n badcwd -c "$gone_dir" "exec sleep 3600"
sleep 0.2
badcwd_pane="$(tmux -L "$SOCK" list-panes -t muxa:badcwd -F '#{pane_id}')"
rm -rf "$gone_dir"
muxa_as "$badcwd_pane" register --name bad-cwd --kind generic >/dev/null
who="$(muxa_as "$bob_pane" who)"
assert_who_status "$who" "bad-cwd" "ghost" "missing pane cwd is ghost"

# --- who --json, tail, send --json (machine-readable surface) ---
human_who="$(muxa_as "$bob_pane" who)"
case "$human_who" in
  NAME*) ok "who default still starts with NAME header" ;;
  *) bad "who default still starts with NAME header" "got: $human_who" ;;
esac
case "$human_who" in
  \[*) bad "who default is not JSON" "got: $human_who" ;;
  *) ok "who default is not JSON" ;;
esac
who_hdr="$(printf '%s\n' "$human_who" | head -1)"
case "$who_hdr" in
  *DELIVER*) bad "who header has no DELIVER column" "got: $who_hdr" ;;
  *) ok "who header has no DELIVER column" ;;
esac
assert_contains "$who_hdr" "STATE" "who header has STATE"
assert_contains "$who_hdr" "STATUS" "who header has STATUS"

whoj="$(muxa_as "$bob_pane" who --json)"
if [ "$(printf '%s' "$whoj" | muxa-broker json-type)" = "array" ] \
  && printf '%s' "$whoj" | muxa-broker json-keys name id parent kind state pane session cwd status; then
  ok "who --json is an array of roster objects"
else
  bad "who --json is an array of roster objects" "got: $whoj"
fi

[ "$(who_json_get "$whoj" alice parent)" = "bob" ] && ok "who --json alice.parent is bob" \
  || bad "who --json alice.parent is bob" "got: $(who_json_get "$whoj" alice parent 2>/dev/null || true)"
who_json_is_null "$whoj" bob parent && ok "who --json bob.parent is null" \
  || bad "who --json bob.parent is null" "bob parent not null"
proj_json_cwd="$(who_json_get "$whoj" projagent cwd)"
if same_dir "$proj_json_cwd" "$projdir"; then
  ok "who --json cwd is a field not a column"
else
  bad "who --json cwd is a field not a column" "got: $proj_json_cwd want: $projdir"
fi
[ "$(who_json_get "$whoj" zsh-cursor status)" = "ghost" ] && ok "who --json status is ghost for cursor+shell" \
  || bad "who --json status is ghost for cursor+shell" "got: $(who_json_get "$whoj" zsh-cursor status 2>/dev/null || true)"
[ "$(who_json_get "$whoj" projagent status)" = "live" ] && ok "who --json status is live for generic sleep" \
  || bad "who --json status is live for generic sleep" "got: $(who_json_get "$whoj" projagent status 2>/dev/null || true)"
[ "$(who_json_get "$whoj" alice state)" = "idle" ] && ok "who --json state is idle when the pane is not drawing" \
  || bad "who --json state is idle when the pane is not drawing" "got: $(who_json_get "$whoj" alice state 2>/dev/null || true)"
who_json_is_null "$whoj" alice session && ok "who --json session is always null" \
  || bad "who --json session is always null" "alice session not null"

shape_bad=""
while IFS= read -r n; do
  [ -n "$n" ] || continue
  st="$(who_json_get "$whoj" "$n" state)"
  case "$st" in
    idle|busy) ;;
    *) shape_bad="${shape_bad}${n}.state=${st}; " ;;
  esac
  [ "$st" = "blocked" ] && shape_bad="${shape_bad}${n}.state=blocked; "
  sta="$(who_json_get "$whoj" "$n" status)"
  case "$sta" in
    live|drawing|ghost) ;;
    *) shape_bad="${shape_bad}${n}.status=${sta}; " ;;
  esac
  if ! who_json_is_null "$whoj" "$n" session; then
    shape_bad="${shape_bad}${n}.session!=null; "
  fi
done <<EOF
$(printf '%s' "$whoj" | muxa-broker json-values name)
EOF
[ -z "$shape_bad" ] && ok "who --json fail-closed: state idle|busy, status live|drawing|ghost, session null" \
  || bad "who --json fail-closed: state idle|busy, status live|drawing|ghost, session null" "$shape_bad"

occ=""
while IFS= read -r n; do
  [ -n "$n" ] || continue
  [ "$(who_json_get "$whoj" "$n" status)" = "live" ] || continue
  cwd="$(who_json_get "$whoj" "$n" cwd)"
  if [ -d "$cwd" ] && same_dir "$cwd" "$projdir"; then
    if [ -n "$occ" ]; then occ="$occ,$n"; else occ="$n"; fi
  fi
done <<EOF
$(printf '%s' "$whoj" | muxa-broker json-values name)
EOF
[ "$occ" = "projagent" ] && ok "who --json occupancy consumes cwd+status with no awk" \
  || bad "who --json occupancy consumes cwd+status with no awk" "got: $occ"

esc_cwd="$tmpdir/acme-\"quote\"\\slash"
mkdir -p "$esc_cwd"
tmux -L "$SOCK" new-window -t muxa -n esccwd -c "$esc_cwd" "exec sleep 3600"
sleep 0.2
esc_pane="$(tmux -L "$SOCK" list-panes -t muxa:esccwd -F '#{pane_id}')"
muxa_as "$esc_pane" register --name jsonesc --kind generic --parent bob >/dev/null
whoj_esc="$(muxa_as "$bob_pane" who --json)"
esc_got="$(who_json_get "$whoj_esc" jsonesc cwd)"
if same_dir "$esc_got" "$esc_cwd"; then
  ok "who --json cwd round-trips quote and backslash"
else
  bad "who --json cwd round-trips quote and backslash" "got: $esc_got want: $esc_cwd"
fi

jsent="$(muxa_as "$bob_pane" send --json carol json-body)"
if printf '%s' "$jsent" | muxa-broker json-keys id pane from to \
  && [ "$(json_get "$jsent" from)" = "bob" ] \
  && [ "$(json_get "$jsent" to)" = "carol" ]; then
  pane_js="$(json_get "$jsent" pane)"
  id_js="$(json_get "$jsent" id)"
  case "$pane_js" in
    %*)
      if [ -n "$id_js" ]; then
        ok "send --json is id+pane+from+to"
      else
        bad "send --json is id+pane+from+to" "got: $jsent"
      fi
      ;;
    *) bad "send --json is id+pane+from+to" "got: $jsent" ;;
  esac
else
  bad "send --json is id+pane+from+to" "got: $jsent"
fi
carol_pane_got="$(json_get "$jsent" pane)"
[ "$carol_pane_got" = "$carol_pane" ] && ok "send --json pane matches roster" \
  || bad "send --json pane matches roster" "want=$carol_pane got=$carol_pane_got"
human_send="$(muxa_as "$bob_pane" send carol still-human)"
assert_contains "$human_send" "queued bob → carol" "send without --json stays human"

tok_esc="ESCJSON_$$"
body="quote=\"hi\" slash=\\ and more"$'\n'"line-two-${tok_esc}"
esc_loop='while true; do printf "ready> "; read -r _ || break; done'
esc_spawn="$(muxa_as "$bob_pane" spawn --window --name escrecv -- bash -c "$esc_loop")"
esc_recv_pane="$(printf '%s\n' "$esc_spawn" | awk '{ sub(/^pane=/,"",$6); print $6 }')"
sleep 0.4
jsent_esc="$(muxa_as "$bob_pane" send --json escrecv "$body")"
if printf '%s' "$jsent_esc" | muxa-broker json-keys id pane from to \
  && [ "$(json_get "$jsent_esc" to)" = "escrecv" ]; then
  ok "send --json stays valid when the body has quote, backslash, newline"
else
  bad "send --json stays valid when the body has quote, backslash, newline" "got: $jsent_esc"
fi
esc_cap=""
i=0
while [ "$i" -lt 40 ]; do
  esc_cap="$(tmux -L "$SOCK" capture-pane -p -t "$esc_recv_pane" 2>/dev/null || true)"
  case "$esc_cap" in
    *"$tok_esc"*) break ;;
  esac
  sleep 0.15
  i=$((i + 1))
done
case "$esc_cap" in
  *quote=\"hi\"*)
    case "$esc_cap" in
      *"slash=\\"*)
        case "$esc_cap" in
          *"line-two-${tok_esc}"*) ok "enqueue delivers body with quote, backslash, newline" ;;
          *) bad "enqueue delivers body with quote, backslash, newline" "cap: $esc_cap" ;;
        esac
        ;;
      *) bad "enqueue delivers body with quote, backslash, newline" "cap: $esc_cap" ;;
    esac
    ;;
  *) bad "enqueue delivers body with quote, backslash, newline" "cap: $esc_cap" ;;
esac

allj="$(muxa_as "$bob_pane" send --json --all json-all-body)"
all_tos="$(printf '%s' "$allj" | muxa-broker json-values to)"
all_froms="$(printf '%s' "$allj" | muxa-broker json-values from)"
if [ "$(printf '%s' "$allj" | muxa-broker json-type)" = "array" ] \
  && printf '%s' "$allj" | muxa-broker json-keys id pane from to; then
  case "$all_tos" in
    *carol*)
      case "$all_tos" in
        *alice*)
          case "$all_froms" in
            *bob*) ok "send --all --json is an array of enqueues" ;;
            *) bad "send --all --json is an array of enqueues" "from=$all_froms got: $allj" ;;
          esac
          ;;
        *) bad "send --all --json is an array of enqueues" "got: $allj" ;;
      esac
      ;;
    *) bad "send --all --json is an array of enqueues" "got: $allj" ;;
  esac
else
  bad "send --all --json is an array of enqueues" "got: $allj"
fi

nonej="$(muxa_as "$eve_pane" send --json --all json-none)"
[ "$nonej" = "[]" ] && ok "send --all --json with no peers is []" \
  || bad "send --all --json with no peers is []" "got: $nonej"

tok_disp="DISPJ_$$"
disp_loop='while true; do printf "ready> "; read -r _ || break; done'
disp_out="$(printf 'dispatch-body-%s\n' "$tok_disp" | muxa_as "$bob_pane" dispatch --window --name dispkid -- bash -c "$disp_loop")"
if printf '%s' "$disp_out" | muxa-broker json-keys name id pane cwd state from to \
  && [ "$(json_get "$disp_out" name)" = "dispkid" ] \
  && [ "$(json_get "$disp_out" to)" = "dispkid" ] \
  && [ "$(json_get "$disp_out" from)" = "bob" ] \
  && [ "$(json_get "$disp_out" state)" = "dispatched" ]; then
  ok "dispatch stdout is send --json plus name/cwd/state"
else
  bad "dispatch stdout is send --json plus name/cwd/state" "got: $disp_out"
fi
disp_pane="$(json_get "$disp_out" pane)"
disp_cap=""
i=0
while [ "$i" -lt 40 ]; do
  disp_cap="$(tmux -L "$SOCK" capture-pane -p -t "$disp_pane" 2>/dev/null || true)"
  case "$disp_cap" in
    *"$tok_disp"*) break ;;
  esac
  sleep 0.15
  i=$((i + 1))
done
case "$disp_cap" in
  *"$tok_disp"*) ok "dispatch delivers the brief once the pane is ready" ;;
  *) bad "dispatch delivers the brief once the pane is ready" "cap: $disp_cap" ;;
esac
disp_esc_out="$(printf 'esc-dispatch\n' | muxa_as "$bob_pane" dispatch --window --name dispesc --cwd "$esc_cwd" -- bash -c "$disp_loop")"
disp_esc_cwd="$(json_get "$disp_esc_out" cwd)"
if same_dir "$disp_esc_cwd" "$esc_cwd"; then
  ok "dispatch stdout cwd round-trips quote and backslash"
else
  bad "dispatch stdout cwd round-trips quote and backslash" "got: $disp_esc_cwd want: $esc_cwd"
fi
brief_file="$tmpdir/dispatch.brief"
printf 'file-brief-%s\n' "$tok_disp" >"$brief_file"
disp_file_out="$(muxa_as "$bob_pane" dispatch --window --name dispfile --brief-file "$brief_file" -- bash -c "$disp_loop")"
disp_file_pane="$(json_get "$disp_file_out" pane)"
disp_file_cap=""
i=0
while [ "$i" -lt 40 ]; do
  disp_file_cap="$(tmux -L "$SOCK" capture-pane -p -t "$disp_file_pane" 2>/dev/null || true)"
  case "$disp_file_cap" in
    *"file-brief-$tok_disp"*) break ;;
  esac
  sleep 0.15
  i=$((i + 1))
done
case "$disp_file_cap" in
  *"file-brief-$tok_disp"*) ok "dispatch --brief-file delivers" ;;
  *) bad "dispatch --brief-file delivers" "cap: $disp_file_cap" ;;
esac
set +e
empty_disp="$(printf '' | muxa_as "$bob_pane" dispatch --name dispempty -- true 2>&1)"
empty_rc=$?
set -e
[ "$empty_rc" -ne 0 ] && ok "dispatch empty brief exits non-zero" \
  || bad "dispatch empty brief exits non-zero" "exit=$empty_rc out=$empty_disp"
assert_contains "$empty_disp" "empty brief" "dispatch empty brief names the error"

tmux -L "$SOCK" new-window -t muxa -n taildata "printf '%s\n' alpha-tail bravo-tail charlie-tail; exec sleep 3600"
sleep 0.3
tail_pane="$(tmux -L "$SOCK" list-panes -t muxa:taildata -F '#{pane_id}' | head -1)"
muxa_as "$tail_pane" register --name tautest --kind generic --parent bob >/dev/null
tailed="$(muxa_as "$bob_pane" tail tautest)"
assert_contains "$tailed" "alpha-tail" "tail captures visible pane"
assert_contains "$tailed" "charlie-tail" "tail captures last visible line"
last="$(muxa_as "$bob_pane" tail tautest -n 1)"
case "$last" in
  *charlie-tail*) ok "tail -n 1 is last history line" ;;
  *) bad "tail -n 1 is last history line" "got: $last" ;;
esac
last2="$(muxa_as "$bob_pane" tail -n 2 tautest)"
assert_contains "$last2" "bravo-tail" "tail -n N NAME flag order"
set +e
muxa_as "$bob_pane" tail nobody 2>/dev/null
tail_code=$?
set -e
[ "$tail_code" -eq 2 ] && ok "tail unknown exits 2" \
  || bad "tail unknown exits 2" "exit=$tail_code"

# --- unregister ---
tmux -L "$SOCK" new-window -t muxa -n unreg "exec sleep 3600"
sleep 0.2
unreg_pane="$(tmux -L "$SOCK" list-panes -t muxa:unreg -F '#{pane_id}')"
muxa_as "$unreg_pane" register --name dropme --kind generic >/dev/null
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
muxa_as "$unreg2_pane" register --name dropid --kind generic >/dev/null
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

if pane_exists "$unreg_pane"; then
  ok "unregister leaves the tmux pane running"
else
  bad "unregister leaves the tmux pane running" "pane $unreg_pane gone"
fi

# --- kill ---
tmux -L "$SOCK" new-window -t muxa -n killme "exec sleep 3600"
sleep 0.2
kill_pane="$(tmux -L "$SOCK" list-panes -t muxa:killme -F '#{pane_id}')"
muxa_as "$kill_pane" register --name killme --kind generic >/dev/null
kill_out="$(muxa_as "$bob_pane" kill killme)"
assert_contains "$kill_out" "killed killme" "kill by name confirms"
who="$(muxa_as "$bob_pane" who)"
case "$who" in
  *killme*) bad "kill by name removes from who" "still listed: $who" ;;
  *) ok "kill by name removes from who" ;;
esac
whoj_kill="$(muxa_as "$bob_pane" who --json)"
set +e
who_json_get "$whoj_kill" killme status >/dev/null
kill_json_rc=$?
set -e
[ "$kill_json_rc" -ne 0 ] && ok "kill by name removes from who --json" \
  || bad "kill by name removes from who --json" "still listed: $whoj_kill"
if pane_exists "$kill_pane"; then
  bad "kill by name removes the tmux pane" "pane $kill_pane still exists"
else
  ok "kill by name removes the tmux pane"
fi

tmux -L "$SOCK" new-window -t muxa -n killid "exec sleep 3600"
sleep 0.2
killid_pane="$(tmux -L "$SOCK" list-panes -t muxa:killid -F '#{pane_id}')"
muxa_as "$killid_pane" register --name killid --kind generic >/dev/null
kill_id="$(muxa_as "$killid_pane" id)"
kill_out="$(muxa_as "$bob_pane" kill "$kill_id")"
assert_contains "$kill_out" "killed killid" "kill by id confirms"
who="$(muxa_as "$bob_pane" who)"
case "$who" in
  *killid*) bad "kill by id removes from who" "still listed: $who" ;;
  *) ok "kill by id removes from who" ;;
esac
if pane_exists "$killid_pane"; then
  bad "kill by id removes the tmux pane" "pane $killid_pane still exists (unregister would leave it)"
else
  ok "kill by id removes the tmux pane"
fi

set +e
muxa_as "$bob_pane" kill nobody 2>/dev/null
kill_code=$?
set -e
[ "$kill_code" -eq 2 ] && ok "kill unknown exits 2" \
  || bad "kill unknown exits 2" "exit=$kill_code"

killdead_sp="$(muxa_as "$bob_pane" spawn --name killdead -- false)"
killdead_pane="$(printf '%s\n' "$killdead_sp" | spawn_pane_id)"
sleep 0.3
killdead_flag="$(tmux -L "$SOCK" display-message -t "$killdead_pane" -p '#{pane_dead}' 2>/dev/null || echo 0)"
[ "$killdead_flag" = "1" ] && ok "kill target with remain-on-exit is a dead pane" \
  || bad "kill target with remain-on-exit is a dead pane" "pane_dead=$killdead_flag"
who="$(muxa_as "$bob_pane" who)"
assert_contains "$who" "killdead" "dead remain-on-exit pane stays on who until kill"
kill_out="$(muxa_as "$bob_pane" kill killdead)"
assert_contains "$kill_out" "killed killdead" "kill removes a remain-on-exit dead pane"
who="$(muxa_as "$bob_pane" who)"
case "$who" in
  *killdead*) bad "kill dead pane removes from who" "still listed: $who" ;;
  *) ok "kill dead pane removes from who" ;;
esac
if pane_exists "$killdead_pane"; then
  bad "kill dead pane removes the tmux pane" "pane $killdead_pane still exists"
else
  ok "kill dead pane removes the tmux pane"
fi

killwin_sp="$(muxa_as "$bob_pane" spawn --name killwin --window -- sleep 3600)"
assert_contains "$killwin_sp" "spawned killwin" "kill --window fixture spawns"
win_list="$(tmux -L "$SOCK" list-windows -F '#{window_name}')"
assert_contains "$win_list" "killwin" "spawn --window fixture names the window"
muxa_as "$bob_pane" kill killwin >/dev/null
win_list="$(tmux -L "$SOCK" list-windows -F '#{window_name}')"
case "$win_list" in
  *killwin*) bad "kill of --window spawn removes the window" "still listed: $win_list" ;;
  *) ok "kill of --window spawn removes the window" ;;
esac

set +e
muxa_as "$alice_pane" state busy >/dev/null 2>&1
state_rc=$?
set -e
[ "$state_rc" -eq 1 ] && ok "state is not a command" \
  || bad "state is not a command" "exit=$state_rc"

# --- darwin adhoc sign of the source broker before RPC (#67) ---
# broker_cli signs $MUXA_BROKER_BIN (the source) then execs it. Do not stop
# the isolated test daemon: who --json still RPCs the source for encoding.
unsigned_src="$tmpdir/unsigned-muxa-broker"
"$GO" build "${ldflags[@]}" -o "$unsigned_src" "$ROOT/broker"
chmod +x "$unsigned_src"
daemon_pid_before=""
[ -f "$MUXA_BROKER_PID" ] && daemon_pid_before="$(cat "$MUXA_BROKER_PID" 2>/dev/null || true)"
saved_bin_sign="$MUXA_BROKER_BIN"
export MUXA_BROKER_BIN="$unsigned_src"
set +e
who_unsigned="$(muxa_as "$bob_pane" who --json 2>&1)"
rc_unsigned=$?
set -e
export MUXA_BROKER_BIN="$saved_bin_sign"
if [ "$rc_unsigned" -eq 0 ] \
  && [ "$(printf '%s' "$who_unsigned" | muxa-broker json-type)" = "array" ]; then
  ok "who --json RPC works through an unsigned source broker (isolated daemon untouched)"
else
  bad "who --json RPC works through an unsigned source broker (isolated daemon untouched)" \
    "exit=$rc_unsigned out=$who_unsigned"
fi
if [ "$(uname -s)" = Darwin ]; then
  if codesign --verify "$unsigned_src" >/dev/null 2>&1; then
    ok "broker_cli adhoc-signs the source broker before RPC"
  else
    bad "broker_cli adhoc-signs the source broker before RPC" "codesign --verify failed"
  fi
else
  ok "broker_cli adhoc-signs the source broker before RPC (non-darwin: no-op)"
fi
daemon_pid_after=""
[ -f "$MUXA_BROKER_PID" ] && daemon_pid_after="$(cat "$MUXA_BROKER_PID" 2>/dev/null || true)"
if [ -n "$daemon_pid_before" ] && [ "$daemon_pid_before" = "$daemon_pid_after" ] \
  && kill -0 "$daemon_pid_before" 2>/dev/null; then
  ok "sign-before-RPC left the isolated test daemon pid unchanged"
else
  bad "sign-before-RPC left the isolated test daemon pid unchanged" \
    "before=${daemon_pid_before:-none} after=${daemon_pid_after:-none}"
fi

# --- jobs/preflight gone; muxa must not require br, git, or python3 ---
nobr_bin="$tmpdir/nobr-bin"
mkdir -p "$nobr_bin"
for cmd in tmux; do
  loc="$(command -v "$cmd" 2>/dev/null || true)"
  [ -n "$loc" ] || continue
  ln -s "$loc" "$nobr_bin/$(basename "$loc")"
done
printf '%s\n' '#!/bin/sh' 'echo "muxa must not invoke python3" >&2' 'exit 127' >"$nobr_bin/python3"
chmod +x "$nobr_bin/python3"
nobr_path="$ROOT/bin:$nobr_bin:/usr/bin:/bin"
[ -z "$(PATH="$nobr_path" command -v br 2>/dev/null || true)" ] && ok "test PATH has no br" \
  || bad "test PATH has no br" "br=$(PATH="$nobr_path" command -v br)"

who_nobr="$(PATH="$nobr_path" muxa_as "$bob_pane" who)"
assert_contains "$who_nobr" "bob" "who works with br absent from PATH"
whoj_nopy="$(PATH="$nobr_path" muxa_as "$bob_pane" who --json)"
if [ "$(printf '%s' "$whoj_nopy" | PATH="$nobr_path" muxa-broker json-type)" = "array" ] \
  && printf '%s' "$whoj_nopy" | PATH="$nobr_path" muxa-broker json-keys name id parent kind state pane session cwd status; then
  ok "who --json works when python3 would fail if invoked"
else
  bad "who --json works when python3 would fail if invoked" "got: $whoj_nopy"
fi
ver_nobr="$(PATH="$nobr_path" muxa version)"
[ -n "$ver_nobr" ] && ok "version works with br absent from PATH" \
  || bad "version works with br absent from PATH" "out=$ver_nobr"
help_nobr="$(PATH="$nobr_path" muxa help)"
assert_contains "$help_nobr" "muxa send" "help works with br absent from PATH"
assert_contains "$help_nobr" "muxa dispatch" "help mentions dispatch"
assert_contains "$help_nobr" "muxa kill" "help mentions kill"
assert_contains "$help_nobr" "leave pane running" "help keeps unregister as leave-pane-running"
case "$help_nobr" in
  *"muxa jobs"*|*"muxa preflight"*) bad "help does not mention jobs or preflight" "out=$help_nobr" ;;
  *) ok "help does not mention jobs or preflight" ;;
esac

set +e
PATH="$nobr_path" muxa jobs list >/dev/null 2>&1
jobs_rc=$?
PATH="$nobr_path" muxa preflight >/dev/null 2>&1
pf_rc=$?
set -e
[ "$jobs_rc" -eq 1 ] && ok "jobs is not a command" \
  || bad "jobs is not a command" "exit=$jobs_rc"
[ "$pf_rc" -eq 1 ] && ok "preflight is not a command" \
  || bad "preflight is not a command" "exit=$pf_rc"

git_stub="$tmpdir/git-must-not-run"
mkdir -p "$git_stub"
printf '%s\n' '#!/bin/sh' 'echo "muxa must not invoke git" >&2' 'exit 127' >"$git_stub/git"
chmod +x "$git_stub/git"
who_nogit="$(PATH="$git_stub:$PATH" muxa_as "$bob_pane" who)"
assert_contains "$who_nogit" "bob" "who works when git would fail if invoked"
ver_nogit="$(PATH="$git_stub:$PATH" muxa version)"
[ -n "$ver_nogit" ] && ok "version works when git would fail if invoked" \
  || bad "version works when git would fail if invoked" "out=$ver_nogit"

tmux -L "$SOCK" new-window -t muxa -n killnogit "exec sleep 3600"
sleep 0.2
killnogit_pane="$(tmux -L "$SOCK" list-panes -t muxa:killnogit -F '#{pane_id}')"
muxa_as "$killnogit_pane" register --name killnogit --kind generic >/dev/null
kill_nogit="$(PATH="$git_stub:$PATH" muxa_as "$bob_pane" kill killnogit)"
assert_contains "$kill_nogit" "killed killnogit" "kill works when git would fail if invoked"

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
