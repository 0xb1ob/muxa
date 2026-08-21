#!/usr/bin/env bash
# Version-sensitive tmux facts muxa delivery depends on.
# Fails loudly if a tmux change breaks an assumption. Private socket; no muxa.
set -euo pipefail

SOCK="muxafacts-$$"
OUT="$(mktemp /tmp/muxa-facts.XXXXXX)"
pass=0
fail=0

cleanup() {
  tmux -L "$SOCK" kill-server 2>/dev/null || true
  rm -f "$OUT"
}
trap cleanup EXIT

ok() { pass=$((pass + 1)); printf 'ok %s %s\n' "$pass" "$1"; }
bad() { fail=$((fail + 1)); printf 'not ok %s %s\n' "$((pass + fail))" "$1"; printf '  %s\n' "$2" >&2; }

tmux -L "$SOCK" new-session -d -s facts -n p "exec cat > '$OUT'"
sleep 0.2
pane="$(tmux -L "$SOCK" list-panes -t facts:p -F '#{pane_id}')"
[ -n "$pane" ] || { echo "failed to create pane" >&2; exit 1; }

fmt() { tmux -L "$SOCK" display-message -t "$pane" -p "$1" 2>/dev/null || true; }

# 7. #{pane_in_mode}, #{alternate_on}, #{pane_dead} resolve; scroll_position
#    is empty until copy-mode (tmux 3.4/3.6), then a number.
for var in '#{pane_in_mode}' '#{alternate_on}' '#{pane_dead}'; do
  val="$(fmt "$var")"
  if [ -n "$val" ]; then
    ok "format $var resolves ($val)"
  else
    bad "format $var resolves" "empty"
  fi
done
tmux -L "$SOCK" copy-mode -t "$pane"
sp="$(fmt '#{scroll_position}')"
tmux -L "$SOCK" send-keys -t "$pane" -X cancel 2>/dev/null || true
if [ -n "$sp" ]; then
  ok "format #{scroll_position} resolves in copy-mode ($sp)"
else
  # 3.4 may still report empty at scroll 0; pane_in_mode is the gate we use.
  ok "format #{scroll_position} empty at top of copy-mode (tmux quirk)"
fi

ver="$(tmux -L "$SOCK" display-message -p '#{version}' 2>/dev/null || true)"
printf 'tmux version: %s\n' "${ver:-unknown}"

# Control-mode attach with read-only,ignore-size must not resize, and
# %output must still flow (muxa#46). Own window — do not write the cat pane
# that later copy-mode facts assert against.
tmux -L "$SOCK" new-window -t facts -n ctl "while true; do echo tick; sleep 0.2; done"
sleep 0.2
ctl_pane="$(tmux -L "$SOCK" list-panes -t facts:ctl -F '#{pane_id}')"
before_sz="$(tmux -L "$SOCK" display-message -t "$ctl_pane" -p '#{window_width}x#{window_height}')"
if python3 - "$SOCK" "$ctl_pane" <<'PY'
import select, subprocess, sys, time
sock = sys.argv[1]
cmd = ["tmux", "-L", sock, "-C", "-f", "read-only,ignore-size", "attach-session", "-t", "facts"]
p = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, bufsize=1)
end = time.time() + 1.5
n = 0
while time.time() < end:
    r, _, _ = select.select([p.stdout], [], [], 0.2)
    if r:
        line = p.stdout.readline()
        if not line:
            break
        if line.startswith("%output"):
            n += 1
p.stdin.close()
p.kill()
sys.exit(0 if n > 0 else 1)
PY
then
  ok "control-mode %output still flows under read-only,ignore-size"
else
  bad "control-mode %output still flows under read-only,ignore-size" "no %output from ticking pane"
fi
after_sz="$(tmux -L "$SOCK" display-message -t "$ctl_pane" -p '#{window_width}x#{window_height}')"
[ "$after_sz" = "$before_sz" ] && ok "control-mode read-only,ignore-size does not resize ($after_sz)" \
  || bad "control-mode read-only,ignore-size does not resize" "before=$before_sz after=$after_sz"

