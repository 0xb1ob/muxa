#!/usr/bin/env bash
# Recapture tests/fixtures/composer from throwaway tmux panes.
# Not part of CI — needs a real Cursor Agent (and optional claude/omp).
# Usage: tests/record-composer-fixtures.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=tests/record-fixture.sh
. "$ROOT/tests/record-fixture.sh"

DEST="$ROOT/tests/fixtures/composer"
SOCK="muxafixrec$$"
SESSION="muxafixrec"
AGENT_BIN="${AGENT_BIN:-$(command -v agent)}"
[ -n "$AGENT_BIN" ] && [ -x "$AGENT_BIN" ] || { echo "agent not found" >&2; exit 1; }

mkdir -p "$DEST"
cleanup() { tmux -L "$SOCK" kill-server 2>/dev/null || true; }
trap cleanup EXIT

tmux_r() { tmux -L "$SOCK" "$@"; }

# Colour is load-bearing: NO_COLOR strips the half-block composer and SGR,
# which is how the splash line (cwd · branch/sha) defeated last-non-empty.
agent_cmd() {
  printf '%s' "unset NO_COLOR FORCE_COLOR; export TERM=xterm-256color COLORTERM=truecolor PATH=$(printf '%q' "$PATH"); exec $(printf '%q' "$AGENT_BIN") $*"
}

wait_plain() {
  local needle="$1" seconds="${2:-30}" i=0 text
  while [ "$i" -lt "$seconds" ]; do
    text="$(tmux_r capture-pane -p -t "$SESSION:cursor" 2>/dev/null || true)"
    case "$text" in
      *"$needle"*) return 0 ;;
    esac
    sleep 1
    i=$((i + 1))
  done
  echo "timeout waiting for: $needle" >&2
  tmux_r capture-pane -p -t "$SESSION:cursor" >&2 || true
  return 1
}

wait_esc() {
  local needle="$1" seconds="${2:-20}" i=0 raw
  while [ "$i" -lt "$seconds" ]; do
    raw="$(tmux_r capture-pane -e -p -t "$SESSION:cursor" 2>/dev/null || true)"
    case "$raw" in
      *"$needle"*) return 0 ;;
    esac
    sleep 0.2
    i=$((i + 1))
  done
  return 1
}

record_named() {
  local name="$1"
  local pane
  pane="$(tmux_r list-panes -t "$SESSION:cursor" -F '#{pane_id}')"
  record_composer_fixture "$SOCK" "$pane" "$DEST/$name"
  printf 'origin=live\nkind=cursor\n' >>"$DEST/$name.meta"
  printf 'recorded %s cursor=%s t2_bytes=%s\n' \
    "$name" \
    "$(awk -F= '/^cursor_/{printf "%s=%s ",$1,$2}' "$DEST/$name.meta")" \
    "$(wc -c <"$DEST/$name.t2.ansi" | tr -d ' ')"
}

# --- Cursor Agent: splash / idle / typed / busy / trust ---
tmux_r start-server
tmux_r new-session -d -s "$SESSION" -x 120 -y 32 -n cursor \
  "$(agent_cmd --trust --force --model composer-2.5-fast)"

wait_plain "Cursor Agent" 40
wait_plain "Plan, search, build anything" 20
wait_esc "▄" 10 || true

# Splash is the first idle screen: empty composer + cwd/branch (or sha) footer.
# Hardware cursor is typically parked on the blank row below that footer, not
# on the composer input — record that, do not guess the TUI caret.
record_named cursor-agent-splash

# Reverse-video block cursor over the placeholder (splash idle).
wait_esc $'\x1b[0;7m' 5 || true
record_named cursor-revcursor-idle

# Faint placeholder without reverse, if the caret blinks off; otherwise this
# is the same idle splash and still a live recapture after #41.
sleep 0.4
record_named cursor-idle

tmux_r send-keys -t "$SESSION:cursor" 'hello world'
wait_plain "hello world" 5
record_named cursor-typed

# Drop the typed text so the busy capture is a running turn, not leftover input.
tmux_r send-keys -t "$SESSION:cursor" C-u
sleep 0.3
tmux_r send-keys -t "$SESSION:cursor" 'Reply with exactly the word pong and then stop.'
sleep 0.2
tmux_r send-keys -t "$SESSION:cursor" Enter

# Busy: interrupt hint inside the box, and/or a spinner above it.
i=0
while [ "$i" -lt 40 ]; do
  text="$(tmux_r capture-pane -p -t "$SESSION:cursor" 2>/dev/null || true)"
  case "$text" in
    *"ctrl+c to stop"*|*"ctrl-c to stop"*|*"esc to interrupt"*|*"Running"*)
      break
      ;;
  esac
  sleep 0.25
  i=$((i + 1))
done
record_named cursor-busy-revcursor
record_named cursor-busy-spinner

# After the turn, idle follow-up placeholder (not the splash).
i=0
while [ "$i" -lt 60 ]; do
  text="$(tmux_r capture-pane -p -t "$SESSION:cursor" 2>/dev/null || true)"
  case "$text" in
    *"Add a follow-up"*)
      case "$text" in
        *"ctrl+c to stop"*|*"Running"*) ;;
        *) break ;;
      esac
      ;;
  esac
  sleep 0.5
  i=$((i + 1))
done
# If follow-up never appeared, keep the earlier idle recapture.
if tmux_r capture-pane -p -t "$SESSION:cursor" | grep -q "Add a follow-up"; then
  record_named cursor-idle
fi

tmux_r kill-session -t "$SESSION" 2>/dev/null || true
sleep 0.3

# Trust dialog: no --trust, throwaway cwd so we do not prompt on this worktree.
trust_home="$(mktemp -d /tmp/muxa-fix-trust.XXXXXX)"
tmux_r new-session -d -s "$SESSION" -x 120 -y 32 -n cursor \
  "$(agent_cmd --force --workspace "$trust_home" --model composer-2.5-fast)"
wait_plain "Trust" 40 || wait_plain "trust" 10
record_named cursor-trust-dialog
rm -rf "$trust_home"

# Cropped unit snippets keep their .ansi bytes; sidecars complete the corpus.
stamp_static_composer_fixture "$DEST/256color-idle" snippet
stamp_static_composer_fixture "$DEST/claude-idle" claude
stamp_static_composer_fixture "$DEST/claude-idle-233" claude
stamp_static_composer_fixture "$DEST/claude-busy" claude
stamp_static_composer_fixture "$DEST/pi-idle" pi
stamp_static_composer_fixture "$DEST/pi-busy" pi
stamp_static_composer_fixture "$DEST/shell-prompt" shell
stamp_static_composer_fixture "$DEST/vim" vim
stamp_static_composer_fixture "$DEST/garbage" snippet

echo "done: fixtures in $DEST"
