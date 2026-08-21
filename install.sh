#!/usr/bin/env bash
# Install muxa onto PATH and wire Claude Code / Cursor / Oh My Pi user hooks.
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
# Go is not required: muxa-broker is downloaded as a release asset.
set -euo pipefail

MUXA_REPO="${MUXA_REPO:-https://github.com/0xb1ob/muxa.git}"
MUXA_REF="${MUXA_REF:-main}"
MUXA_HOME="${MUXA_HOME:-$HOME/.muxa}"
BIN="${MUXA_BIN_DIR:-$HOME/.local/bin}"

die() { printf 'muxa-install: %s\n' "$*" >&2; exit 1; }

is_muxa_tree() {
  [ -n "${1:-}" ] && [ -f "$1/bin/muxa" ] && [ -f "$1/install.sh" ] && [ -d "$1/skills/muxa-parent" ]
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

mkdir -p "$BIN"
ln -sfn "$ROOT/bin/muxa" "$BIN/muxa"
chmod +x "$ROOT/bin/muxa" "$ROOT/install.sh" "$ROOT/tests/run.sh"

# Go broker. Never compiled here: Go is not an install-time dependency.
# The binary is a release asset, verified against that release's SHA256SUMS.
broker_platform() {
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

# stdout: path of a verified broker binary in $1 (a scratch dir). Nonzero on
# any failure — an unverified daemon binary is never installed.
download_broker() {
  local tmp="$1" plat asset base bin want got
  plat="$(broker_platform)" || {
    printf 'muxa-install: no muxa-broker build for %s/%s\n' "$(uname -s)" "$(uname -m)" >&2
    return 1
  }
  asset="muxa-broker-$plat"
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
    printf 'muxa-install: %s/SHA256SUMS missing; refusing unverified broker\n' "$base" >&2
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

if [ -d "$ROOT/broker" ]; then
  _tmp="$(mktemp -d "${TMPDIR:-/tmp}/muxa-broker.XXXXXX")"
  if _bin="$(download_broker "$_tmp")" && [ -s "$_bin" ]; then
    install -m 755 "$_bin" "$ROOT/bin/muxa-broker"
    if [ "$(uname -s)" = Darwin ]; then
      # darwin 25+ SIGKILLs quarantined / unsigned Mach-O.
      xattr -c "$ROOT/bin/muxa-broker" 2>/dev/null || true
      codesign -s - --force --timestamp=none "$ROOT/bin/muxa-broker" 2>/dev/null || true
    fi
    ln -sfn "$ROOT/bin/muxa-broker" "$BIN/muxa-broker"
    printf 'muxa-install: muxa-broker -> %s\n' "$ROOT/bin/muxa-broker"
  elif [ -x "$ROOT/bin/muxa-broker" ]; then
    ln -sfn "$ROOT/bin/muxa-broker" "$BIN/muxa-broker"
    printf 'muxa-install: keeping existing %s\n' "$ROOT/bin/muxa-broker" >&2
  else
    printf 'muxa-install: no muxa-broker installed; muxa send will fail closed\n' >&2
  fi
  rm -rf "$_tmp"
  unset _tmp _bin
fi

# Skills: on-demand, not MCP. Progressive disclosure = fewer tokens.
# Global so any project can load muxa-parent / muxa-worker.
rm -rf "$HOME/.cursor/skills/muxa" "$HOME/.claude/skills/muxa" "$HOME/.agents/skills/muxa"
rm -rf "$HOME/.cursor/skills/muxa-orchestrator" "$HOME/.claude/skills/muxa-orchestrator" "$HOME/.agents/skills/muxa-orchestrator"
for skill in muxa-parent muxa-worker; do
  for dest in "$HOME/.cursor/skills/$skill" "$HOME/.claude/skills/$skill" "$HOME/.agents/skills/$skill"; do
    mkdir -p "$dest"
    cp "$ROOT/skills/$skill/SKILL.md" "$dest/SKILL.md"
  done
done

# Claude Code user hooks — merge without clobbering unrelated settings.
python3 - "$HOME/.claude/settings.json" "$BIN/muxa" <<'PY'
import json, os, sys
path, muxa = sys.argv[1], sys.argv[2]
os.makedirs(os.path.dirname(path), exist_ok=True)
try:
    with open(path) as f:
        data = json.load(f)
except FileNotFoundError:
    data = {}
except json.JSONDecodeError:
    print("muxa-install: ~/.claude/settings.json is not JSON; skip Claude merge", file=sys.stderr)
    sys.exit(0)

cmd = muxa + " hook"
hooks = data.setdefault("hooks", {})

def cmd_entry(event, extra=""):
    c = cmd + " " + event + extra
    return [{"hooks": [{"type": "command", "command": c}]}]

wanted = {
    "SessionStart": cmd_entry("session-start", " --kind claude"),
    "SessionEnd": cmd_entry("session-end"),
    "PreToolUse": cmd_entry("busy"),
    "PermissionRequest": cmd_entry("blocked"),
    "Stop": cmd_entry("stop", " --format claude"),
}

changed = False
for event, entries in wanted.items():
    blob = json.dumps(hooks.get(event), sort_keys=True)
    if "muxa hook" not in blob:
        hooks[event] = entries + (hooks.get(event) or [])
        changed = True
if changed:
    tmp = path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(data, f, indent=2)
        f.write("\n")
    os.replace(tmp, path)
    print("merged Claude Code hooks ->", path)
else:
    print("Claude Code hooks already mention muxa")
PY

# Cursor user hooks
python3 - "$HOME/.cursor/hooks.json" "$BIN/muxa" <<'PY'
import json, os, sys
path, muxa = sys.argv[1], sys.argv[2]
os.makedirs(os.path.dirname(path), exist_ok=True)
try:
    with open(path) as f:
        data = json.load(f)
except FileNotFoundError:
    data = {"version": 1, "hooks": {}}
except json.JSONDecodeError:
    print("muxa-install: ~/.cursor/hooks.json is not JSON; skip Cursor merge", file=sys.stderr)
    sys.exit(0)

data.setdefault("version", 1)
hooks = data.setdefault("hooks", {})
wanted = {
    "sessionStart": [{"command": muxa + " hook session-start --kind cursor"}],
    "sessionEnd": [{"command": muxa + " hook session-end"}],
    "preToolUse": [{"command": muxa + " hook busy"}],
    "stop": [{"command": muxa + " hook stop --format cursor", "loop_limit": None}],
    "afterAgentResponse": [{"command": muxa + " hook idle"}],
}
changed = False
for event, entries in wanted.items():
    blob = json.dumps(hooks.get(event), sort_keys=True)
    if "muxa hook" not in blob:
        hooks[event] = entries + (hooks.get(event) or [])
        changed = True
if changed:
    tmp = path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(data, f, indent=2)
        f.write("\n")
    os.replace(tmp, path)
    print("merged Cursor hooks ->", path)
else:
    print("Cursor hooks already mention muxa")
PY

# Oh My Pi / pi extension (copy, do not merge JS)
for dest in "$HOME/.omp/agent/extensions" "$HOME/.pi/agent/extensions"; do
  mkdir -p "$dest"
  cp "$ROOT/hooks/pi/muxa.ts" "$dest/muxa.ts"
  echo "installed pi extension -> $dest/muxa.ts"
done

echo
echo "muxa $("$BIN/muxa" version) -> $BIN/muxa  (repo $ROOT)"
echo "Skills muxa-parent / muxa-worker -> ~/.cursor/skills, ~/.claude/skills, ~/.agents/skills"
echo "Put $BIN on PATH if needed. Start each CLI inside tmux, then: muxa who"
