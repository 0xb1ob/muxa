#!/usr/bin/env bash
# Bash 3.2 portability gate (muxa#119). Ubuntu CI ships bash 5 and will not
# catch fractional read -t or other bash-4-only constructs in shell fixtures.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
pass=0
fail=0

ok() { pass=$((pass + 1)); printf 'ok %s %s\n' "$pass" "$1"; }
bad() { fail=$((fail + 1)); printf 'not ok %s %s\n' "$((pass + fail))" "$1"; printf '  %s\n' "$2" >&2; }

# Shell sources that must stay bash-3.2-safe (macOS /bin/bash is 3.2.57).
scan=(
  "$ROOT/scripts/composer-standin.sh"
  "$ROOT/scripts/claude-composer-standin.sh"
  "$ROOT/scripts/muxa-hook.sh"
  "$ROOT/scripts/version-ldflags.sh"
  "$ROOT/install.sh"
  "$ROOT/tests/broker.sh"
  "$ROOT/tests/run.sh"
  "$ROOT/tests/e2e.sh"
  "$ROOT/tests/install.sh"
  "$ROOT/tests/tmux-facts.sh"
)

for f in "${scan[@]}"; do
  [ -f "$f" ] || { bad "scan target exists: $(basename "$f")" "missing $f"; continue; }

  if grep -E 'read[[:space:]].*-t[[:space:]]+"?[0-9]+\.[0-9]+' "$f" >/dev/null \
    || grep -E 'read[[:space:]]+-t[[:space:]]+"?[0-9]+\.[0-9]+' "$f" >/dev/null; then
    bad "no fractional read -t in $(basename "$f")" "$(grep -En 'read.*-t.*[0-9]+\.[0-9]+' "$f" | head -3)"
  else
    ok "no fractional read -t in $(basename "$f")"
  fi

  if grep -E 'declare[[:space:]]+-A|[[:space:]]-A[[:space:]]+[a-zA-Z_]' "$f" >/dev/null; then
    bad "no associative arrays in $(basename "$f")" "$(grep -En 'declare.*-A|\s-A\s' "$f" | head -3)"
  else
    ok "no associative arrays in $(basename "$f")"
  fi

  if grep -E '\$\{[^}]+\^\^|\$\{[^}]+\,\}' "$f" >/dev/null; then
    bad "no bash-4 parameter expansion in $(basename "$f")" "$(grep -En '\$\{[^}]+\^\^|\$\{[^}]+\,\}' "$f" | head -3)"
  else
    ok "no bash-4 parameter expansion in $(basename "$f")"
  fi
done

standin="$ROOT/scripts/composer-standin.sh"
[ -x "$standin" ] || chmod +x "$standin"
claude_standin="$ROOT/scripts/claude-composer-standin.sh"
[ -x "$claude_standin" ] || chmod +x "$claude_standin"

# Runtime probe under stock /bin/bash when the host provides bash 3.x.
if [ -x /bin/bash ]; then
  bash_major="$(/bin/bash --version 2>/dev/null | sed -n '1s/.*version \([0-9]*\).*/\1/p')"
  if [ -n "$bash_major" ] && [ "$bash_major" -lt 4 ] 2>/dev/null; then
    probe_dir="$(mktemp -d /tmp/muxa-bash32-probe.XXXXXX)"
    err="$probe_dir/err"
    log="$probe_dir/composer.log"
    state="$probe_dir/composer.state"
    printf 'idle\n' >"$state"
    : >"$log"
    for probe in "$standin" "$claude_standin"; do
      : >"$err"
      : >"$log"
      COMPOSER_LOG="$log" COMPOSER_STATE="$state" /bin/bash "$probe" 2>"$err" &
      probe_pid=$!
      sleep 0.6
      kill "$probe_pid" 2>/dev/null || true
      wait "$probe_pid" 2>/dev/null || true
      if [ -s "$err" ] && grep -qi 'invalid timeout specification' "$err"; then
        bad "$(basename "$probe") silent under /bin/bash $bash_major" "$(head -3 "$err")"
      else
        ok "$(basename "$probe") silent under /bin/bash $bash_major"
      fi
    done
    rm -rf "$probe_dir"
  else
    ok "runtime /bin/bash probe skipped (major=${bash_major:-unknown}, need <4)"
  fi
else
  ok "runtime /bin/bash probe skipped (no /bin/bash)"
fi

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
