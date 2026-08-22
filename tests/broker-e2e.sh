#!/usr/bin/env bash
# Live E2E for the pane-mail broker on this machine.
# Isolated tmux socket + throwaway runtime dir. Does not spawn agent workers,
# does not touch ~/.muxa mail, does not message the operator parent pane.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PATH="$ROOT/bin:$PATH"
GO="${GO:-/usr/local/go/bin/go}"
command -v "$GO" >/dev/null 2>&1 || GO="$(command -v go)"
LOG="${MUXA_BROKER_E2E_LOG:-/tmp/muxa-broker-e2e.txt}"

ldflags=()
case "$(uname -s)" in
  Darwin) ldflags=(-ldflags=-linkmode=external) ;;
esac
"$GO" build "${ldflags[@]}" -o "$ROOT/bin/muxa" "$ROOT/broker"
if [ "$(uname -s)" = Darwin ]; then
  xattr -c "$ROOT/bin/muxa" 2>/dev/null || true
  codesign -s - --force --timestamp=none "$ROOT/bin/muxa" 2>/dev/null || true
fi

ISO="/tmp/muxa-broker-e2e-$$"
SOCK="muxa-e2e-broker-$$"
mkdir -p "$ISO/run" "$ISO/broker"
: >"$LOG"

log() { printf '%s\n' "$*" | tee -a "$LOG"; }

pass=0
fail=0
ok() { pass=$((pass + 1)); log "PASS $1"; }
bad() { fail=$((fail + 1)); log "FAIL $1"; log "  $2"; }

export MUXA_TMUX_SOCKET="$SOCK"
export MUXA_ENTER_DELAY=0.08
export MUXA_BROKER=1
export MUXA_BROKER_DIR="$ISO/broker"
export MUXA_BROKER_SOCK="$ISO/broker/broker.sock"
export MUXA_BROKER_PID="$ISO/broker/broker.pid"
export MUXA_BROKER_BIN="$ROOT/bin/muxa"
export MUXA_BROKER_DEADLINE=12
export MUXA_BROKER_POLL_MS=150
export XDG_RUNTIME_DIR="$ISO/run"
unset TMUX MUXA_NAME MUXA_PARENT MUXA_ID MUXA_HOME || true

