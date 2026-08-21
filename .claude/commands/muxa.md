Send a message to another AI CLI in this tmux server.

Root pane: skill **muxa-parent**. Spawned workers: skill **muxa-worker**.

Usage: `/muxa <name> <text>` or `/muxa who`

Run **one** Bash command:

- Roster: `muxa who` / `muxa who --json`
- Pane read: `muxa tail <name>` (one-shot; never a poll)
- Send: `muxa send <name> <text from the rest of this invocation>`
- Send JSON: `muxa send --json <name> <text>` (queued id + pane)
- Dispatch: `muxa dispatch [--name NAME] [--cwd DIR] [--brief-file F] -- CMD…` (brief on stdin or `--brief-file`; stdout JSON)
- Broadcast: `muxa send --all <text>`
- No reply wanted: `muxa send --no-reply <name> <text>`

Do not poll. Incoming `[muxa]` turns are already delivered.
