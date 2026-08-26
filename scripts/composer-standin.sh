#!/usr/bin/env bash
# Composer stand-in for broker integration tests (muxa#111, muxa#116).
# Simulates a Cursor/Claude half-block composer TUI in a tmux pane.
#
# MUST run under bash 3.2+ (/bin/bash on macOS). Do not use fractional
# read -t — bash 3.2 rejects it (muxa#119). Idle polls use integer read -t 1;
# busy polls use sleep + non-blocking read -t 0.
#
# Test-only. Never attach to an operator pane (respawn-pane with this script
# on a live parent was the muxa#119 incident; debug copies were named
# parent-composer.sh under /tmp/muxa-debug.*).
set -uo pipefail

COMPOSER_LOG="${COMPOSER_LOG:-composer.log}"
COMPOSER_STATE="${COMPOSER_STATE:-composer.state}"

: >"$COMPOSER_LOG" 2>/dev/null || true
[ -f "$COMPOSER_STATE" ] || printf 'idle\n' >"$COMPOSER_STATE"

top=$'\033[38;2;38;38;38m▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄\033[0m'
bot=$'\033[38;2;38;38;38m▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\033[0m'

frame() {
  local row log out
  case "$1" in
  busy)  row=$'\033[48;2;38;38;38m \033[2m→ Add a follow-up   ctrl+c to stop\033[0m' ;;
  typed) row=$'\033[48;2;38;38;38m HUMANTYPING\033[0m' ;;
  cursor-typed) row=$'\033[48;2;38;38;38m \033[2m→ Plan, search, build anything\033[0mHUMANTYPING\033[0m' ;;
  *)     row=$'\033[48;2;38;38;38m \033[2m→ Plan, search, build anything\033[0m' ;;
  esac
  log="$(cat "$COMPOSER_LOG" 2>/dev/null || true)"
  out=$'\033[H\033[2J'
  [ -n "$log" ] && out="$out$log"$'\n'
  out="$out$top"$'\n'"$row"$'\n'"$bot"$'\n'" Composer 2.5 Fast"$'\n'" /tmp/fake-worktree"$'\n'
  printf '%s' "$out"
}

prev=""
while true; do
  st="$(cat "$COMPOSER_STATE" 2>/dev/null || true)"
  if [ "$st" != "$prev" ] || [ "$st" = busy ]; then
    prev="$st"
    frame "$st"
  fi
  if [ "$st" = busy ]; then
    sleep 0.05
    if IFS= read -r -t 0 line; then
      printf '%s\n' "$line" >>"$COMPOSER_LOG"
      frame "$st"
    fi
  elif IFS= read -r -t 1 line; then
    printf '%s\n' "$line" >>"$COMPOSER_LOG"
    frame "$st"
  fi
done
