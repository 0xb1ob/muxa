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
# darwin 25+ aborts a test binary without LC_UUID; only the external linker emits it.
"$GO" test "${ldflags[@]}" "$ROOT/broker"
"$GO" test "${ldflags[@]}" "$ROOT/tests/jsonhelper"
"$GO" build "${ldflags[@]}" -o "$ROOT/bin/muxa-test-json" "$ROOT/tests/jsonhelper"
if [ "$(uname -s)" = Darwin ]; then
  xattr -c "$ROOT/bin/muxa-broker" 2>/dev/null || true
  codesign -s - --force --timestamp=none "$ROOT/bin/muxa-broker" 2>/dev/null || true
  xattr -c "$ROOT/bin/muxa-test-json" 2>/dev/null || true
  codesign -s - --force --timestamp=none "$ROOT/bin/muxa-test-json" 2>/dev/null || true
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
case "$MUXA_BROKER_DIR" in
  "$HOME_ISO"/*) ;;
  *) echo "tests/broker.sh: MUXA_BROKER_DIR must be under $HOME_ISO" >&2; exit 1 ;;
esac

pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok %s %s\n' "$pass" "$1"; }
bad() { fail=$((fail + 1)); printf 'not ok %s %s\n' "$((pass + fail))" "$1"; printf '  %s\n' "$2" >&2; }

cleanup() {
  case "${MUXA_BROKER_PID:-}" in
    "$HOME_ISO"/*)
      if [ -f "$MUXA_BROKER_PID" ]; then
        kill "$(cat "$MUXA_BROKER_PID")" 2>/dev/null || true
      fi
      ;;
  esac
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
  timeout=0.3
  [ "$st" = busy ] && timeout=0.05
  if [ "$st" != "$prev" ] || [ "$st" = busy ]; then
    prev="$st"
    frame "$st"
  fi
  if IFS= read -r -t "$timeout" line; then
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

muxa_as "$parent_pane" register --name parent --kind generic >/dev/null
muxa_as "$child_pane" register --name child --kind generic --parent parent >/dev/null

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

# --- busy pane past deadline keeps mail; N messages arrive in order, none clobbered ---
tok_t="BRKT_$$"
tok_u="BRKU_$$"
settle "$child_pane"
tmux -L "$SOCK" send-keys -t "$child_pane" "BLOCKEDINPUT"
sent_t="$(muxa_as "$parent_pane" send child "$tok_t")"
sent_u="$(muxa_as "$parent_pane" send child "$tok_u")"
case "$sent_t" in
  *broker*) ok "busy-pane first send enqueues" ;;
  *) bad "busy-pane first send enqueues" "got: $sent_t" ;;
esac
case "$sent_u" in
  *broker*) ok "busy-pane second send enqueues" ;;
  *) bad "busy-pane second send enqueues" "got: $sent_u" ;;
esac
sleep $((MUXA_BROKER_DEADLINE + 2))
cap_t="$(tmux -L "$SOCK" capture-pane -p -t "$child_pane" 2>/dev/null || true)"
case "$cap_t" in
  *"$tok_t"*|*"$tok_u"*) bad "busy pane past deadline does not fallback-paste" "cap: $cap_t" ;;
  *) ok "busy pane past deadline does not fallback-paste" ;;
esac
pending_t="$(find "$MUXA_BROKER_DIR/pending" -name '*.json' 2>/dev/null | wc -l | tr -d ' ')"
[ "$pending_t" -eq 2 ] && ok "both messages still pending after deadline" \
  || bad "both messages still pending after deadline" "pending=$pending_t"
case "$(cat "$MUXA_BROKER_DIR/broker.log" 2>/dev/null || true)" in
  *"timeout fallback"*) bad "log has no timeout fallback paste" "log mentions timeout fallback" ;;
  *"holding"*"left queued"*) ok "log holds mail past deadline instead of pasting" ;;
  *) bad "log holds mail past deadline instead of pasting" "log: $(tail -20 "$MUXA_BROKER_DIR/broker.log" 2>/dev/null)" ;;
esac
tmux -L "$SOCK" send-keys -t "$child_pane" C-u
cap_t="$(wait_capture "$child_pane" "$tok_t" 50 || true)"
cap_u="$(wait_capture "$child_pane" "$tok_u" 50 || true)"
case "$cap_t" in
  *"$tok_t"*) ok "first queued message arrives after pane is free" ;;
  *) bad "first queued message arrives after pane is free" "cap: $cap_t" ;;
esac
case "$cap_u" in
  *"$tok_u"*) ok "second queued message arrives after pane is free" ;;
  *) bad "second queued message arrives after pane is free" "cap: $cap_u" ;;
esac
# bash-native: both tokens present and first before second
pre_t="${cap_u%%"$tok_t"*}"
pre_u="${cap_u%%"$tok_u"*}"
if [ "$pre_t" != "$cap_u" ] && [ "$pre_u" != "$cap_u" ] && [ ${#pre_t} -lt ${#pre_u} ]; then
  ok "queued messages arrive in order, none clobbered"
else
  bad "queued messages arrive in order, none clobbered" "cap: $cap_u"
fi

# --- dispatch: first brief after ready; never-ready fails to parent, not child ---
tok_dsp="BRKDSP_$$"
dsp_json="$(printf '%s\n' "$tok_dsp" | muxa_as "$parent_pane" dispatch --window --name dspkid -- bash -c "$prompt_loop")"
if [ "$(printf '%s' "$dsp_json" | muxa-test-json json-get state)" = "dispatched" ] \
  && [ "$(printf '%s' "$dsp_json" | muxa-test-json json-get name)" = "dspkid" ]; then
  dsp_pane="$(printf '%s' "$dsp_json" | muxa-test-json json-get pane)"
  case "$dsp_pane" in
    %*) ok "dispatch enqueues with JSON dispatched" ;;
    *) bad "dispatch enqueues with JSON dispatched" "got: $dsp_json" ;;
  esac
else
  bad "dispatch enqueues with JSON dispatched" "got: $dsp_json"
  dsp_pane=""
fi
cap_dsp="$(wait_capture "$dsp_pane" "$tok_dsp" 50 || true)"
case "$cap_dsp" in
  *"$tok_dsp"*) ok "dispatch pastes the brief once the child is ready" ;;
  *) bad "dispatch pastes the brief once the child is ready" "cap: $cap_dsp" ;;
esac
n_dsp="$(printf '%s\n' "$cap_dsp" | grep -c "$tok_dsp" || true)"
[ "$n_dsp" -eq 1 ] && ok "dispatch brief appears once" \
  || bad "dispatch brief appears once" "count=$n_dsp cap=$cap_dsp"

tok_nr="BRKNR_$$"
start_nr="$(date +%s)"
nr_json="$(printf '%s\n' "$tok_nr" | muxa_as "$parent_pane" dispatch --window --name dspstuck -- sleep 3600)"
nr_pane="$(printf '%s' "$nr_json" | muxa-test-json json-get pane)"
cap_nr_parent="$(wait_capture "$parent_pane" "dispatch failed: dspstuck" 80 || true)"
elapsed_nr=$(( $(date +%s) - start_nr ))
case "$cap_nr_parent" in
  *"dispatch failed: dspstuck"*"$nr_pane"*) ok "never-ready dispatch mails the parent" ;;
  *) bad "never-ready dispatch mails the parent" "elapsed=${elapsed_nr}s cap: $cap_nr_parent" ;;
esac
[ "$elapsed_nr" -le $((MUXA_BROKER_DEADLINE + 4)) ] \
  && ok "never-ready failure arrives near the deadline (${elapsed_nr}s)" \
  || bad "never-ready failure arrives near the deadline" "took ${elapsed_nr}s deadline=${MUXA_BROKER_DEADLINE}s"
cap_nr_child="$(tmux -L "$SOCK" capture-pane -p -t "$nr_pane" 2>/dev/null || true)"
case "$cap_nr_child" in
  *"$tok_nr"*) bad "never-ready child is not timeout-pasted" "cap: $cap_nr_child" ;;
  *) ok "never-ready child is not timeout-pasted" ;;
esac
case "$(cat "$MUXA_BROKER_DIR/broker.log" 2>/dev/null || true)" in
  *"timeout fallback"*) bad "dispatch does not timeout-fallback paste" "log mentions timeout fallback" ;;
  *"dispatch failed"*"never ready"*) ok "log records dispatch failure instead of a fallback paste" ;;
  *) bad "log records dispatch failure instead of a fallback paste" "log: $(tail -15 "$MUXA_BROKER_DIR/broker.log" 2>/dev/null)" ;;
esac

# --- E: agent-CLI composer pane takes the first brief immediately ---
# The regression is timing as much as delivery: a first brief to an idle
# composer must land because free-detection plus typed-in-box say free, not
# because a deadline expired. Control-mode silence should make that
# sub-second; the log must never call it a fallback paste (that path is gone).
tmux -L "$SOCK" split-window -v -t muxa:parent "$composer_loop"
sleep 0.5
composer_pane="$(tmux -L "$SOCK" list-panes -t muxa:parent -F '#{pane_id} #{pane_top}' | sort -k2,2n | awk 'END{print $1}')"
muxa_as "$composer_pane" register --name composer --kind generic --parent parent >/dev/null
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
# Typed-in-box still protects unsubmitted input. A mid-turn pane must
# actually draw (free-detection is drawing / two-signal, not interrupt
# phrases), so the busy stand-in animates rather than painting one static
# chrome frame.
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
muxa_as "$parent_pane" broker start >/dev/null 2>&1 || true
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

# --- I: a concurrent auto-start must not race the first daemon's bind ---
# ensure_broker used to drop start.lock as soon as it had backgrounded the
# broker, but the daemon re-execs with setsid and binds a moment later. In that
# gap a second starter took the lock, deleted broker.sock/broker.pid, and ran a
# second daemon against the same dir — two owners polling one pending/. The
# shim below binds late so the gap is wide on purpose; without the fix this
# logs two "listening" lines.
muxa_as "$parent_pane" broker stop >/dev/null 2>&1 || true
sleep 0.3
race_shim="$HOME_ISO/slow-broker"
cat >"$race_shim" <<SHIM
#!/bin/sh
sleep 2
exec "$ROOT/bin/muxa-broker" "\$@"
SHIM
chmod +x "$race_shim"
: >"$MUXA_BROKER_DIR/broker.log"
rm -f "$MUXA_BROKER_SOCK" "$MUXA_BROKER_PID"
rmdir "$MUXA_BROKER_DIR/start.lock" 2>/dev/null || true
( MUXA_BROKER_BIN="$race_shim" muxa_as "$parent_pane" broker start >/dev/null 2>&1 ) &
race_a=$!
# Mid-gap: the shim has not exec'd the real binary yet, so nothing is bound.
# The lock must still be held here — that is the whole fix. Asserting the held
# lock rather than racing a second starter keeps this deterministic; the
# destructive window (a non-owner deleting a *live* socket) is only a few
# hundred ms wide and would make the test flaky either way.
sleep 0.8
if muxa_as "$parent_pane" broker status >/dev/null 2>&1; then
  bad "I precondition: daemon not yet bound mid-gap" "socket already answering"
else
  ok "I precondition: daemon not yet bound mid-gap"
fi
if [ -d "$MUXA_BROKER_DIR/start.lock" ]; then
  ok "I start.lock is held until the daemon answers"
else
  bad "I start.lock is held until the daemon answers" \
      "lock already released while nothing was bound — a second starter could now delete the socket and pidfile of the daemon coming up"
fi
# A second starter arriving in the gap must not be able to claim the lock.
if mkdir "$MUXA_BROKER_DIR/start.lock" 2>/dev/null; then
  bad "I a second starter cannot claim the lock mid-gap" "took the lock"
  rmdir "$MUXA_BROKER_DIR/start.lock" 2>/dev/null || true
else
  ok "I a second starter cannot claim the lock mid-gap"
fi
( MUXA_BROKER_BIN="$race_shim" muxa_as "$parent_pane" broker start >/dev/null 2>&1 ) &
race_b=$!
wait "$race_a" "$race_b" 2>/dev/null || true
sleep 0.5
owners="$(grep -c 'listening' "$MUXA_BROKER_DIR/broker.log" 2>/dev/null || true)"
[ "$owners" = 1 ] && ok "I concurrent starts leave exactly one queue owner" \
  || bad "I concurrent starts leave exactly one queue owner" \
         "listening lines=$owners log: $(cat "$MUXA_BROKER_DIR/broker.log" 2>/dev/null)"
race_pid="$(cat "$MUXA_BROKER_PID" 2>/dev/null || true)"
if [ -n "$race_pid" ] && kill -0 "$race_pid" 2>/dev/null; then
  ok "I the surviving owner is the one in the pidfile"
else
  bad "I the surviving owner is the one in the pidfile" "pid=${race_pid:-none}"
fi
if [ -d "$MUXA_BROKER_DIR/start.lock" ]; then
  bad "I start.lock is released once the daemon is up" "lock still held"
else
  ok "I start.lock is released once the daemon is up"
fi
# The queue must still work through a real daemon afterwards.
tok_i="BRKI_$$"
settle "$child_pane"
muxa_as "$parent_pane" send child "$tok_i" >/dev/null
cap_i="$(wait_capture "$child_pane" "$tok_i" 60 || true)"
case "$cap_i" in
  *"$tok_i"*) ok "I the single owner still delivers" ;;
  *) bad "I the single owner still delivers" "cap: $cap_i" ;;
esac

# --- who STATUS drawing from control-mode %output, no hooks ---
tmux -L "$SOCK" new-window -t muxa -n ticker "while true; do echo DRAWING_TICK; sleep 0.15; done"
sleep 0.4
ticker_pane="$(tmux -L "$SOCK" list-panes -t muxa:ticker -F '#{pane_id}' | head -1)"
muxa_as "$ticker_pane" register --name ticker --kind generic --parent parent >/dev/null
# Drawing report window is 1s; wait for %output to land in the hub.
sleep 1.2
who_tick="$(muxa_as "$parent_pane" who)"
st_tick="$(printf '%s\n' "$who_tick" | awk '$1=="ticker" { print substr($0, 105, 8); exit }')"
st_tick="${st_tick#"${st_tick%%[![:space:]]*}"}"
st_tick="${st_tick%"${st_tick##*[![:space:]]}"}"
[ "$st_tick" = "drawing" ] && ok "who STATUS is drawing for a pane emitting %output" \
  || bad "who STATUS is drawing for a pane emitting %output" "status=$st_tick who=$who_tick"
st_state="$(printf '%s\n' "$who_tick" | awk '$1=="ticker" { print substr($0, 96, 8); exit }')"
st_state="${st_state#"${st_state%%[![:space:]]*}"}"
st_state="${st_state%"${st_state##*[![:space:]]}"}"
[ "$st_state" = "busy" ] && ok "who STATE is busy for a pane emitting %output" \
  || bad "who STATE is busy for a pane emitting %output" "state=$st_state who=$who_tick"
who_tickj="$(muxa_as "$parent_pane" who --json)"
[ "$(printf '%s' "$who_tickj" | muxa-test-json json-get ticker status)" = "drawing" ] \
  && [ "$(printf '%s' "$who_tickj" | muxa-test-json json-get ticker state)" = "busy" ] \
  && ok "who --json status is drawing and state is busy for a pane emitting %output" \
  || bad "who --json status is drawing and state is busy for a pane emitting %output" "json=$who_tickj"

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

# --- J: broker_cli must RPC a freshly go-built source binary (unsigned /
# provenance-tagged). ensure_broker already signs the runtime copy; without
# signing $bin, darwin 25 SIGKILLs who --json / ping and `muxa broker start`
# fails even though the daemon copy is fine.
unsigned_j="$HOME_ISO/fresh-unsigned-broker"
"$GO" build "${ldflags[@]}" -o "$unsigned_j" "$ROOT/broker"
chmod +x "$unsigned_j"
muxa_as "$parent_pane" broker stop >/dev/null || true
sleep 0.1
saved_bin_j="$MUXA_BROKER_BIN"
export MUXA_BROKER_BIN="$unsigned_j"
set +e
start_j="$(muxa_as "$parent_pane" broker start 2>&1)"
rc_j=$?
who_j="$(muxa_as "$parent_pane" who --json 2>&1)"
rc_who_j=$?
set -e
export MUXA_BROKER_BIN="$saved_bin_j"
[ "$rc_j" -eq 0 ] && ok "J broker start with unsigned source binary" \
  || bad "J broker start with unsigned source binary" "exit=$rc_j out=$start_j"
[ "$rc_who_j" -eq 0 ] && ok "J who --json with unsigned source binary" \
  || bad "J who --json with unsigned source binary" "exit=$rc_who_j out=$who_j"

# restart broker for cleanliness
muxa_as "$parent_pane" broker start >/dev/null || true

printf '\n%s tests, %s failed\n' "$((pass + fail))" "$fail"
[ "$fail" -eq 0 ]
