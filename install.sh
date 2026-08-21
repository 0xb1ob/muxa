#!/usr/bin/env bash
# Install muxa onto PATH and copy muxa-parent / muxa-worker skills.
#
# Fresh machine (copy-paste):
#   curl -fsSL https://raw.githubusercontent.com/0xb1ob/muxa/main/install.sh | bash
#
# That clones this repo into ~/.muxa, then continues. From a checkout:
#   ./install.sh
#
# Env: MUXA_HOME (default ~/.muxa) MUXA_REPO MUXA_REF MUXA_BIN_DIR
#      MUXA_BROKER_VERSION (release tag, default latest) MUXA_BROKER_URL
#      MUXA_BROKER_BASE_URL MUXA_BROKER_SKIP_VERIFY
#
# Go is not required: muxa is downloaded as a release asset.
# A live daemon is stopped before the binary is replaced (overwriting a
# running Mach-O on darwin 25 SIGKILLs it), then started again so the new
# process re-adopts pending/ done/ in the broker dir.
set -euo pipefail

MUXA_REPO="${MUXA_REPO:-https://github.com/0xb1ob/muxa.git}"
MUXA_REF="${MUXA_REF:-main}"
MUXA_HOME="${MUXA_HOME:-$HOME/.muxa}"
BIN="${MUXA_BIN_DIR:-$HOME/.local/bin}"

die() { printf 'muxa-install: %s\n' "$*" >&2; exit 1; }

is_muxa_tree() {
  [ -n "${1:-}" ] && [ -f "$1/install.sh" ] && [ -d "$1/skills/muxa-parent" ]
}

