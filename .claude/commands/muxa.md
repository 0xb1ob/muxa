Send a message to another AI CLI in this tmux server.

Root pane: skill **muxa-parent**. Spawned workers: skill **muxa-worker**.

Usage: `/muxa <name> <text>` or `/muxa who`

Run **one** Bash command:

- Roster: `muxa who` / `muxa who --json`
- Pane read: `muxa tail <name>` (one-shot; never a poll)
- Kill pane: `muxa kill <name>` (removes the pane)
- Adopt orphans: `muxa adopt <dead-alias>` (root only; re-parent dead parent's children)
- Send: `muxa send <name> <text from the rest of this invocation>`
- Send from file: `muxa send --file <path> <name>`
- Send JSON: `muxa send --json <name> <text>` (queued id + pane)
- Dispatch: `muxa dispatch [--name NAME] [--cwd DIR] [--brief-file F] -- CMD…` (brief on stdin or `--brief-file`; stdout JSON)
- No reply wanted: `muxa send --no-reply <name> <text>`

Do not poll. Incoming `[muxa]` turns are already delivered.
