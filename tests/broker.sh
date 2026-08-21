#!/usr/bin/env bash
# Broker unit-adjacent integration: private tmux socket, dummy prompt loops.
# Does not touch the live ~/.muxa mailbox or the operator session.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PATH="$ROOT/bin:$PATH"
GO="${GO:-/usr/local/go/bin/go}"
command -v "$GO" >/dev/null 2>&1 || GO="$(command -v go)"

ldflags=()
case "$(uname -s)" in
  Darwin) ldflags=(-ldflags=-linkmode=external) ;;
esac
"$GO" build "${ldflags[@]}" -o "$ROOT/bin/muxa-broker" "$ROOT/broker"
if [ "$(uname -s)" = Darwin ]; then
  xattr -c "$ROOT/bin/muxa-broker" 2>/dev/null || true
  codesign -s - --force --timestamp=none "$ROOT/bin/muxa-broker" 2>/dev/null || true
fi

SOCK="muxabroker-$$"
HOME_ISO="$(mktemp -d /tmp/muxa-broker-test.XXXXXX)"
export MUXA_TMUX_SOCKET="$SOCK"
export MUXA_ENTER_DELAY=0.05
export MUXA_BROKER=1
export MUXA_BROKER_DIR="$HOME_ISO/broker"
export MUXA_BROKER_SOCK="$HOME_ISO/broker/broker.sock"
export MUXA_BROKER_PID="$HOME_ISO/broker/broker.pid"
export MUXA_BROKER_BIN="$ROOT/bin/muxa-broker"
export MUXA_BROKER_DEADLINE="${MUXA_BROKER_DEADLINE:-8}"
export MUXA_BROKER_POLL_MS=100
export XDG_RUNTIME_DIR="$HOME_ISO/run"
unset TMUX MUXA_NAME MUXA_PARENT MUXA_ID || true
# Do not inherit the operator mailbox.
unset MUXA_HOME || true

pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok %s %s\n' "$pass" "$1"; }
bad() { fail=$((fail + 1)); printf 'not ok %s %s\n' "$((pass + fail))" "$1"; printf '  %s\n' "$2" >&2; }

cleanup() {
  if [ -f "$MUXA_BROKER_PID" ]; then
    kill "$(cat "$MUXA_BROKER_PID")" 2>/dev/null || true
  fi
  tmux -L "$SOCK" kill-server 2>/dev/null || true
  rm -rf "$HOME_ISO"
}
trap cleanup EXIT

prompt_loop='while true; do printf "ready> "; read -r _ || break; done'

# Stand-in for an agent-CLI pane: a half-block composer box whose input row
# holds only a faint placeholder, with status chrome *below* the box — the
# shape a freshly spawned Cursor Agent / Claude / pi pane actually shows. The
# old last-non-empty-line rule read the cwd row as command output, so a first
# brief to a pane like this never looked free and waited out the deadline.
#
# The input row is driven by a state file so the test can put the pane into a
# state a real CLI would render — a shell's own echo lands wherever the cursor
# is, never inside the box, so faking "typed" any other way would make the
# assertion pass for the wrong reason.
#
# Each frame is one write, and only on a state change or new input. A real TUI
# emits a frame atomically; clearing and then drawing line by line on a timer
# leaves a window where capture-pane sees a blank or half-drawn pane, which
# reads as free and makes this case flaky in the broker's favour.
composer_log="$HOME_ISO/composer.log"
composer_state="$HOME_ISO/composer.state"
: >"$composer_log"
printf 'idle\n' >"$composer_state"
composer_loop='
top="\033[38;2;38;38;38m▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄\033[0m"
bot="\033[38;2;38;38;38m▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\033[0m"
frame() {
  local row log out
  case "$1" in
    busy)  row="\033[48;2;38;38;38m \033[2m→ Add a follow-up   ctrl+c to stop\033[0m" ;;
    typed) row="\033[48;2;38;38;38m HUMANTYPING\033[0m" ;;
    *)     row="\033[48;2;38;38;38m \033[2m→ Plan, search, build anything\033[0m" ;;
  esac
  log="$(cat '"$composer_log"' 2>/dev/null || true)"
  out="\033[H\033[2J"
  [ -n "$log" ] && out="$out$log\n"
  out="$out$top\n$row\n$bot\n Composer 2.5 Fast\n /tmp/fake-worktree\n"
  printf "%b" "$out"
}
prev=""
while true; do
  st="$(cat '"$composer_state"' 2>/dev/null || true)"
  if [ "$st" != "$prev" ]; then
    prev="$st"
    frame "$st"
  fi
  if IFS= read -r -t 0.3 line; then
    printf "%s\n" "$line" >>'"$composer_log"'
    frame "$st"
  fi
