#!/usr/bin/env bash
# Claude Code composer stand-in for broker integration tests (muxa#147).
# Simulates the rule-bordered composer a Claude Code pane draws: two
# full-width ─ rules with a "❯ " prompt row between them, cwd/model chrome
# below. Cursor Agent's half-block box lives in composer-standin.sh; the two
# shapes are different and the broker has to handle both.
#
# The hardware cursor is parked two cells into the prompt row after every
# frame. That is not decoration: it is where the cursor sits when the
# operator arrows or Ctrl-A back into a half-typed draft, and it is what
# makes emptyAtCursor — and therefore two-signal — call the pane free with
# the whole draft still on screen. Without it this test would pass on the
# two-signal gate and prove nothing about the composer gate.
#
# MUST run under bash 3.2+ (/bin/bash on macOS). No fractional read -t
# (muxa#119).
#
# Test-only. Never attach to an operator pane.
set -uo pipefail

COMPOSER_LOG="${COMPOSER_LOG:-claude-composer.log}"
COMPOSER_STATE="${COMPOSER_STATE:-claude-composer.state}"

: >"$COMPOSER_LOG" 2>/dev/null || true
[ -f "$COMPOSER_STATE" ] || printf 'idle\n' >"$COMPOSER_STATE"

rule=$'\033[38;5;244m────────────────────────────────────────\033[0m'

frame() {
  local row log out
  case "$1" in
  draft) row=$'\033[39m\xe2\x9d\xaf HUMANDRAFTING' ;;
  *)     row=$'\033[39m\xe2\x9d\xaf ' ;;
  esac
  log="$(cat "$COMPOSER_LOG" 2>/dev/null || true)"
  out=$'\033[H\033[2J'
  [ -n "$log" ] && out="$out$log"$'\n'
  out="$out$rule"$'\n'"$row"$'\n'"$rule"$'\n'" ~/w/muxa (main) | Opus 5 | Context: 4.0%"$'\n'" bypass permissions on"$'\n'
  # Park the cursor at the head of the prompt row: four lines up from where
  # the trailing newline left it, column 3 (cursor_x 2, just past "❯ ").
  out="$out"$'\033[4A\033[3G'
  printf '%s' "$out"
}

prev=""
while true; do
  st="$(cat "$COMPOSER_STATE" 2>/dev/null || true)"
  if [ "$st" != "$prev" ]; then
    prev="$st"
    frame "$st"
  fi
  if IFS= read -r -t 1 line; then
    printf '%s\n' "$line" >>"$COMPOSER_LOG"
    frame "$st"
  fi
done
