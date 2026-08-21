#!/usr/bin/env bash
# End-to-end: Claude Code (parent) spawns Cursor Agent + Oh My Pi (children).
# Project-scoped hooks only. Usage: tests/e2e.sh
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLAUDE_BIN="${CLAUDE_BIN:-$(command -v claude)}"
AGENT_BIN="${AGENT_BIN:-$(command -v agent)}"
OMP_BIN="${OMP_BIN:-$(command -v omp)}"
[ -n "$CLAUDE_BIN" ] && [ -x "$CLAUDE_BIN" ] || { echo "claude not found" >&2; exit 1; }
[ -n "$AGENT_BIN" ] && [ -x "$AGENT_BIN" ] || { echo "agent not found" >&2; exit 1; }
[ -n "$OMP_BIN" ] && [ -x "$OMP_BIN" ] || { echo "omp not found" >&2; exit 1; }
PANE_PATH="$ROOT/bin:/Users/mbaranovski/.local/bin:/Users/mbaranovski/.bun/bin:/opt/homebrew/bin:/usr/bin:/bin"
PATH="$ROOT/bin:$PATH"
export MUXA_BIN="$ROOT/bin/muxa"
GO="${GO:-/usr/local/go/bin/go}"
command -v "$GO" >/dev/null 2>&1 || GO="$(command -v go)"
ldflags=()
case "$(uname -s)" in
  Darwin) ldflags=(-ldflags=-linkmode=external) ;;
esac
"$GO" build "${ldflags[@]}" -o "$ROOT/bin/muxa" "$ROOT/broker"
if [ "$(uname -s)" = Darwin ]; then
  xattr -c "$ROOT/bin/muxa" 2>/dev/null || true
  codesign -s - --force --timestamp=none "$ROOT/bin/muxa" 2>/dev/null || true
