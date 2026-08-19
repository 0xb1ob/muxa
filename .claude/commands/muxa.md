Send a message to another AI CLI in this tmux server.

Root pane: skill **muxa-parent**. Spawned workers: skill **muxa-worker**.

Usage: `/muxa <name> <text>` or `/muxa who`

Run **one** Bash command:

- Roster: `muxa who`
- Send: `muxa send <name> <text from the rest of this invocation>`
- Broadcast: `muxa send --all <text>`
- No reply wanted: `muxa send --no-reply <name> <text>`

Do not poll. Incoming `[muxa]` turns are already delivered.
