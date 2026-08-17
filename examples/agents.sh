#!/usr/bin/env bash
# Start a tmux session with one window per coding CLI. Project hooks register each pane.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SESSION="${1:-agents}"
export PATH="$ROOT/bin:$PATH"

if tmux has-session -t "$SESSION" 2>/dev/null; then
  exec tmux attach -t "$SESSION"
fi

tmux new-session -d -s "$SESSION" -c "$ROOT" -n claude "export PATH='$ROOT/bin:\$PATH' MUXA_NAME=claude; exec claude"
command -v agent >/dev/null && tmux new-window -t "$SESSION" -c "$ROOT" -n cursor "export PATH='$ROOT/bin:\$PATH' MUXA_NAME=cursor; exec agent --trust --workspace '$ROOT'"
command -v omp >/dev/null && tmux new-window -t "$SESSION" -c "$ROOT" -n pi "export PATH='$ROOT/bin:\$PATH' MUXA_NAME=pi; exec omp"
tmux select-window -t "$SESSION:claude"
exec tmux attach -t "$SESSION"
