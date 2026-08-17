#!/usr/bin/env bash
# Install muxa onto PATH and wire Claude Code / Cursor / Oh My Pi user hooks.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
BIN="${MUXA_BIN_DIR:-$HOME/.local/bin}"
MUXA_HOME="${MUXA_HOME:-$HOME/.muxa}"

die() { printf 'muxa-install: %s\n' "$*" >&2; exit 1; }

mkdir -p "$BIN" "$MUXA_HOME"
ln -sfn "$ROOT/bin/muxa" "$BIN/muxa"
chmod +x "$ROOT/bin/muxa" "$ROOT/install.sh" "$ROOT/tests/run.sh"

# Skills: on-demand, not MCP. Progressive disclosure = fewer tokens.
mkdir -p "$HOME/.cursor/skills/muxa" "$HOME/.claude/skills/muxa"
cp "$ROOT/skills/muxa/SKILL.md" "$HOME/.cursor/skills/muxa/SKILL.md"
cp "$ROOT/skills/muxa/SKILL.md" "$HOME/.claude/skills/muxa/SKILL.md"

mkdir -p "$HOME/.claude/commands"
cp "$ROOT/integrations/claude/muxa.md" "$HOME/.claude/commands/muxa.md"

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

# Optional one-line reminder for AGENTS.md / CLAUDE.md — do not auto-append to repos.
cp "$ROOT/integrations/AGENTS.md.snippet" "$MUXA_HOME/AGENTS.md.snippet"

echo
echo "muxa $("$BIN/muxa" version) -> $BIN/muxa"
echo "Put $BIN on PATH if needed. Start each CLI inside tmux, then: muxa who"
echo "Optional: paste $MUXA_HOME/AGENTS.md.snippet into a project AGENTS.md"