done'

tmux -L "$SOCK" new-session -d -s muxa -n parent "$prompt_loop"
tmux -L "$SOCK" split-window -h -t muxa:parent "$prompt_loop"
sleep 0.3

parent_pane="$(tmux -L "$SOCK" list-panes -t muxa:parent -F '#{pane_id} #{pane_left}' | sort -k2,2n | awk 'NR==1{print $1}')"
child_pane="$(tmux -L "$SOCK" list-panes -t muxa:parent -F '#{pane_id} #{pane_left}' | sort -k2,2n | awk 'NR==2{print $1}')"
[ -n "$parent_pane" ] && [ -n "$child_pane" ] || { echo "failed to create panes" >&2; exit 1; }

muxa_as() {
  local pane="$1"
  shift
  TMUX_PANE="$pane" muxa "$@"
}

muxa_as "$parent_pane" register --name parent --kind generic --deliver inject >/dev/null
muxa_as "$child_pane" register --name child --kind generic --deliver inject --parent parent >/dev/null

wait_capture() {
  local pane="$1" needle="$2" tries="${3:-40}" cap
  local i=0
  while [ "$i" -lt "$tries" ]; do
    cap="$(tmux -L "$SOCK" capture-pane -p -t "$pane" 2>/dev/null || true)"
    case "$cap" in
      *"$needle"*) printf '%s' "$cap"; return 0 ;;
    esac
    sleep 0.15
    i=$((i + 1))
  done
  printf '%s' "$cap"
  return 1
}

# Wait until the pane stops changing. Inject pastes, waits MUXA_ENTER_DELAY,
# then sends Enter — so wait_capture returns while that Enter is still in
# flight. Typing immediately after it races the Enter and the test's own
# keystrokes get submitted, which looks exactly like the broker pasting over
# typed text. Two identical captures mean the Enter has landed.
settle() {
  local pane="$1" prev="" cur i=0
  while [ "$i" -lt 40 ]; do
    cur="$(tmux -L "$SOCK" capture-pane -p -t "$pane" 2>/dev/null || true)"
    if [ "$i" -gt 0 ] && [ "$cur" = "$prev" ]; then
      return 0
    fi
    prev="$cur"
    sleep 0.15
    i=$((i + 1))
  done
  return 0
}

# --- A: idle empty prompt → paste + enter ---
tok_a="BRKA_$$"
sent_a="$(muxa_as "$parent_pane" send child "$tok_a")"
case "$sent_a" in
  *"queued parent → child"*"broker"*) ok "A send enqueues on broker" ;;
  *) bad "A send enqueues on broker" "got: $sent_a" ;;
esac
cap_a="$(wait_capture "$child_pane" "$tok_a" 40 || true)"
case "$cap_a" in
  *"$tok_a"*) ok "A idle prompt received token" ;;
  *) bad "A idle prompt received token" "cap: $cap_a" ;;
esac
kw="$(tmux -L "$SOCK" display-message -t "$child_pane" -p '#{@muxa_kick_wait}')"
[ -z "$kw" ] && ok "A did not spawn kick_wait" || bad "A did not spawn kick_wait" "wait=$kw"

# --- no double-delivery: one token, one [muxa] block ---
n_a="$(printf '%s\n' "$cap_a" | grep -c "$tok_a" || true)"
[ "$n_a" -eq 1 ] && ok "A token appears once" || bad "A token appears once" "count=$n_a cap=$cap_a"

# --- B: typing → wait → deliver after clear ---
tok_b="BRKB_$$"
settle "$child_pane"
tmux -L "$SOCK" send-keys -t "$child_pane" "STILLTYPING"
sleep 0.1
sent_b="$(muxa_as "$parent_pane" send child "$tok_b")"
case "$sent_b" in
  *broker*) ok "B send enqueues while typed" ;;
  *) bad "B send enqueues while typed" "got: $sent_b" ;;