cleanup() {
  case "${MUXA_BROKER_PID:-}" in
    "$ISO"/*)
      if [ -f "$MUXA_BROKER_PID" ]; then
        kill "$(cat "$MUXA_BROKER_PID")" 2>/dev/null || true
      fi
      ;;
  esac
  tmux -L "$SOCK" kill-server 2>/dev/null || true
  # keep $ISO for evidence if failed; always leave LOG
}
trap cleanup EXIT

prompt_loop='while true; do printf "ready> "; read -r _ || break; done'

log "=== muxa broker live E2E $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="
log "cwd=$ROOT"
log "binary=$ROOT/bin/muxa"
log "muxa=$ROOT/bin/muxa"
log "iso=$ISO sock=-L $SOCK"
log "operator mailbox must not see these tokens (throwaway XDG_RUNTIME_DIR)"

tmux -L "$SOCK" new-session -d -s e2e -n pair "$prompt_loop"
tmux -L "$SOCK" split-window -h -t e2e:pair "$prompt_loop"
sleep 0.4

parent_pane="$(tmux -L "$SOCK" list-panes -t e2e:pair -F '#{pane_id} #{pane_left}' | sort -k2,2n | awk 'NR==1{print $1}')"
child_pane="$(tmux -L "$SOCK" list-panes -t e2e:pair -F '#{pane_id} #{pane_left}' | sort -k2,2n | awk 'NR==2{print $1}')"
log "parent_pane=$parent_pane child_pane=$child_pane"

muxa_as() {
  local pane="$1"
  shift
  TMUX_PANE="$pane" "$ROOT/bin/muxa" "$@"
}

log "\$ $ROOT/bin/muxa register --name e2e-parent (pane $parent_pane)"
muxa_as "$parent_pane" register --name e2e-parent --kind generic | tee -a "$LOG"
log "\$ muxa register --name e2e-child --parent e2e-parent (pane $child_pane)"
muxa_as "$child_pane" register --name e2e-child --kind generic --parent e2e-parent | tee -a "$LOG"
log "\$ muxa broker start"
muxa_as "$parent_pane" broker start | tee -a "$LOG"

wait_capture() {
  local pane="$1" needle="$2" tries="${3:-50}" cap
  local i=0
  while [ "$i" -lt "$tries" ]; do
    cap="$(tmux -L "$SOCK" capture-pane -p -t "$pane" 2>/dev/null || true)"
    case "$cap" in
      *"$needle"*) printf '%s' "$cap"; return 0 ;;
    esac
    sleep 0.2
    i=$((i + 1))
  done
  printf '%s' "$cap"
  return 1
}

# Wait until the pane stops changing. Inject pastes, waits MUXA_ENTER_DELAY,
# then sends Enter — so wait_capture returns while that Enter is still in
# flight. Typing straight after it races the Enter, the test's own keystrokes
# get submitted, and "held while typed" fails for a reason that has nothing to
# do with the broker. Two identical captures mean the Enter has landed.
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

dump_cap() {
  local pane="$1" label="$2"
  {
    printf -- '--- capture-pane %s %s ---\n' "$label" "$pane"
    tmux -L "$SOCK" capture-pane -p -t "$pane" 2>/dev/null || printf '(capture failed)\n'
    printf -- '--- end capture ---\n'
  } | tee -a "$LOG"
}

# A
tok_a="E2E_A_IDLE_$$"
log "=== A idle empty prompt ==="
log "\$ muxa send e2e-child $tok_a  (from parent $parent_pane)"
sent_a="$(muxa_as "$parent_pane" send e2e-child "$tok_a" 2>&1)" || true
log "send: $sent_a"
cap_a="$(wait_capture "$child_pane" "$tok_a" 50 || true)"
log "wait_capture A:"
printf '%s\n' "$cap_a" >>"$LOG"
dump_cap "$child_pane" "A-child"
case "$sent_a" in *broker*) ok "A enqueue broker" ;; *) bad "A enqueue broker" "$sent_a" ;; esac
case "$cap_a" in *"$tok_a"*) ok "A pasted+submitted on idle prompt" ;; *) bad "A pasted+submitted on idle prompt" "$cap_a" ;; esac
kw="$(tmux -L "$SOCK" display-message -t "$child_pane" -p '#{@muxa_kick_wait}')"
[ -z "$kw" ] && ok "A no kick_wait" || bad "A no kick_wait" "wait=$kw"

# B
tok_b="E2E_B_WAIT_$$"
log "=== B non-empty input then clear ==="
settle "$child_pane"
log "\$ tmux send-keys -t $child_pane 'TYPED'"
tmux -L "$SOCK" send-keys -t "$child_pane" "TYPED"
sleep 0.15
dump_cap "$child_pane" "B-before-send"
log "\$ muxa send e2e-child $tok_b"
sent_b="$(muxa_as "$parent_pane" send e2e-child "$tok_b" 2>&1)" || true
log "send: $sent_b"
sleep 0.5
cap_b0="$(tmux -L "$SOCK" capture-pane -p -t "$child_pane")"
log "capture while typed:"
printf '%s\n' "$cap_b0" >>"$LOG"
case "$cap_b0" in *"$tok_b"*) bad "B held while typed" "$cap_b0" ;; *) ok "B held while typed" ;; esac
log "\$ tmux send-keys -t $child_pane C-u"
tmux -L "$SOCK" send-keys -t "$child_pane" C-u
cap_b="$(wait_capture "$child_pane" "$tok_b" 60 || true)"
log "wait_capture B:"
printf '%s\n' "$cap_b" >>"$LOG"
dump_cap "$child_pane" "B-after-clear"
case "$cap_b" in *"$tok_b"*) ok "B delivered after clear" ;; *) bad "B delivered after clear" "$cap_b" ;; esac

# D child→parent
tok_d="E2E_D_C2P_$$"
log "=== D child→parent ==="
log "\$ muxa send e2e-parent $tok_d  (from child $child_pane)"
sent_d="$(muxa_as "$child_pane" send e2e-parent "$tok_d" 2>&1)" || true
log "send: $sent_d"
cap_d="$(wait_capture "$parent_pane" "$tok_d" 50 || true)"
log "wait_capture D (parent):"
printf '%s\n' "$cap_d" >>"$LOG"
dump_cap "$parent_pane" "D-parent"
case "$sent_d" in *"broker"*) ok "D child→parent send" ;; *) bad "D child→parent send" "$sent_d" ;; esac
case "$cap_d" in *"$tok_d"*) ok "D unique token in parent capture" ;; *) bad "D unique token in parent capture" "$cap_d" ;; esac

# C broker down: fail closed, nothing pasted
tok_c="E2E_C_FAILCLOSED_$$"
log "=== C broker down fail-closed ==="
log "\$ muxa broker stop"
muxa_as "$parent_pane" broker stop | tee -a "$LOG"
export MUXA_BROKER_BIN="$ISO/no-such-broker"
log "\$ muxa send e2e-child $tok_c  (broker bin hidden)"
set +e
sent_c="$(muxa_as "$parent_pane" send e2e-child "$tok_c" 2>&1)"
rc_c=$?
set -e
log "send rc=$rc_c: $sent_c"
export MUXA_BROKER_BIN="$ROOT/bin/muxa"
cap_c="$(tmux -L "$SOCK" capture-pane -p -t "$child_pane" 2>/dev/null || true)"
log "capture C:"
printf '%s\n' "$cap_c" >>"$LOG"
dump_cap "$child_pane" "C-child"
[ "$rc_c" -ne 0 ] && ok "C send exits non-zero" || bad "C send exits non-zero" "rc=$rc_c $sent_c"
case "$sent_c" in *"fallback paste"*|*"falling back"*) bad "C no paste fallback" "$sent_c" ;; *) ok "C no paste fallback" ;; esac
case "$cap_c" in *"$tok_c"*) bad "C nothing pasted" "$cap_c" ;; *) ok "C nothing pasted" ;; esac

# Confirm live ~/.muxa / default runtime did not handle tokens
log "=== isolation: operator mailbox must not contain tokens ==="
# Scope to *this* run's tokens. A bare "E2E_[ABCD]_" also matches the
# installed copy of this script (where the token is still an unexpanded $$)
# and any residue an older, unisolated run left in the operator runtime —
# neither of which says anything about whether this run stayed isolated.
op_hits="$(grep -R -l -E "E2E_[ABCD]_[A-Z]+_$$\b" "$HOME/.muxa" /tmp/muxa-"$(id -u)"/muxa 2>/dev/null || true)"
# exclude our iso dir if it lives under /tmp/muxa-uid (it does not)
if [ -n "$op_hits" ]; then
  bad "operator paths have no E2E tokens" "$op_hits"
else
  ok "operator ~/.muxa and default runtime have no E2E tokens"
fi

log "=== summary pass=$pass fail=$fail log=$LOG ==="
log "how to run daemon: muxa broker start"
log "  env: MUXA_BROKER_DIR MUXA_BROKER_SOCK MUXA_BROKER_PID MUXA_BROKER_DEADLINE (seconds, default 600)"
log "  binary: $ROOT/bin/muxa (auto-started from muxa send)"
[ "$fail" -eq 0 ]
