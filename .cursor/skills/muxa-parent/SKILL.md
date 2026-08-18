---
name: muxa-parent
description: >-
  Orchestrate muxa worker panes in tmux: spawn CLIs, brief jobs, wait for
  [muxa] results. Use only in a root pane (`muxa parent` is empty) when the
  user mentions muxa, spawning workers, tmux agents, or parent/child agent
  chat. Do not use in a spawned child pane.
---

# muxa-parent

You are the orchestrator. Spawn workers, brief them, wait for `[muxa]` results. Do not do their jobs.

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

1. Isolate parallel code work with **one worktree per worker** (`treehouse get --lease` if available, else `git worktree add`). `cd` into that directory, then spawn. muxa starts the child in the **process `$PWD`**, not the tmux pane path, so this works when your shell is a subprocess (Cursor). `muxa spawn --cwd DIR -- agent` if you cannot cd.
2. Spawn **only** the CLI and optional `--model`. Muxa assigns a unique `adjective-noun` alias (`swift-oak`). Read alias and `cwd=` from spawn stdout (`spawned swift-oak … cwd=/path`). Confirm `cwd=` is the worktree before briefing. Do not pass `--name`.
3. Do not `--help` child CLIs. Do not pass trust, yolo, skip-permissions, approval-mode, hook paths, `--workspace`, or other per-CLI launch flags.
4. Spawn puts the parent's `muxa` on the child's PATH. Point a worker at the worktree's `bin/muxa` only when the job is to change muxa itself.
5. Brief immediately with `muxa send` using the template below. The worker's cwd is already the worktree — do not paste that path into the job, and do not tell the worker to `cd` unless spawn `cwd=` was wrong.
6. Do not leave this checkout on a branch the worker must attach (`feat/…`). Stay on `main` (or another branch) so the leased worktree can `git checkout` the job branch.

`muxa who` STATUS `ghost` means the CLI is gone or the pane cwd is missing (a `generic` shell is still `live`). Do not brief ghosts. `muxa unregister NAME|ID` clears muxa identity and leaves the pane. After the lease is returned you may also kill the pane.

## First brief

The first send to a new worker **must** name `muxa-worker` and include the generic rules (the worker may not have skills installed yet), then the job:

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

## Reply policy

Silence is default. Reply only with a question, a result, or a blocker. Never ack. Stop after two ping-pongs unless a decision is still open. `--no-reply` for status dumps.

## Do not

- Add MCP tools for muxa
- `muxa peek` in a loop
- Inject `tmux send-keys` yourself
- Message sibling panes or other roots — `muxa spawn` a child if you need another agent
- Do the worker's job in this pane