esac
sleep 0.4
cap_b0="$(tmux -L "$SOCK" capture-pane -p -t "$child_pane")"
case "$cap_b0" in
  *"$tok_b"*) bad "B does not paste over typing" "cap: $cap_b0" ;;
  *) ok "B does not paste over typing" ;;
esac
tmux -L "$SOCK" send-keys -t "$child_pane" C-u
cap_b="$(wait_capture "$child_pane" "$tok_b" 50 || true)"
case "$cap_b" in
  *"$tok_b"*) ok "B delivers after input cleared" ;;
  *) bad "B delivers after input cleared" "cap: $cap_b" ;;
esac

# --- D: child → parent (the historically broken path) ---
tok_d="BRKD_$$"
sent_d="$(muxa_as "$child_pane" send parent "$tok_d")"
case "$sent_d" in
  *"queued child → parent"*"broker"*) ok "D child→parent enqueues" ;;
  *) bad "D child→parent enqueues" "got: $sent_d" ;;
esac
cap_d="$(wait_capture "$parent_pane" "$tok_d" 40 || true)"
case "$cap_d" in
  *"$tok_d"*) ok "D parent pane received child token" ;;
  *) bad "D parent pane received child token" "cap: $cap_d" ;;
esac

# --- timeout fallback: never-free pane still pastes after deadline ---
tok_t="BRKT_$$"
settle "$child_pane"
tmux -L "$SOCK" send-keys -t "$child_pane" "BLOCKEDINPUT"
sent_t="$(muxa_as "$parent_pane" send child "$tok_t")"
case "$sent_t" in
  *broker*) ok "timeout send enqueues" ;;
  *) bad "timeout send enqueues" "got: $sent_t" ;;
esac
cap_t="$(wait_capture "$child_pane" "$tok_t" 80 || true)"
case "$cap_t" in
  *"$tok_t"*) ok "timeout fallback pastes once" ;;
  *) bad "timeout fallback pastes once" "cap: $cap_t" ;;
esac
tmux -L "$SOCK" send-keys -t "$child_pane" C-u
sleep 0.2

# --- E: agent-CLI composer pane takes the first brief immediately ---
# The regression is timing as much as delivery: before the composer rule this
# pane only ever got a timeout fallback paste, so assert both that the token
# lands well inside the deadline and that the log does not call it a fallback.
tmux -L "$SOCK" split-window -v -t muxa:parent "$composer_loop"
sleep 0.5
composer_pane="$(tmux -L "$SOCK" list-panes -t muxa:parent -F '#{pane_id} #{pane_top}' | sort -k2,2n | awk 'END{print $1}')"
muxa_as "$composer_pane" register --name composer --kind generic --deliver inject --parent parent >/dev/null
cap_e0="$(tmux -L "$SOCK" capture-pane -p -t "$composer_pane")"
# Check the border glyph too: if the box does not render, the rest of this
# case fails as a mysterious delivery timeout instead of naming the cause.
case "$cap_e0" in
  *"Composer 2.5 Fast"*"▀"*|*"▀"*"Composer 2.5 Fast"*) ok "E composer pane painted its box" ;;
  *) bad "E composer pane painted its box (half-block borders missing?)" "cap: $cap_e0" ;;
esac
tok_e="BRKE_$$"
start_e="$(date +%s)"
sent_e="$(muxa_as "$parent_pane" send composer "$tok_e")"
case "$sent_e" in
  *broker*) ok "E send enqueues for composer pane" ;;
  *) bad "E send enqueues for composer pane" "got: $sent_e" ;;
esac
cap_e="$(wait_capture "$composer_pane" "$tok_e" 30 || true)"
elapsed_e=$(( $(date +%s) - start_e ))
case "$cap_e" in
  *"$tok_e"*) ok "E idle composer received the first brief" ;;
  *) bad "E idle composer received the first brief" "cap: $cap_e" ;;
esac
[ "$elapsed_e" -lt "$MUXA_BROKER_DEADLINE" ] \
  && ok "E delivered before the deadline (${elapsed_e}s < ${MUXA_BROKER_DEADLINE}s)" \
  || bad "E delivered before the deadline" "took ${elapsed_e}s, deadline ${MUXA_BROKER_DEADLINE}s"