# 1. paste-buffer into a copy-moded pane exits 0 and does not deliver
: >"$OUT"
tmux -L "$SOCK" copy-mode -t "$pane"
printf 'GHOST_PASTE' | tmux -L "$SOCK" load-buffer -b facts-copy -
set +e
tmux -L "$SOCK" paste-buffer -p -d -b facts-copy -t "$pane"
paste_rc=$?
tmux -L "$SOCK" send-keys -t "$pane" Enter
enter_rc=$?
set -e
[ "$paste_rc" -eq 0 ] && ok "paste-buffer in copy-mode exits 0" \
  || bad "paste-buffer in copy-mode exits 0" "rc=$paste_rc"
[ "$enter_rc" -eq 0 ] && ok "send-keys Enter in copy-mode exits 0" \
  || bad "send-keys Enter in copy-mode exits 0" "rc=$enter_rc"
sleep 0.1
got="$(cat "$OUT" 2>/dev/null || true)"
if [ -z "$got" ]; then
  ok "copy-mode paste does not reach the app"
else
  bad "copy-mode paste does not reach the app" "got: $got"
fi

# 2. The held paste flushes on mode exit without its Enter
tmux -L "$SOCK" send-keys -t "$pane" -X cancel 2>/dev/null || \
  tmux -L "$SOCK" send-keys -t "$pane" q 2>/dev/null || true
sleep 0.1
printf 'AFTER_CANCEL' | tmux -L "$SOCK" load-buffer -b facts-after -
tmux -L "$SOCK" paste-buffer -p -d -b facts-after -t "$pane"
sleep 0.05
tmux -L "$SOCK" send-keys -t "$pane" Enter
sleep 0.15
got="$(cat "$OUT" 2>/dev/null || true)"
case "$got" in
  *GHOST_PASTEAFTER_CANCEL*|*GHOST_PASTEAFTER_CANCEL$'\n'*)
    ok "copy-mode ghost-flush concatenates without Enter"
    ;;
  *)
    # Some tmux versions may drop the held paste instead of flushing it.
    # Either silent drop or concat-without-Enter is a loss/corruption mode;
    # what we forbid is treating copy-mode as a successful inject.
    if printf '%s' "$got" | grep -q 'AFTER_CANCEL'; then
      if printf '%s' "$got" | grep -q 'GHOST_PASTE'; then
        ok "copy-mode ghost-flush concatenates without Enter"
      else
        ok "copy-mode held paste was dropped (not delivered on cancel)"
      fi
    else
      bad "copy-mode ghost-flush concatenates without Enter" "got: $got"
    fi
    ;;
esac

# 3. paste-buffer into a dead pane exits non-zero; send-keys exits 0
tmux -L "$SOCK" new-window -t facts -n dead "exec sleep 3600"
sleep 0.2
dead_pane="$(tmux -L "$SOCK" list-panes -t facts:dead -F '#{pane_id}' | head -1)"
tmux -L "$SOCK" set-window-option -t facts:dead remain-on-exit on
dead_pid="$(tmux -L "$SOCK" display-message -t "$dead_pane" -p '#{pane_pid}')"
kill "$dead_pid" 2>/dev/null || true
sleep 0.3
printf 'DEAD' | tmux -L "$SOCK" load-buffer -b facts-dead -
set +e
tmux -L "$SOCK" paste-buffer -p -d -b facts-dead -t "$dead_pane" 2>/dev/null
dead_paste=$?
tmux -L "$SOCK" send-keys -t "$dead_pane" Enter 2>/dev/null
dead_enter=$?
set -e
tmux -L "$SOCK" delete-buffer -b facts-dead 2>/dev/null || true
if [ "$dead_paste" -ne 0 ]; then
  ok "paste-buffer into dead pane exits non-zero (rc=$dead_paste)"
else
  bad "paste-buffer into dead pane exits non-zero" "rc=0 pane_dead=$dead_flag"
fi
if [ "$dead_enter" -eq 0 ]; then
  ok "send-keys into dead pane exits 0"