# True if dir is empty or only files the previous installer left behind.
is_stale_muxa_home() {
  local f
  [ -d "$1" ] || return 1
  [ ! -d "$1/.git" ] || return 1
  for f in "$1"/* "$1"/.[!.]*; do
    [ -e "$f" ] || continue
    case "$(basename "$f")" in
      .DS_Store) ;;
      *) return 1 ;;
    esac
  done
  return 0
}

# Directory containing this script, if it is a real file (not `curl | bash`).
script_dir() {
  local src="${BASH_SOURCE[0]:-}"
  [ -n "$src" ] && [ -f "$src" ] || return 1
  ( cd "$(dirname "$src")" && pwd )
}

ensure_muxa_home() {
  command -v git >/dev/null 2>&1 || die "git is required to install muxa"
  export GIT_TERMINAL_PROMPT=0

  if is_muxa_tree "$MUXA_HOME" && [ -d "$MUXA_HOME/.git" ]; then
    printf 'muxa-install: updating %s (%s)\n' "$MUXA_HOME" "$MUXA_REF"
    git -C "$MUXA_HOME" fetch --depth 1 origin "$MUXA_REF"
    # Cache, not a checkout. Depth-1 fetch is not ff-mergeable after a squash.
    git -C "$MUXA_HOME" checkout -q -B "$MUXA_REF" "origin/$MUXA_REF"
    git -C "$MUXA_HOME" reset --hard "origin/$MUXA_REF"
    return 0
  fi

  if [ -e "$MUXA_HOME" ]; then
    if is_stale_muxa_home "$MUXA_HOME"; then
      rm -rf "$MUXA_HOME"
    else
      die "$MUXA_HOME exists and is not a muxa checkout; move it aside"
    fi
  fi

  printf 'muxa-install: cloning %s (%s) -> %s\n' "$MUXA_REPO" "$MUXA_REF" "$MUXA_HOME"
  git clone --depth 1 --branch "$MUXA_REF" "$MUXA_REPO" "$MUXA_HOME"
}

HERE="$(script_dir || true)"
if is_muxa_tree "$HERE"; then
  ROOT="$HERE"
else
  ensure_muxa_home
  exec "$MUXA_HOME/install.sh"
fi

mkdir -p "$BIN" "$ROOT/bin"
chmod +x "$ROOT/install.sh" "$ROOT/tests/run.sh" 2>/dev/null || true

muxa_platform() {
  local os arch
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) return 1 ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) return 1 ;;
  esac
  printf '%s-%s' "$os" "$arch"
}

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 2 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    return 1
  fi
}

sha256_of() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    return 1
  fi
}

# stdout: path of a verified muxa binary in $1 (a scratch dir). Nonzero on
# any failure — an unverified binary is never installed.
download_muxa() {
  local tmp="$1" plat asset base bin want got
  plat="$(muxa_platform)" || {
    printf 'muxa-install: no muxa build for %s/%s\n' "$(uname -s)" "$(uname -m)" >&2
    return 1
  }
  asset="muxa-$plat"
  bin="$tmp/$asset"

  if [ -n "${MUXA_BROKER_URL:-}" ]; then
    fetch "$MUXA_BROKER_URL" "$bin" || return 1
    printf '%s' "$bin"
    return 0
  fi

  base="${MUXA_BROKER_BASE_URL:-https://github.com/0xb1ob/muxa/releases}"
  if [ -n "${MUXA_BROKER_VERSION:-}" ]; then
    base="$base/download/$MUXA_BROKER_VERSION"
  else
    base="$base/latest/download"
  fi

  fetch "$base/$asset" "$bin" || {
    printf 'muxa-install: cannot download %s/%s\n' "$base" "$asset" >&2
    return 1
  }

  if [ "${MUXA_BROKER_SKIP_VERIFY:-0}" = 1 ]; then
    printf '%s' "$bin"
    return 0
  fi
  fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || {
    printf 'muxa-install: %s/SHA256SUMS missing; refusing unverified muxa\n' "$base" >&2
    return 1
  }
  want="$(awk -v a="$asset" '$2 == a || $2 == "*" a {print $1}' "$tmp/SHA256SUMS" | head -1)"
  got="$(sha256_of "$bin" || true)"
  if [ -z "$want" ] || [ -z "$got" ] || [ "$want" != "$got" ]; then
    printf 'muxa-install: checksum mismatch for %s (want=%s got=%s)\n' "$asset" "${want:-none}" "${got:-none}" >&2
    return 1
  fi
  printf '%s' "$bin"
}

# Same layout as muxa broker_setup_paths / runtime_root.
install_broker_dir() {
  local base pid
  if [ -n "${MUXA_BROKER_DIR:-}" ]; then
    printf '%s' "$MUXA_BROKER_DIR"
    return 0
  fi
  base="${XDG_RUNTIME_DIR:-/tmp/muxa-${UID:-$(id -u)}}"
  if [ -n "${MUXA_TMUX_SOCKET:-}" ]; then
    pid="$(tmux -L "$MUXA_TMUX_SOCKET" list-sessions -F '#{pid}' 2>/dev/null | head -1 || true)"
  else
    pid="$(tmux list-sessions -F '#{pid}' 2>/dev/null | head -1 || true)"
  fi
  [ -n "$pid" ] || pid=0
  printf '%s/muxa/%s/broker' "$base" "$pid"
}

install_broker_pidfile() {
  if [ -n "${MUXA_BROKER_PID:-}" ]; then
    printf '%s' "$MUXA_BROKER_PID"
    return 0
  fi
  printf '%s/broker.pid' "$(install_broker_dir)"
}

# SIGTERM the pidfile pid, matching muxa broker stop. Do not rm pending/,
# done/, or the broker dir — the next start re-adopts that queue.
stop_running_broker() {
  local pidfile sock pid i
  pidfile="$(install_broker_pidfile)"
  sock="${MUXA_BROKER_SOCK:-$(dirname "$pidfile")/broker.sock}"
  [ -f "$pidfile" ] || return 0
  pid="$(tr -d ' \t\n' <"$pidfile" 2>/dev/null || true)"
  if [ -n "$pid" ] && [ "$pid" -gt 1 ] 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    i=0
    while [ "$i" -lt 50 ]; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.1
      i=$((i + 1))
    done
    if kill -0 "$pid" 2>/dev/null; then
      printf 'muxa-install: broker pid %s still running after SIGTERM\n' "$pid" >&2
    else
      printf 'muxa-install: stopped broker pid=%s\n' "$pid"
    fi
  fi
  rm -f "$pidfile" "$sock"
}

# After the new muxa is on PATH. Skip without tmux: curl|bash on a fresh
# machine has no session to attach a daemon to. `muxa broker start` later.
start_installed_broker() {
  local muxa
  if [ -x "$BIN/muxa" ]; then
    muxa="$BIN/muxa"
  elif [ -x "$ROOT/bin/muxa" ]; then
    muxa="$ROOT/bin/muxa"
  else
    printf 'muxa-install: muxa not on PATH yet; start the broker later with: muxa broker start\n' >&2
    return 0
  fi
  if [ -z "${TMUX:-}" ] && [ -z "${MUXA_TMUX_SOCKET:-}" ]; then
    printf 'muxa-install: not in tmux; start the broker later with: muxa broker start\n'
    return 0
  fi
  PATH="$BIN:$PATH" "$muxa" broker start || \
    printf 'muxa-install: could not start muxa-broker; run: muxa broker start\n' >&2
  return 0
}

_tmp="$(mktemp -d "${TMPDIR:-/tmp}/muxa-bin.XXXXXX")"
if _bin="$(download_muxa "$_tmp")" && [ -s "$_bin" ]; then
  # Stop first, then replace. A live inode overwrite SIGKILLs on darwin 25.
  stop_running_broker
  install -m 755 "$_bin" "$ROOT/bin/muxa"
  if [ "$(uname -s)" = Darwin ]; then
    xattr -c "$ROOT/bin/muxa" 2>/dev/null || true
    codesign -s - --force --timestamp=none "$ROOT/bin/muxa" 2>/dev/null || true
  fi
  ln -sfn "$ROOT/bin/muxa" "$BIN/muxa"
  printf 'muxa-install: muxa -> %s\n' "$ROOT/bin/muxa"
elif [ -x "$ROOT/bin/muxa" ]; then
  ln -sfn "$ROOT/bin/muxa" "$BIN/muxa"
  printf 'muxa-install: keeping existing %s\n' "$ROOT/bin/muxa" >&2
else
  printf 'muxa-install: no muxa installed; muxa send will fail closed\n' >&2
fi
rm -rf "$_tmp"
unset _tmp _bin
if [ -x "$BIN/muxa" ] || [ -x "$ROOT/bin/muxa" ]; then
  start_installed_broker
fi

# Skills: on-demand, not MCP. Progressive disclosure = fewer tokens.
# Global so any project can load muxa-parent / muxa-worker.
rm -rf "$HOME/.cursor/skills/muxa" "$HOME/.claude/skills/muxa" "$HOME/.agents/skills/muxa"
for skill in muxa-parent muxa-worker; do
  for dest in "$HOME/.cursor/skills/$skill" "$HOME/.claude/skills/$skill" "$HOME/.agents/skills/$skill"; do
    mkdir -p "$dest"
    cp "$ROOT/skills/$skill/SKILL.md" "$dest/SKILL.md"
  done
done

echo
if [ -x "$BIN/muxa" ]; then
  echo "muxa $("$BIN/muxa" version 2>/dev/null || true) -> $BIN/muxa  (repo $ROOT)"
elif [ -x "$ROOT/bin/muxa" ]; then
  echo "muxa $("$ROOT/bin/muxa" version 2>/dev/null || true) -> $ROOT/bin/muxa  (repo $ROOT)"
else
  echo "muxa not installed (repo $ROOT)"
fi
echo "Skills muxa-parent / muxa-worker -> ~/.cursor/skills, ~/.claude/skills, ~/.agents/skills"
echo "Put $BIN on PATH if needed. Start each CLI inside tmux, then: muxa who"
