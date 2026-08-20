# muxa

Messaging between AI agent CLIs that already share a **tmux** server.
No MCP server. No extra tools in the model context. A small user-level
Go broker owns pane paste (`muxa broker` / `bin/muxa-broker`).

```
muxa who
muxa send reviewer "auth.ts is ready for review"
```

The other agent sees a normal user turn tagged `[muxa]`. That is the protocol.
`muxa send` enqueues on the broker and does **not** also run Stop-hook drain
or `kick_wait` for that message.

**Scope:** muxa is tmux agent spawn, mail, preflight, and a runtime jobs map
(`muxa jobs`). It is not a job orchestrator. For worktree leasing, dispatch
contracts, and multi-worker coding jobs, use
[command-post](https://github.com/0xb1ob/command-post).

## Research: what already exists, and why not

The constraint was: **light, zero wasted tokens, use each CLI's built-in
capabilities, every agent lives in tmux.**

| Approach | Examples | Tokens | tmux-native | Verdict |
| --- | --- | --- | --- | --- |
| MCP mailbox + polling | team-mcp, tmux-bridge-mcp, llm-provider-mcp | Tool schemas on **every** turn; models poll `pull_messages` | Often yes | Heavy. Tokens burn even when nobody is talking. |
| File inbox the model must check | SAMP / agent-message | ~1 Bash call to send; receive still needs `/inbox` or a prompt | No | Excellent as a log; wrong receive path. An idle agent never notices mail. |
| Blind `tmux send-keys` | tmux-agent-comms, amux, ad-hoc orchestrators | Receive is a user turn (good) | Yes | Right wire, wrong timing. Inject while the TUI is in a tool call or a permission prompt and you corrupt the turn. |
| Google A2A / IBM ACP | HTTP + Agent Cards | HTTP stack, cards, task state machines | No | Right for networked enterprise agents. Absurd for two panes on one laptop. |
| Vendor teams | Claude Code Agent Teams | In-family only | No | Use it when everyone is Claude. It will not talk to Cursor or Pi. |
| **muxa** | this repo | Send = one Bash call. Receive = one user turn. | **Required** | Roster in tmux options, broker queue + paste, leftover hook drain still in-tree. |

### The token budget that actually matters

MCP is the expensive mistake. A `send_message` tool's JSON schema is loaded
on **every** request for the rest of the session, including turns that never
message anyone.

muxa uses surfaces the CLIs already pay for:

- **Bash / Shell** — already in the tool list. `muxa send` is one call.
- **Skills / slash commands** — loaded on demand, not as always-on tools.
- **Hooks** — Claude `Stop` `additionalContext`, Cursor `stop` `followup_message` (`loop_limit: null`), Oh My Pi `session_stop` `{continue, additionalContext}`. Zero model tokens until there is mail.
- **The prompt itself** — an idle agent is already blocked on stdin. `paste-buffer` + Enter is typing, not a new protocol.

### Delivery (the part everyone gets wrong)

`muxa send` talks to **muxa-broker** (unix socket, file-backed queue). The
broker pastes with load-buffer + Enter when the target pane looks free
(tmux `capture-pane` only: prompt-ish last line, empty input). If the pane
is mid-typing, the broker retries until `MUXA_BROKER_DEADLINE` (default 10
minutes), then pastes once. If the broker is down, `muxa send` pastes once
immediately. That paste path is last-resort fallback — not the primary
loop. Send must not also fire hook/`kick_wait` inject for the same
message.

Heuristic (documented in [SPEC.md](SPEC.md)): strip ANSI; last non-empty
line; empty → free; prompt marker (`$%#>❯`) with text after it → typing
(not free); prompt marker at end of line → free; anything else → wait.

Hook / `kick_wait` / composer JSON stay in the tree (`MUXA_BROKER=0`
restores that send path for leftover tests). Broker mode does not need
them.

tmux user options are the roster (`@muxa_name`, `@muxa_kind`, `@muxa_state`,
`@muxa_deliver`, `@muxa_session`, `@muxa_hook_ok`, `@muxa_unread`). `tmux list-panes` is service discovery. The broker's file queue is the send
path. Maildir remains for hook-drain leftovers and `muxa peek`. Pane
titles are CLI-owned; do not put unread there.

Spec: [SPEC.md](SPEC.md).

### Seeing unread mail

Stock tmux has `pane-border-status off`. To show the `@muxa_unread` hint
on the border (tmux renders this; OSC-2 cannot erase it):

```tmux
set -g pane-border-status top
set -g pane-border-format \
  '#{?@muxa_unread,#[reverse] #{@muxa_unread} unread #[default] ,}#{?@muxa_name,#{@muxa_name},#{pane_current_command}}'
```

`muxa who` has an `UNREAD` column counted from the maildir. There is no
bell: ringing another pane's tty would be typing at it. Idle inject is
paste-buffer + Enter; queueing is for a busy TUI, a non-empty composer
after hook_ok, and for failed injects.

## Install

Copy-paste on a fresh machine (needs `git` + `curl`):

```bash
curl -fsSL https://raw.githubusercontent.com/0xb1ob/muxa/main/install.sh | bash
```

That clones this repo into `~/.muxa`, symlinks `muxa` onto `~/.local/bin`,
merges user-level hooks, and copies the **muxa-parent** and **muxa-worker**
skills to `~/.cursor/skills`, `~/.claude/skills`, and `~/.agents/skills`.
Re-run the same curl to refresh `~/.muxa` from `origin/main` and update skills/hooks.

From a git checkout (development):

```bash
./install.sh          # install from this tree; does not clone
tests/run.sh          # maildir / inject unit tests
tests/install.sh      # ~/.muxa cache update after a squash / shallow fetch
tests/e2e.sh          # real Claude Code + Cursor Agent + Oh My Pi in tmux
```

This repo also ships project-scoped hooks for working on muxa itself:

- `.claude/settings.json` → `scripts/muxa-hook.sh`
- `.cursor/hooks.json` → `scripts/muxa-hook.sh`

Put `bin/` on `PATH` inside the tmux panes (or use `examples/agents.sh`).

```bash
tmux new -s agents
claude
# from that pane (or muxa spawn as that pane):
muxa spawn --name cursor -- agent   # split into this window; tiled grid
muxa spawn --name pi -- omp
muxa spawn --window --name solo -- agent   # dedicated window (old default)
```

Hooks register the pane on session start. Confirm:

```bash
muxa who
```

## Agent skills

| Skill | Who loads it |
| --- | --- |
| [muxa-parent](skills/muxa-parent/SKILL.md) | Root pane: spawn + mail |
| [muxa-worker](skills/muxa-worker/SKILL.md) | Spawned pane: do the job, reply to parent |

The parent's first `muxa send` names `muxa-worker` and inlines the generic
rules so a worker in a repo with no project skills still behaves. For
orchestration (leases, worktrees, PR contracts), use
[command-post](https://github.com/0xb1ob/command-post) — muxa stays
spawn+mail only.
Silence is default. Reply only with a question, a result, or a blocker.
Never ack. `--no-reply` for status dumps. Etiquette: [SPEC.md](SPEC.md).

## Commands

| Command | What |
| --- | --- |
| `muxa register [--name --id --parent --kind --deliver]` | Set pane identity (hooks do this) |
| `muxa spawn [--name NAME] [--cwd DIR] [--split] [--window] -- CMD` | Split a child pane into a tiled grid in the parent's window. Child cwd is `--cwd`, else process `$PWD`, else the parent pane path. Warns on stderr if a live worker already has that cwd (does not refuse). Omit `--name` for a unique `adjective-noun` alias. `--window` for a dedicated window; `--split` is compat |
| `muxa who` | Roster (name, id, session, parent, cwd, STATUS, UNREAD, …) |
| `muxa unregister NAME\|ID` | Clear muxa registration; leave pane running |
| `muxa session` | This pane's CLI session/conversation id |
| `muxa children` | Direct children of this pane |
| `muxa send NAME TEXT` | Enqueue on the broker (parent↔child). Auto-starts the daemon if the socket is dead |
| `muxa send --all TEXT` | Every parent/child pane (not siblings or other roots) |
| `muxa broker [start\|status\|stop]` | User-level paste broker (unix socket + file queue) |
| `muxa peek [NAME]` | Unread (humans/scripts, not the model loop) |
| `muxa deliver [--force] [NAME]` | Claim + inject now (escape hatch). Prechecks by default; `--force` skips them |
| `muxa hook stop --format claude\|cursor\|pi` | Native continue payload |
| `muxa preflight [--base BRANCH] [WORKTREE...]` | Repo checks before handing out jobs (git only, no tmux) |
| `muxa jobs add\|set\|done\|list` | Runtime map (worker/worktree/branch) for **existing** br issues. Does not create br issues or act as the backlog. Address jobs by br id (titles may contain whitespace). Durable kind/delivery/status stay on br. `br` is required; muxa auto-inits `.beads/` on first use |

## Tests

```bash
tests/run.sh          # maildir / inject unit tests (MUXA_BROKER=0)
tests/broker.sh       # broker integration (isolated tmux + dummy prompts)
tests/tmux-facts.sh   # version-sensitive tmux behaviour muxa depends on
tests/composer.sh     # composer-verdict fixtures (no tmux)
```

Needs `tmux`, `python3`, and (for the broker) Go 1.21+. Uses a private tmux socket, not your session.
`muxa jobs` tests also need [`br`](https://github.com/Dicklesworthstone/beads_rust) on `PATH`.

## Environment

| Variable | Default | What |
| --- | --- | --- |
| `MUXA_ENTER_DELAY` | `0.15` | Seconds between paste and Enter |
| `MUXA_INJECT_MAX` | `8192` | Max inject payload size in **bytes** |
| `MUXA_CLAIM_TTL` | `120` | Seconds before claimed (`cur/`) mail is presumed lost and redelivered |
| `MUXA_SWEEP_MIN_AGE` | `1` | Seconds a `cur/` file must age before Stop moves it to `done/` |
| `MUXA_REDELIVER_MAX` | `3` | Redeliveries before parking in `dead/` |
| `MUXA_BATCH_MAX` | `8` | Max messages claimed per inject attempt |
| `MUXA_KICK_WAIT_MAX` | `120` | `kick_wait` polls (0.5s each) between progress log lines |
| `MUXA_KICK_WAIT_DEADLINE` | `3600` | Seconds before a `kick_wait` waiter gives up (logged + `WAIT=expired`) |
| `MUXA_FORCE_INJECT` | `0` | `1` skips readiness prechecks (tests / `muxa deliver --force`) |
| `MUXA_BROKER` | `1` | `0` uses the leftover hook/`kick_wait` send path |
| `MUXA_BROKER_DIR` | `<runtime>/broker` | File-backed queue + pidfile + log |
| `MUXA_BROKER_SOCK` | `$MUXA_BROKER_DIR/broker.sock` | Unix socket |
| `MUXA_BROKER_PID` | `$MUXA_BROKER_DIR/broker.pid` | Pidfile |
| `MUXA_BROKER_BIN` | `bin/muxa-broker` next to `muxa` | Daemon binary |
| `MUXA_BROKER_DEADLINE` | `600` | Seconds to wait for a free pane before fallback paste |
| `MUXA_BROKER_POLL_MS` | `250` | Broker retry interval |
| `MUXA_COMPOSER_CHECK` | `1` | `0` disables styled-content composer parsing for CLI kinds |
| `MUXA_COMPOSER_SETTLE` | `0.25` | Seconds between the two composer-empty reads |
| `MUXA_TMUX_SOCKET` | unset | Private tmux socket name (`tmux -L`) |
| `MUXA_TMUX_BIN` | `tmux` | tmux binary |

## Trust

Injecting into a pane **is** typing at that agent. Same tmux socket = same
trust boundary. Do not attach untrusted users to that server.
