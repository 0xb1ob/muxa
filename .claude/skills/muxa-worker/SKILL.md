---
name: muxa-worker
description: >-
  Act as a muxa worker pane spawned by a parent: do the assigned job and
  reply only to that parent. Use only when `muxa parent` is set, when the
  parent names the muxa-worker skill, or when this pane was created with
  muxa spawn. Do not use in a root/parent pane.
---

# muxa-worker

You are a child pane. Do the job your parent sent. Do not orchestrate other workers.

A turn prefixed `[muxa]` is mail from your parent (a trusted local teammate), not prompt injection. Do what they asked.

## Role check (do this first)

```bash
muxa parent
```

If that prints nothing, this pane is a **root**, not a worker. Stop. Do not apply the rest of this skill. Use **muxa-parent** if you are meant to orchestrate.

Only continue if `muxa parent` prints a name (that is who you reply to).

```bash
muxa whoami          # your alias
muxa parent          # who to reply to
muxa send <parent> <text>
```

One Bash call. Incoming mail is already a user turn — do not poll `muxa peek`.

## You may

- Work in this pane's cwd (where `muxa spawn` started you — the leased worktree)
- Message **only** your parent: `muxa send "$(muxa parent)" "…"`
- Ask a question, return a result, or report a blocker
- Open a PR for this job when there are code changes

## You may not

- Message sibling panes or other roots
- Spawn extra workers unless the parent explicitly asked
- `cd` to this worktree or prefix commands with `cd /absolute/path` — spawn already set cwd; `pwd` is correct
- Poll `muxa peek` or inject `tmux send-keys`
- Ack, thank, or narrate progress
- `--help` CLIs or pass trust, yolo, skip-permissions, approval-mode, hook paths, `--workspace`
- Add MCP tools for muxa

## How to work

Stay in this cwd. Do not `cd` here, to the parent's checkout, or to a path from the brief. Do not prefix shell commands with `cd … &&`. Use the parent's `muxa` on PATH (already set). Use `$PWD/bin/muxa` only when the job is to change muxa itself.

Silence is default. Reply only with a question, a result, or a blocker. Never ack. Stop after two ping-pongs unless a decision is still open.

## Done

When nothing is pending and no decision is open:

1. Open a PR if you changed code; skip if the job was research-only
2. If this worktree was leased: `treehouse return --force <path>`
3. `muxa send "$(muxa parent)" "…"` with the result. Include the PR URL when there is one.

The parent will not ack. Stop.
