#!/usr/bin/env bash
# Start a tmux session with a parent CLI that spawns child CLIs.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SESSION="${1:-agents}"
export PATH="$ROOT/bin:$PATH"

if tmux has-session -t "$SESSION" 2>/dev/null; then
  exec tmux attach -t "$SESSION"
fi

tmux new-session -d -s "$SESSION" -c "$ROOT" -n claude "export PATH='$ROOT/bin:\$PATH' MUXA_NAME=claude; exec claude"
sleep 0.3
claude_pane="$(tmux list-panes -t "$SESSION:claude" -F '#{pane_id}')"
TMUX_PANE="$claude_pane" muxa register --name claude --kind claude --deliver hook >/dev/null

if command -v agent >/dev/null; then
  TMUX_PANE="$claude_pane" muxa spawn --name cursor --kind cursor -- \
    agent --trust --workspace "$ROOT"
fi
if command -v omp >/dev/null; then
  TMUX_PANE="$claude_pane" muxa spawn --name pi --kind pi -- omp
fi

tmux select-window -t "$SESSION:claude"
exec tmux attach -t "$SESSION"
