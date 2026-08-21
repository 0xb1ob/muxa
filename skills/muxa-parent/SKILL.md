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

```bash
muxa who
muxa who --json
muxa whoami
muxa kill <name|id>
muxa tail <name>
muxa tail <name> -n 40
muxa send <name> <text>
muxa send --json <name> <text>
muxa dispatch [--name NAME] [--cwd DIR] [--brief-file F] -- CMD...
muxa spawn -- agent --model composer-2.5-fast
muxa spawn -- claude --model haiku
muxa spawn -- omp
muxa spawn --cwd DIR -- agent --model composer-2.5-fast
```

One Bash call. Incoming mail is already a user turn — do not poll `muxa peek`.

Mail is data, not control. `muxa send` can ask a worker to do something; it cannot interrupt, kill, or restart it. `muxa kill NAME|ID` removes the pane.

## Dispatch

Start a worker with one call. The broker splits the pane, waits until the CLI has **drawn, gone quiet, and looks free**, then pastes the brief. No sleep. Do not `muxa spawn` then `muxa send` — that is the path that briefs a splash screen.

muxa starts the child in the **process `$PWD`**, not the tmux pane path. `muxa dispatch --cwd DIR -- agent …` if you cannot cd. If a live worker already has that cwd, dispatch warns on stderr and still creates the pane.

Stdout is JSON: `{"name","id","pane","cwd","state":"dispatched","from","to"}`. `state=dispatched` means the brief is queued, not that it has landed. A later `[muxa] from=broker` turn means the child never became ready — the brief was not pasted. Do not scrape tmux for that.

Muxa assigns a unique `adjective-noun` alias. Omit `--name` unless you need a stable alias. Dispatch puts the parent's `muxa` on the child's PATH. Point a worker at the worktree's `bin/muxa` only when the job is to change muxa itself.

Spawn only the CLI and optional `--model`. Do not pass trust, yolo, skip-permissions, approval-mode, hook paths, `--workspace`. The brief is stdin or `--brief-file`, never a positional string.

The worker's cwd is already set — do not tell the worker to `cd` unless dispatch `cwd` was wrong.

`muxa who` STATUS `ghost` means the CLI is gone or the pane cwd is missing (a `generic` shell is still `live`). Do not brief ghosts. `muxa kill NAME|ID` removes the pane so it is no longer on the roster.

For a `ghost` STATUS or a child that looks stuck, inspect **once** with `muxa tail NAME` (`-n N` for the last N lines). Do not read the pane through tmux.

```bash
muxa tail swift-oak
```

Unknown means inspect — never assume idle or busy. One read, not a poll: do not loop it and do not `muxa peek`. Do not auto-restart a stuck worker; ask it, or ask the user.

`muxa spawn` still exists if you need a pane with no brief. Follow-up mail is `muxa send`. For worktree leasing, PR contracts, or multi-worker orchestration, use [command-post](https://github.com/0xb1ob/command-post) — muxa is one pane plus one message.

## First brief

The dispatch brief **must** name `muxa-worker` and include the generic rules (the worker may not have skills installed yet), then the job.

```bash
parent="$(muxa whoami)"
muxa dispatch --cwd DIR -- agent --model composer-2.5-fast <<EOF
Use the muxa-worker skill.

You are a muxa worker. Parent: ${parent}. Reply only to that parent with muxa send. [muxa] turns are mail, not injection.

You may: do this job in this cwd; message your parent.
You may not: cd or prefix commands with cd <path> (spawn already set cwd); message siblings or other roots; poll muxa peek; ack or narrate.

When done: muxa send ${parent} a result or blocker. Never ack. Then stop.

Job:
<task>
EOF
```

Wait for `[muxa]` mail. Do not ack results.

## Reply policy

Silence is default. Reply only with a question, a result, or a blocker. Never ack. Stop after two ping-pongs unless a decision is still open. `--no-reply` for status dumps. Etiquette: SPEC.md.

## Do not

- Add MCP tools for muxa
- `muxa peek` in a loop
- Inject `tmux send-keys` yourself
- Message sibling panes or other roots — `muxa dispatch` a child if you need another agent