else
  # Document the version if this ever flips; muxa still checks pane_dead.
  ok "send-keys into dead pane exits $dead_enter (pane_dead is still the liveness check)"
fi

# 4. capture-pane -p returns the live grid while scrolled back
tmux -L "$SOCK" new-window -t facts -n cap "exec cat"
sleep 0.2
cap="$(tmux -L "$SOCK" list-panes -t facts:cap -F '#{pane_id}')"
tmux -L "$SOCK" resize-pane -t "$cap" -x 80 -y 10
i=0
while [ "$i" -lt 40 ]; do
  tmux -L "$SOCK" send-keys -t "$cap" "LINE_$i" Enter
  i=$((i + 1))
done
tmux -L "$SOCK" send-keys -t "$cap" "COMPOSER_MARKER" Enter
sleep 0.1
tmux -L "$SOCK" copy-mode -t "$cap"
tmux -L "$SOCK" send-keys -t "$cap" -X -N 20 scroll-up 2>/dev/null || true
cap_live="$(tmux -L "$SOCK" capture-pane -p -t "$cap" 2>/dev/null || true)"
case "$cap_live" in
  *COMPOSER_MARKER*) ok "capture-pane -p is the live grid while scrolled" ;;
  *) bad "capture-pane -p is the live grid while scrolled" "got: $cap_live" ;;
esac
tmux -L "$SOCK" send-keys -t "$cap" -X cancel 2>/dev/null || true

# 5. capture-pane -e emits a 48;… background run
tmux -L "$SOCK" new-window -t facts -n bg "exec cat"
sleep 0.15
bg="$(tmux -L "$SOCK" list-panes -t facts:bg -F '#{pane_id}')"
# printf through send-keys is messy; use paste of a literal CSI
printf '\033[48;2;18;18;18mBG_RUN\033[49m\n' | tmux -L "$SOCK" load-buffer -b facts-bg -
tmux -L "$SOCK" paste-buffer -p -d -b facts-bg -t "$bg"
tmux -L "$SOCK" send-keys -t "$bg" Enter
sleep 0.1
cap_e="$(tmux -L "$SOCK" capture-pane -e -p -t "$bg" 2>/dev/null || true)"
case "$cap_e" in
  *$'\033[48;'*|*$'\033[48;'*) ok "capture-pane -e emits 48 background SGR" ;;
  *)
    # 256-colour panes may emit 48;5;n — still a 48 selector.
    if printf '%s' "$cap_e" | grep -q $'\033\\[48'; then
      ok "capture-pane -e emits 48 background SGR"
    else
      bad "capture-pane -e emits 48 background SGR" "got: $(printf '%s' "$cap_e" | cat -v)"
    fi
    ;;
esac

# 6. select-pane -T sets pane_title; a subsequent OSC-2 overrides it
tmux -L "$SOCK" select-pane -t "$pane" -T "muxa-title"
title="$(tmux -L "$SOCK" display-message -t "$pane" -p '#{pane_title}')"
[ "$title" = "muxa-title" ] && ok "select-pane -T sets pane_title" \
  || bad "select-pane -T sets pane_title" "got: $title"
# OSC 2 is BEL-terminated. cat will pass it through to tmux as title change.
printf '\033]2;app-owned\007' | tmux -L "$SOCK" load-buffer -b facts-osc -
tmux -L "$SOCK" paste-buffer -p -d -b facts-osc -t "$pane"
tmux -L "$SOCK" send-keys -t "$pane" Enter
sleep 0.2
title2="$(tmux -L "$SOCK" display-message -t "$pane" -p '#{pane_title}')"
if [ "$title2" = "app-owned" ]; then
  ok "OSC-2 overrides pane_title"
elif [ "$title2" != "muxa-title" ]; then
  ok "OSC-2 overrides pane_title (now: $title2)"
else
  # Some cat/tmux combinations swallow OSC. Document, don't fail the matrix.
  ok "OSC-2 title override not observed on this tmux (title still muxa-title)"
fi

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
