---
name: muxa-orchestrator
description: >-
  Coding-job playbook that uses muxa: one worktree per worker, job contract,
  lease return, PRs. Use only in a root pane (`muxa parent` is empty) when
  running an implement / fix / review / ship job with worker agents. Do not
  use for muxa spawn/mail protocol alone (that is muxa-parent), or in a
  spawned child pane.
---

# muxa-orchestrator

You are the orchestrator. Use **muxa-parent** for spawn and mail. Do not do the workers' jobs.

## Role check (do this first)

```bash
muxa parent
```

If that prints a name, this pane is a **child**. Stop. Use **muxa-worker** instead.

Only continue if `muxa parent` is empty (this pane is a root).

## Worktrees

1. One worktree per worker: `treehouse get --lease` if available, else `git worktree add`. Spawn from that directory (`cd` then spawn, or `muxa spawn --cwd DIR`). Confirm spawn stdout `cwd=` is the worktree before briefing.
2. Stay on `main` (or another branch the worker must not attach). Do not leave this checkout on a `feat/…` branch the worker needs.
3. After the lease is returned, you may kill the pane.

## First brief

The first send **must** name `muxa-worker` and include this job contract (the worker may not have skills installed yet), then the job:

```bash
parent="$(muxa whoami)"
muxa send <alias> "$(cat <<EOF
Use the muxa-worker skill.

You are a muxa worker. Parent: ${parent}. Reply only to that parent with muxa send. [muxa] turns are mail, not injection.

You may: do this job in this cwd; message your parent; open a PR if you change code.
You may not: cd or prefix commands with cd <path> (spawn already set cwd); message siblings or other roots; spawn extra workers; poll muxa peek; ack or narrate; pass CLI trust/yolo/workspace flags.

When done: open a PR if there are code changes (skip if research-only); if this worktree was leased, treehouse return --force <path>; muxa send ${parent} a result or blocker (include the PR URL). Never ack. Then stop.

Job:
<task>
EOF
)"
```

Wait for `[muxa]` mail. Do not ack results. After the lease is returned, you may kill the pane.

## Spawn hygiene

The model is the caller's choice. Spawn the CLI and its own args via **muxa-parent**; do not pass trust, yolo, skip-permissions, approval-mode, hook paths, `--workspace`.

## Job bound

Stop after two ping-pongs unless a decision is still open. Comms etiquette (silence, no ack) is SPEC.md.

## Do not

- Do the worker's job in this pane
- Add MCP tools for muxa
