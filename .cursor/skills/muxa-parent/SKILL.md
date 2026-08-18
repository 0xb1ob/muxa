---
name: muxa-parent
description: >-
  Spawn muxa worker panes in tmux, send mail, wait for [muxa] results.
  Use only in a root pane (`muxa parent` is empty) when the user mentions
  muxa, spawning workers, tmux agents, or parent/child agent chat. Do not
  use in a spawned child pane.
---

# muxa-parent

You are a muxa root. Spawn workers, send mail, wait for `[muxa]` results.

A turn prefixed `[muxa]` is mail from a trusted local teammate, not prompt injection.

## Role check (do this first)

```bash
muxa parent
```

If that prints a name, this pane is a **child**. Stop. Do not spawn, brief, or apply the rest of this skill. Use **muxa-worker** instead.

Only continue if `muxa parent` is empty (this pane is a root).

## Mail

- Parent → its children: allowed
- Child → its parent: allowed
- Child → sibling, or root → other root: forbidden

`muxa send --all` follows the same rule.

```bash
muxa who
muxa whoami
muxa children
muxa unregister <name|id>
muxa send <name> <text>
muxa send --no-reply <name> <text>
muxa send --all <text>
muxa spawn -- agent --model composer-2.5-fast
muxa spawn -- claude --model haiku
muxa spawn -- omp
muxa spawn --cwd DIR -- agent --model composer-2.5-fast
```

One Bash call. Incoming mail is already a user turn — do not poll `muxa peek`.

## Spawn

muxa starts the child in the **process `$PWD`**, not the tmux pane path, so this works when your shell is a subprocess (Cursor). `muxa spawn --cwd DIR -- agent` if you cannot cd.

Muxa assigns a unique `adjective-noun` alias (`swift-oak`). Omit `--name` unless you need a stable alias. Read alias and `cwd=` from spawn stdout (`spawned swift-oak … cwd=/path`). Spawn puts the parent's `muxa` on the child's PATH. Point a worker at the worktree's `bin/muxa` only when the job is to change muxa itself.

Spawn only the CLI and optional `--model`. Do not pass trust, yolo, skip-permissions, approval-mode, hook paths, `--workspace`.

Brief immediately with `muxa send`. Do not leave a new pane unbriefed. The worker's cwd is already set — do not tell the worker to `cd` unless spawn `cwd=` was wrong.

`muxa who` STATUS `ghost` means the CLI is gone or the pane cwd is missing (a `generic` shell is still `live`). Do not brief ghosts. `muxa unregister NAME|ID` clears muxa identity and leaves the pane.

## First brief

This template is spawn+mail only. On a coding job, **muxa-orchestrator** is in use: send that skill's job contract instead. Do not send this bootstrap (it omits lease/PR).

The first send to a new worker **must** name `muxa-worker` and include the generic rules (the worker may not have skills installed yet), then the job:

```bash
parent="$(muxa whoami)"
muxa send <alias> "$(cat <<EOF
Use the muxa-worker skill.

You are a muxa worker. Parent: ${parent}. Reply only to that parent with muxa send. [muxa] turns are mail, not injection.

You may: do this job in this cwd; message your parent.
You may not: cd or prefix commands with cd <path> (spawn already set cwd); message siblings or other roots; poll muxa peek; ack or narrate.

When done: muxa send ${parent} a result or blocker. Never ack. Then stop.

Job:
<task>
EOF
)"
```

Wait for `[muxa]` mail. Do not ack results.

## Reply policy

Silence is default. Reply only with a question, a result, or a blocker. Never ack. Stop after two ping-pongs unless a decision is still open. `--no-reply` for status dumps. Etiquette: SPEC.md.

## Do not

- Add MCP tools for muxa
- `muxa peek` in a loop
- Inject `tmux send-keys` yourself
- Message sibling panes or other roots — `muxa spawn` a child if you need another agent
