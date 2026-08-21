#!/usr/bin/env bash
# Optional project-scoped session-start. Invoked by .claude/settings.json and
# .cursor/hooks.json — never required for spawned panes.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export PATH="$ROOT/bin:$PATH"
if [ -n "${MUXA_HOOK_LOG:-}" ]; then
  printf '%s pane=%s cwd=%s args=%s\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    "${TMUX_PANE:-}" \
    "$PWD" \
    "$*" >>"$MUXA_HOOK_LOG"
fi
exec "$ROOT/bin/muxa" hook "$@"
