#!/usr/bin/env bash
# Record one composer fixture: capture-pane + tmux cursor, then a second
# capture ~250ms later so quiescence is representable.
#
# Usage (sourced): record_composer_fixture SOCKET PANE DEST [delay_ms]
# Writes DEST.ansi, DEST.meta, DEST.t2.ansi.
# DEST is a path without suffix (e.g. tests/fixtures/composer/cursor-idle).
#
# Do not enable set -e here: tests/e2e.sh sources this under `set -u` only.

record_composer_fixture() {
  local sock="$1" target="$2" dest="$3"
  local delay_ms="${4:-250}"
  local dir meta delay_s

  dir="$(dirname "$dest")"
  mkdir -p "$dir" || return 1

  # Capture first, then cursor, so .meta describes the .ansi that was written.
  # Two tmux calls cannot be the same instant; they are adjacent on purpose.
  tmux -L "$sock" capture-pane -e -p -t "$target" >"$dest.ansi" || return 1
  meta="$(tmux -L "$sock" display-message -t "$target" -p \
    $'cursor_y=#{cursor_y}\ncursor_x=#{cursor_x}\npane_width=#{pane_width}\npane_height=#{pane_height}')" || return 1
  {
    printf '%s\n' "$meta"
    printf 'delay_ms=%s\n' "$delay_ms"
  } >"$dest.meta" || return 1

  delay_s="$(LC_ALL=C awk -v ms="$delay_ms" 'BEGIN { printf "%.3f", ms / 1000 }')"
  sleep "$delay_s"
  tmux -L "$sock" capture-pane -e -p -t "$target" >"$dest.t2.ansi" || return 1
}

# Stamp sidecars for a cropped/static .ansi that was not live-recaptured.
# t2 is a byte copy (the snippet has no time dimension). Cursor is measured
# from a pane whose height matches the file so the coords sit on that grid.
stamp_static_composer_fixture() {
  local dest="$1" kind="${2:-snippet}"
  local sock="muxastamp$$"
  local lines cols pane cy cx w h
  [ -f "$dest.ansi" ] || return 1
  cp "$dest.ansi" "$dest.t2.ansi" || return 1
  lines="$(wc -l <"$dest.ansi" | tr -d ' ')"
  [ "$lines" -ge 1 ] || lines=1
  cols="$(awk '{ n=length($0)+1; if (n>c) c=n } END { print c+8 }' "$dest.ansi")"
  [ "$cols" -ge 20 ] || cols=20
  tmux -L "$sock" new-session -d -s stamp -x "$cols" -y "$lines" \
    "unset NO_COLOR FORCE_COLOR; export TERM=xterm-256color; cat '$dest.ansi'; exec sleep 30" || return 1
  sleep 0.15
  pane="$(tmux -L "$sock" list-panes -t stamp -F '#{pane_id}')"
  cy="$(tmux -L "$sock" display-message -t "$pane" -p '#{cursor_y}')"
  cx="$(tmux -L "$sock" display-message -t "$pane" -p '#{cursor_x}')"
  w="$(tmux -L "$sock" display-message -t "$pane" -p '#{pane_width}')"
  h="$(tmux -L "$sock" display-message -t "$pane" -p '#{pane_height}')"
  tmux -L "$sock" kill-server 2>/dev/null || true
  printf 'cursor_y=%s\ncursor_x=%s\npane_width=%s\npane_height=%s\ndelay_ms=250\norigin=static\nkind=%s\n' \
    "$cy" "$cx" "$w" "$h" "$kind" >"$dest.meta"
}
