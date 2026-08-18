#!/usr/bin/env bash
# Composer-verdict fixtures. No tmux. Pipes each capture into muxa __composer-verdict.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PATH="$ROOT/bin:$PATH"
FIX="$ROOT/tests/fixtures/composer"

pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok %s %s\n' "$pass" "$1"; }
bad() { fail=$((fail + 1)); printf 'not ok %s %s\n' "$((pass + fail))" "$1"; printf '  %s\n' "$2" >&2; }

expect() {
  local file="$1" kind="$2" want="$3"
  local got
  got="$(muxa __composer-verdict --kind "$kind" <"$FIX/$file" 2>/dev/null || true)"
  got="$(printf '%s' "$got" | tr -d '\r\n')"
  if [ "$got" = "$want" ]; then
    ok "$file ($kind) -> $want"
  else
    bad "$file ($kind) -> $want" "got: $got"
  fi
}

[ -x "$ROOT/bin/muxa" ] || { echo "missing bin/muxa" >&2; exit 1; }

expect cursor-idle.ansi cursor empty
expect cursor-busy-spinner.ansi cursor pending
expect cursor-busy-revcursor.ansi cursor pending
expect cursor-revcursor-idle.ansi cursor empty
expect cursor-typed.ansi cursor pending
expect claude-idle.ansi claude empty
expect claude-busy.ansi claude pending
expect pi-idle.ansi pi empty
expect pi-busy.ansi pi pending
expect shell-prompt.ansi generic unknown
expect vim.ansi generic unknown
expect 256color-idle.ansi cursor empty
expect garbage.ansi cursor unknown

# garbage must not traceback
trace="$(muxa __composer-verdict --kind cursor <"$FIX/garbage.ansi" 2>&1 || true)"
case "$trace" in
  *Traceback*) bad "garbage.ansi no traceback" "$trace" ;;
  *) ok "garbage.ansi no traceback" ;;
esac

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