line_e="$(grep -F "$tok_e" "$MUXA_BROKER_DIR/broker.log" 2>/dev/null || grep 'delivered parent → composer' "$MUXA_BROKER_DIR/broker.log" 2>/dev/null || true)"
case "$line_e" in
  *"timeout fallback"*) bad "E was not a timeout fallback paste" "log: $line_e" ;;
  *) ok "E was not a timeout fallback paste" ;;
esac

# --- F: a composer that is typed-in or mid-turn is left alone ---
# Widening the heuristic must not cost the protection it replaces: text a human
# typed and a live interrupt hint both still mean "not free".
composer_holds() {
  local label="$1" state="$2" tok="$3" cap
  settle "$composer_pane"
  printf '%s\n' "$state" >"$composer_state"
  sleep 0.5
  muxa_as "$parent_pane" send composer "$tok" >/dev/null
  sleep 1.5
  cap="$(tmux -L "$SOCK" capture-pane -p -t "$composer_pane")"
  case "$cap" in
    *"$tok"*) bad "$label" "cap: $cap" ;;
    *) ok "$label" ;;
  esac
  printf 'idle\n' >"$composer_state"
  cap="$(wait_capture "$composer_pane" "$tok" 40 || true)"
  case "$cap" in
    *"$tok"*) ok "$label → delivered once idle" ;;
    *) bad "$label → delivered once idle" "cap: $cap" ;;
  esac
}
composer_holds "F typed composer is not pasted over" typed "BRKF_$$"
composer_holds "F busy composer is not pasted over" busy "BRKFB_$$"

# --- G: the daemon outlives its starter's process group ---
# The incident: nohup + disown left the broker in the caller's process group,
# so the teardown at the end of the calling tool call killed it before its
# first delivery. Start it from a throwaway session, tear that whole group
# down, and require the queue to still have an owner that drains.
muxa_as "$parent_pane" broker stop >/dev/null 2>&1 || true
sleep 0.3
python3 - "$ROOT/bin/muxa" <<'PY' >/dev/null 2>&1 || true
import os, subprocess, sys
subprocess.run([sys.argv[1], "broker", "start"], preexec_fn=os.setsid,
               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
PY
sleep 0.3
daemon_pid="$(cat "$MUXA_BROKER_PID" 2>/dev/null || true)"
if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null; then
  ok "G daemon started from a throwaway session"
else
  bad "G daemon started from a throwaway session" "pid=${daemon_pid:-none}"
fi
daemon_sid="$(ps -p "$daemon_pid" -o sess= 2>/dev/null | tr -d ' ' || true)"
own_sid="$(ps -p $$ -o sess= 2>/dev/null | tr -d ' ' || true)"
daemon_pgid="$(ps -p "$daemon_pid" -o pgid= 2>/dev/null | tr -d ' ' || true)"
if [ -n "$daemon_pgid" ] && [ "$daemon_pgid" = "$daemon_pid" ]; then
  ok "G daemon leads its own process group (pgid=$daemon_pgid)"
else
  bad "G daemon leads its own process group" "pid=$daemon_pid pgid=${daemon_pgid:-?} sid=${daemon_sid:-?} own_sid=${own_sid:-?}"
fi
# Kill every group except the daemon's that could plausibly have started it.
kill -TERM -- "-$daemon_pgid" 2>/dev/null && sleep 0.5
if kill -0 "$daemon_pid" 2>/dev/null; then
  bad "G group TERM aimed at the daemon's own group stops it" "still alive"
else
  ok "G group TERM aimed at the daemon's own group stops it"
fi
# Now the real shape: restart, then kill the *starter's* group and require survival.
muxa_as "$parent_pane" broker start >/dev/null 2>&1 || true
sleep 0.3
daemon_pid="$(cat "$MUXA_BROKER_PID" 2>/dev/null || true)"
starter_pgid="$(ps -p $$ -o pgid= 2>/dev/null | tr -d ' ' || true)"
daemon_pgid="$(ps -p "$daemon_pid" -o pgid= 2>/dev/null | tr -d ' ' || true)"
if [ -n "$daemon_pgid" ] && [ "$daemon_pgid" != "$starter_pgid" ]; then
  ok "G daemon left the starting shell's process group"
else
  bad "G daemon left the starting shell's process group" "daemon_pgid=${daemon_pgid:-?} starter_pgid=${starter_pgid:-?}"
fi
tok_g="BRKG_$$"
muxa_as "$parent_pane" send child "$tok_g" >/dev/null
cap_g="$(wait_capture "$child_pane" "$tok_g" 40 || true)"
case "$cap_g" in
  *"$tok_g"*) ok "G restarted daemon drains the queue" ;;
  *) bad "G restarted daemon drains the queue" "cap: $cap_g" ;;
