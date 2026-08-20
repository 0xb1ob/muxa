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

# --- C: broker down → immediate paste fallback ---
muxa_as "$parent_pane" broker stop >/dev/null || true
sleep 0.1
# Prevent auto-start by hiding the binary for this one send.
saved_bin="$MUXA_BROKER_BIN"
export MUXA_BROKER_BIN="$HOME_ISO/no-such-broker"
tok_c="BRKC_$$"
sent_c="$(muxa_as "$parent_pane" send child "$tok_c" 2>&1)" || true
export MUXA_BROKER_BIN="$saved_bin"
case "$sent_c" in
  *"fallback paste"*) ok "C send reports fallback paste" ;;
  *) bad "C send reports fallback paste" "got: $sent_c" ;;
esac
cap_c="$(wait_capture "$child_pane" "$tok_c" 20 || true)"
case "$cap_c" in
  *"$tok_c"*) ok "C fallback delivered" ;;
  *) bad "C fallback delivered" "cap: $cap_c" ;;
esac

# restart broker for cleanliness
muxa_as "$parent_pane" broker start >/dev/null || true

printf '\n%s tests, %s failed\n' "$((pass + fail))" "$fail"
[ "$fail" -eq 0 ]
