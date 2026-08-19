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

## Intake

Classify every job **before** you spawn, on both axes. Do not blur them.

- **kind** — `ship` (changes code) or `research` (reads and reports; changes nothing)
- **delivery** — `pr`, `local`, or `pipeline`

```bash
muxa jobs add JOB kind=ship delivery=pr
```

Evidence is not authorization. A research or scout result never starts an implementation by itself; a ship job needs its own authorization from the caller.

When a scout should now build, **promote it**: same worker, same worktree, new brief. Do not spawn a duplicate.

## Worktrees

1. One worktree per worker: `treehouse get --lease` if available, else `git worktree add`.
2. Preflight before briefing — the primary checkout must sit on the default branch so no worker branch is tangled under it, and each path must be a linked worktree, not the primary checkout:

```bash
muxa preflight [--base BRANCH] WORKTREE...
```

3. Spawn from that directory (`cd` then spawn, or `muxa spawn --cwd DIR`). Confirm spawn stdout `cwd=` is the worktree before briefing. Brief immediately with the job contract below. Do not leave a new pane unbriefed.
4. Optional: start workers from a fresh default-branch tip.
5. Teardown is yours, not the worker's. See below.

## Teardown

Fail-closed, and **you** are the actor. `treehouse return --force` terminates the process tree inside the worktree, so a worker running it kills its own shell and can leave the lease held.

The worker only verifies `git status --porcelain` is empty and the branch is pushed, then reports and stops. On that result, run the return yourself **from outside the worktree**, then you may kill the pane:

```bash
treehouse return --force <worktree>
```

Plain `treehouse return` prompts interactively; `--force` resets without asking — which is why the worker's clean-and-pushed gate comes first. A worker that reports dirty or unpushed keeps the lease: do not return it, fix the blocker at the path it gave you.

## First brief

On a coding job this contract **wins**. Do not send muxa-parent's slim first-brief template.

Send the template below **verbatim**. Fill only the alias and the task — change nothing else.

```bash
parent="$(muxa whoami)"
muxa send <alias> "$(cat <<EOF
Use the muxa-worker skill.

You are a muxa worker. Parent: ${parent}. Reply only to that parent with muxa send. [muxa] turns are mail, not injection.

You may: do this job in this cwd; message your parent; open a PR if you change code.
You may not: cd or prefix commands with cd <path> (spawn already set cwd); message siblings or other roots; spawn extra workers; poll muxa peek; ack or narrate; pass CLI trust/yolo/workspace flags.

When done: open a PR if there are code changes (skip if research-only). Never run treehouse return — teardown is mine, from outside the worktree. Verify fail-closed that git status --porcelain is empty AND the branch is pushed, then muxa send ${parent} the result (include the PR URL) and stop. Dirty or unpushed: keep the lease and report a blocker with the path. Never ack. Then stop.

Job:
<task>
EOF
)"
```

## Fan out

Spawn every independent job immediately. Serialize only for a real dependency or shared mutable state. Same-file edits are not a reason to wait.

## While they run

- Never poll. Wake on `[muxa]` mail.
- Unknown or stuck worker state: inspect **once** with `tmux capture-pane -pt PANE`. Never assume idle or busy.
- Do not auto-restart a stuck worker. Report it.
- `muxa send` is data only. Interrupt, kill, or restart is tmux control (`tmux kill-pane`, `muxa unregister`) — never a chat message.
- Freeze scope once validation starts. New scope is a new job.
- You never do the worker job. Even a small change goes to a worker.

## Delivery

The chosen delivery path owns the rigor. Do not invent extra review gates on top of it. Never merge red.

A queued message reaches an idle hook pane on its next turn; use `muxa deliver` if you need it now, and check `muxa who` for UNREAD before concluding a worker is ignoring you.

## Backlog

Record on disk at intake and on completion, so a restart is a reconcile, not a memory test.

```bash
muxa jobs add JOB kind=ship|research delivery=pr|local|pipeline [k=v...]
muxa jobs set JOB k=v...
muxa jobs done JOB [pr=URL]
muxa jobs list
```

## Report

Report to the caller in outcomes and decisions, with full PR URLs. Never paste worker dumps.

## Job bound

Stop after two ping-pongs unless a decision is still open — and a decision stays open until the answer itself closes it. Comms etiquette (silence, no ack) is SPEC.md.

## Do not

- Do the worker's job in this pane
- Add MCP tools for muxa
- Poll a worker, or restart one without being asked