esac

# --- H: a stopped daemon accounts for what it could not hand over ---
tok_h="BRKH_$$"
settle "$child_pane"
tmux -L "$SOCK" send-keys -t "$child_pane" "BLOCKSHUTDOWN"
sleep 0.2
muxa_as "$parent_pane" send child "$tok_h" >/dev/null
sleep 0.5
muxa_as "$parent_pane" broker stop >/dev/null 2>&1 || true
sleep 0.8
case "$(cat "$MUXA_BROKER_DIR/broker.log" 2>/dev/null)" in
  *"shutdown signal="*) ok "H shutdown is logged" ;;
  *) bad "H shutdown is logged" "log tail: $(tail -3 "$MUXA_BROKER_DIR/broker.log" 2>/dev/null)" ;;
esac
case "$(cat "$MUXA_BROKER_DIR/broker.log" 2>/dev/null)" in
  *"pending left in"*|*"queue drained"*) ok "H shutdown accounts for the queue" ;;
  *) bad "H shutdown accounts for the queue" "log tail: $(tail -3 "$MUXA_BROKER_DIR/broker.log" 2>/dev/null)" ;;
esac
if [ -n "$(ls -A "$MUXA_BROKER_DIR/pending" 2>/dev/null)" ]; then
  muxa_as "$parent_pane" broker start >/dev/null 2>&1 || true
  sleep 0.5
  case "$(cat "$MUXA_BROKER_DIR/broker.log" 2>/dev/null)" in
    *"re-adopted"*) ok "H restart re-adopts stranded mail" ;;
    *) bad "H restart re-adopts stranded mail" "log tail: $(tail -3 "$MUXA_BROKER_DIR/broker.log" 2>/dev/null)" ;;
  esac
  tmux -L "$SOCK" send-keys -t "$child_pane" C-u
  cap_h="$(wait_capture "$child_pane" "$tok_h" 60 || true)"
  case "$cap_h" in
    *"$tok_h"*) ok "H stranded mail is delivered after restart" ;;
    *) bad "H stranded mail is delivered after restart" "cap: $cap_h" ;;
  esac
else
  ok "H restart re-adopts stranded mail (queue already drained on shutdown)"
  ok "H stranded mail is delivered after restart (drained on shutdown)"
  tmux -L "$SOCK" send-keys -t "$child_pane" C-u
fi
sleep 0.2

# --- C: broker down + binary hidden → fail closed, nothing pasted ---
muxa_as "$parent_pane" broker stop >/dev/null || true
sleep 0.1
saved_bin="$MUXA_BROKER_BIN"
export MUXA_BROKER_BIN="$HOME_ISO/no-such-broker"
tok_c="BRKC_$$"
set +e
sent_c="$(muxa_as "$parent_pane" send child "$tok_c" 2>&1)"
rc_c=$?
set -e
export MUXA_BROKER_BIN="$saved_bin"
[ "$rc_c" -ne 0 ] && ok "C send exits non-zero when broker down" \
  || bad "C send exits non-zero when broker down" "exit=$rc_c out=$sent_c"
case "$sent_c" in
  *"fallback paste"*|*"falling back"*) bad "C does not paste-buffer fallback" "got: $sent_c" ;;
  *) ok "C does not paste-buffer fallback" ;;
esac
cap_c="$(tmux -L "$SOCK" capture-pane -p -t "$child_pane" 2>/dev/null || true)"
case "$cap_c" in
  *"$tok_c"*) bad "C nothing pasted when broker down" "cap: $cap_c" ;;
  *) ok "C nothing pasted when broker down" ;;
esac

# restart broker for cleanliness
muxa_as "$parent_pane" broker start >/dev/null || true

printf '\n%s tests, %s failed\n' "$((pass + fail))" "$fail"
[ "$fail" -eq 0 ]