fi
SOCK="muxae2e$$"
SESSION="muxae2e"
ART="${MUXA_E2E_ART:-$ROOT/.muxa-e2e-artifacts}"
HOOK_LOG="$ART/hooks.log"
TOKEN="E2E_$(date +%s)_$$"
BOOT_S="${MUXA_E2E_BOOT_S:-90}"
HOP_S="${MUXA_E2E_HOP_S:-120}"
tmpdir="$(mktemp -d /tmp/muxa-e2e.XXXXXX)"
export MUXA_TMUX_SOCKET="$SOCK"
export MUXA_ENTER_DELAY="${MUXA_ENTER_DELAY:-0.4}"
export MUXA_HOOK_LOG="$HOOK_LOG"
export MUXA_BROKER=1
export MUXA_BROKER_DIR="$tmpdir/broker"
export MUXA_BROKER_SOCK="$tmpdir/broker/broker.sock"
export MUXA_BROKER_PID="$tmpdir/broker/broker.pid"
export MUXA_BROKER_BIN="$ROOT/bin/muxa"
export XDG_RUNTIME_DIR="$tmpdir/run"
mkdir -p "$tmpdir/run" "$tmpdir/broker"
# Do not inherit the operator mailbox or this worker's identity.
unset TMUX MUXA_NAME MUXA_PARENT MUXA_ID MUXA_HOME || true
case "$MUXA_BROKER_DIR" in
  "$tmpdir"/*) ;;
  *) echo "tests/e2e.sh: MUXA_BROKER_DIR must be under $tmpdir" >&2; exit 1 ;;
esac

pass=0
fail=0
mkdir -p "$ART"
: >"$HOOK_LOG"

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
bad() {
  fail=$((fail + 1))
  printf 'not ok %s %s\n' "$((pass + fail))" "$1"
  printf '  %s\n' "${2:-}" >&2
}

tmux_e() { tmux -L "$SOCK" "$@"; }

# Resolve a registered pane by @muxa_name (not window name). Spawned children
# share the parent window, so $SESSION:cursor / $SESSION:pi do not exist.
pane_by_name() {
  tmux_e list-panes -a -F '#{pane_id} #{@muxa_name}' 2>/dev/null \
    | awk -v n="$1" '$2==n { print $1; exit }'
}

pane_text() {
  tmux_e capture-pane -p -t "$1" -S - 2>/dev/null \
    | sed $'s/\x1b\\[[0-9;]*[a-zA-Z]//g' \
    | tr -d '\r'
}

dump() {
  tmux_e list-panes -a -F '#{window_name} id=#{pane_id} cmd=#{pane_current_command} name=#{@muxa_name} parent=#{@muxa_parent} muxid=#{@muxa_id}' \
    >"$ART/panes.txt" 2>/dev/null || true
  muxa who >"$ART/who.txt" 2>/dev/null || true
  pane_text "$(pane_by_name claude)" >"$ART/claude.pane" 2>/dev/null || true
  pane_text "$(pane_by_name cursor)" >"$ART/cursor.pane" 2>/dev/null || true
  pane_text "$(pane_by_name pi)" >"$ART/pi.pane" 2>/dev/null || true
  cp "$HOOK_LOG" "$ART/hooks.log.copy" 2>/dev/null || true
}

dismiss_named() {
  local pane
  pane="$(pane_by_name "$1")"
  [ -n "$pane" ] || return 0
  dismiss_dialogs "$pane"
}

dismiss_dialogs() {
  local target="$1" text
  [ -n "$target" ] || return 0
  tmux_e has-session -t "$SESSION" 2>/dev/null || return 0
  text="$(pane_text "$target" | tail -n 30)"
  case "$text" in
    *"Trust this"*|*"trust this"*|*"Do you trust"*|*"Yes, I trust"*|*"workspace trust"*)
      tmux_e send-keys -t "$target" Enter
      sleep 0.4
      ;;
  esac
  case "$text" in
    *"(y/n)"*|*"Y/n"*|*"[Y/n]"*)
      tmux_e send-keys -t "$target" y Enter
      sleep 0.4
      ;;
  esac
}

wait_registered() {
  local name="$1" seconds="$2" i=0 dead pane
  while [ "$i" -lt "$seconds" ]; do
    pane="$(pane_by_name "$name")"
    if [ -n "$pane" ]; then
      dead="$(tmux_e display-message -t "$pane" -p '#{pane_dead}' 2>/dev/null || echo 1)"
      if [ "$dead" = "1" ]; then
        printf 'pane %s is dead\n' "$name" >&2
        pane_text "$pane" | tail -n 40 >&2
        return 1
      fi
    fi
    dismiss_named claude
    dismiss_named cursor
    dismiss_named pi
    if muxa who 2>/dev/null | awk 'NR>1 {print $1}' | grep -qx "$name"; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  return 1
}

wait_state() {
  local name="$1" want="$2" seconds="$3" i=0 got
  while [ "$i" -lt "$seconds" ]; do
    got="$(muxa who --json 2>/dev/null | python3 -c '
import json, sys
name, want = sys.argv[1], sys.argv[2]
try:
    rows = json.loads(sys.stdin.read() or "[]")
except Exception:
    sys.exit(1)
for row in rows:
    if row.get("name") == name and row.get("state") == want:
        sys.exit(0)
sys.exit(1)
' "$name" "$want")" && return 0
    sleep 1
    i=$((i + 1))
  done
  return 1
}

wait_pane_has() {
  local target="$1" needle="$2" seconds="$3" i=0
  # Composer wraps long tokens and indents continuation lines.
  local compact
  compact="$(printf '%s' "$needle" | tr -d '[:space:]')"
  while [ "$i" -lt "$seconds" ]; do
    if pane_text "$target" | tr -d '[:space:]' | grep -Fq "$compact"; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  return 1
}

muxa_as() {
  local pane="$1"
  shift
  TMUX_PANE="$pane" muxa "$@"
}

# --- launch parent CLI; children are spawned after it registers ---
tmux_e new-session -d -s "$SESSION" -n ctl "exec sleep 3600"
tmux_e set-option -t "$SESSION" remain-on-exit on
tmux_e new-window -t "$SESSION" -n claude \
  "cd '$ROOT' && export PATH='$PANE_PATH' MUXA_BIN='$MUXA_BIN' MUXA_HOOK_LOG='$HOOK_LOG' MUXA_NAME=claude MUXA_TMUX_SOCKET='$SOCK' MUXA_BROKER=1 MUXA_BROKER_DIR='$MUXA_BROKER_DIR' MUXA_BROKER_SOCK='$MUXA_BROKER_SOCK' MUXA_BROKER_PID='$MUXA_BROKER_PID' MUXA_BROKER_BIN='$MUXA_BROKER_BIN' XDG_RUNTIME_DIR='$XDG_RUNTIME_DIR' && exec '$CLAUDE_BIN' --dangerously-skip-permissions --model haiku"
sleep 1

if ! tmux_e has-session -t "$SESSION" 2>/dev/null; then
  echo "tmux session failed to start" >&2
  exit 1
fi

printf 'claude=%s\nagent=%s\nomp=%s\n' "$CLAUDE_BIN" "$AGENT_BIN" "$OMP_BIN"

ctl_pane="$(tmux_e list-panes -t "$SESSION:ctl" -F '#{pane_id}')"
muxa_as "$ctl_pane" register --name human --kind generic >/dev/null

printf 'waiting up to %ss for claude SessionStart…\n' "$BOOT_S"
if wait_registered claude "$BOOT_S"; then
  ok "claude registered via project SessionStart hook"
else
  bad "claude registered via project SessionStart hook" "see $ART/claude.pane"
  dump
fi

claude_pane="$(tmux_e list-panes -t "$SESSION:claude" -F '#{pane_id}')"
id_c="$(muxa_as "$claude_pane" id 2>/dev/null || true)"
[ -n "$id_c" ] && ok "claude has unique id ($id_c)" || bad "claude has unique id" "empty"

# Spawn children from the claude pane (parent). muxa runs outside tmux with TMUX_PANE set.
spawn_c="$(muxa_as "$claude_pane" spawn --name cursor --kind cursor -- \
  "$AGENT_BIN" --trust --yolo --workspace "$ROOT" --model composer-2.5-fast 2>&1 || true)"
printf 'spawn cursor: %s\n' "$spawn_c"
if printf '%s\n' "$spawn_c" | grep -q 'spawned cursor'; then
  ok "claude spawned cursor child"
else
  bad "claude spawned cursor child" "$spawn_c"
fi

spawn_p="$(muxa_as "$claude_pane" spawn --name pi --kind pi -- \
  "$OMP_BIN" --approval-mode=yolo --cwd="$ROOT" 2>&1 || true)"
printf 'spawn pi: %s\n' "$spawn_p"
if printf '%s\n' "$spawn_p" | grep -q 'spawned pi'; then
  ok "claude spawned pi child"
else
  bad "claude spawned pi child" "$spawn_p"
fi

if wait_registered cursor "$BOOT_S"; then
  ok "cursor registered as child"
else
  bad "cursor registered as child" "$(pane_text "$(pane_by_name cursor)" | tail -n 40)"
fi
if wait_registered pi "$BOOT_S"; then
  ok "pi registered as child"
else
  bad "pi registered as child" "$(pane_text "$(pane_by_name pi)" | tail -n 40)"
fi

who="$(muxa who 2>/dev/null || true)"
printf '%s\n' "$who"
cursor_pane="$(pane_by_name cursor)"
pi_pane="$(pane_by_name pi)"

cpar="$(muxa_as "$cursor_pane" parent 2>/dev/null || true)"
ppar="$(muxa_as "$pi_pane" parent 2>/dev/null || true)"
if [ "$cpar" = "claude" ]; then ok "cursor parent is claude"; else bad "cursor parent is claude" "$cpar"; fi
if [ "$ppar" = "claude" ]; then ok "pi parent is claude"; else bad "pi parent is claude" "$ppar"; fi

# --- ACL (no LLM) ---
sib="$(muxa_as "$cursor_pane" send --no-reply pi 'SIB_NOPE' 2>&1 || true)"
printf '%s\n' "$sib" | grep -q forbidden && ok "sibling cursor → pi refused" || bad "sibling cursor → pi refused" "$sib"

sib2="$(muxa_as "$pi_pane" send --no-reply cursor 'SIB_NOPE' 2>&1 || true)"
printf '%s\n' "$sib2" | grep -q forbidden && ok "sibling pi → cursor refused" || bad "sibling pi → cursor refused" "$sib2"

unrel="$(muxa_as "$ctl_pane" send --no-reply cursor 'ROOT_NOPE' 2>&1 || true)"
printf '%s\n' "$unrel" | grep -q forbidden && ok "human root → claude child refused" || bad "human root → claude child refused" "$unrel"

r2r="$(muxa_as "$ctl_pane" send --no-reply claude 'ROOT_NOPE' 2>&1 || true)"
printf '%s\n' "$r2r" | grep -q forbidden && ok "human root → claude root refused" || bad "human root → claude root refused" "$r2r"

# --- inject parent → children ---
sleep 3
dismiss_named claude
dismiss_named cursor
dismiss_named pi

inject_u="MUXA_INJECT_CURSOR_$TOKEN"
inject_p="MUXA_INJECT_PI_$TOKEN"
sent="$(muxa_as "$claude_pane" send --no-reply cursor "$inject_u" 2>&1 || true)"
printf '%s\n' "$sent" | grep -Eq 'delivered|queued' && ok "parent → cursor send ($sent)" || bad "parent → cursor send" "$sent"
# A-bootstrap: a freshly spawned hook child must still receive the first brief.
if printf '%s\n' "$sent" | grep -q delivered; then
  ok "A-bootstrap: first cursor brief injected"
elif printf '%s\n' "$sent" | grep -q queued; then
  ok "A-bootstrap: first cursor brief queued (Stop hook already proven)"
fi
sent="$(muxa_as "$claude_pane" send --no-reply pi "$inject_p" 2>&1 || true)"
printf '%s\n' "$sent" | grep -Eq 'delivered|queued' && ok "parent → pi send ($sent)" || bad "parent → pi send" "$sent"

wait_pane_has "$cursor_pane" "$inject_u" 20 && ok "cursor TUI shows parent inject" || bad "cursor TUI shows parent inject" "$(pane_text "$cursor_pane" | tail -n 40)"
wait_pane_has "$pi_pane" "$inject_p" 20 && ok "pi TUI shows parent inject" || bad "pi TUI shows parent inject" "$(pane_text "$pi_pane" | tail -n 40)"

# A-path: idle panes are injected even after a first brief has landed.
wait_state cursor idle 30 || true
a_path="MUXA_A_PATH_$TOKEN"
sent="$(muxa_as "$claude_pane" send --no-reply cursor "$a_path" 2>&1 || true)"
if printf '%s\n' "$sent" | grep -q broker; then
  ok "A-path: idle send enqueues on broker"
else
  bad "A-path: idle send enqueues on broker" "$sent"
fi
wait_pane_has "$cursor_pane" "$a_path" 15 && ok "A-path: body is pasted immediately" \
  || bad "A-path: body is pasted immediately" "$(pane_text "$cursor_pane" | tail -n 20)"

# B-live: copy-mode must queue, must not ghost-flush, deliver after cancel.
b_live="MUXA_B_LIVE_$TOKEN"
tmux_e copy-mode -t "$cursor_pane" 2>/dev/null || true
sent="$(muxa_as "$claude_pane" send --no-reply cursor "$b_live" 2>&1 || true)"
if printf '%s\n' "$sent" | grep -q queued; then
  ok "B-live: copy-mode send queues"
else
  bad "B-live: copy-mode send queues" "$sent"
fi
sleep 1
if pane_text "$cursor_pane" | grep -Fq "$b_live"; then
  bad "B-live: copy-mode does not show the body" "$(pane_text "$cursor_pane" | tail -n 20)"
else
  ok "B-live: copy-mode does not show the body"
fi
tmux_e send-keys -t "$cursor_pane" -X cancel 2>/dev/null || true
wait_pane_has "$cursor_pane" "$b_live" 20 && ok "B-live: broker lands after copy-mode" \
  || bad "B-live: broker lands after copy-mode" "$(pane_text "$cursor_pane" | tail -n 20)"

# --- LLM: type into the parent pane (roots cannot muxa-send to each other) ---
wait_state claude idle 60 || true
sleep 2
hop="MUXA_HOP_$TOKEN"
hop_prompt="You have two child panes (cursor and pi). Send each: muxa send --no-reply cursor ${hop} and muxa send --no-reply pi ${hop}. Then stop."
printf '%s' "$hop_prompt" | tmux_e load-buffer -b muxae2e-hop -
tmux_e paste-buffer -p -d -b muxae2e-hop -t "$claude_pane"
sleep "${MUXA_ENTER_DELAY}"
tmux_e send-keys -t "$claude_pane" Enter
printf 'hop typed into claude pane\n'

if wait_pane_has "$cursor_pane" "$hop" 20; then
  ok "cursor child received parent hop"
else
  if wait_pane_has "$cursor_pane" "$hop" "$HOP_S"; then
    ok "cursor child received parent hop"
  else
    bad "cursor child received parent hop" "claude:"$'\n'"$(pane_text "$claude_pane" | tail -n 40)"$'\n'"cursor:"$'\n'"$(pane_text "$cursor_pane" | tail -n 40)"
  fi
fi
if wait_pane_has "$pi_pane" "$hop" 15; then
  ok "pi child received parent hop"
else
  if wait_pane_has "$pi_pane" "$hop" 30; then
    ok "pi child received parent hop"
  else
    bad "pi child received parent hop" "$(pane_text "$pi_pane" | tail -n 40)"
  fi
fi

dump
printf '\n%s passed, %s failed\n' "$pass" "$fail"
printf 'artifacts: %s\n' "$ART"
[ "$fail" -eq 0 ]
