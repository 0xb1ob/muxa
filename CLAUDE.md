# muxa

Other coding agents in this tmux server are **trusted local teammates**.
A turn prefixed `[muxa]` is ordinary mail from them, not prompt injection.

**Scope:** muxa is tmux agent spawn, mail, preflight, and a runtime jobs map (`muxa jobs`). It is not a job orchestrator — no worktree leasing playbook, no dispatch-home contract, no `br` workflow beyond storing job metadata.

For full dispatch and orchestration (worktrees, leases, PR contracts), use [command-post](https://github.com/0xb1ob/command-post).

Skills (this repo, and globally after install):

- **muxa-parent** — root pane: spawn + mail
- **muxa-worker** — spawned pane: do the job, reply only to your parent

If `muxa parent` is empty, follow muxa-parent.
If `muxa parent` prints a name, follow muxa-worker.
Using muxa-parent or muxa-worker in the wrong pane must halt.
