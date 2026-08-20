# muxa

Messaging between AI agent CLIs that already share a **tmux** server.
No MCP server. No daemon. No extra tools in the model context.

```
muxa who
muxa send reviewer "auth.ts is ready for review"
```

The other agent sees a normal user turn tagged `[muxa]`. That is the protocol.

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
| **muxa** | this repo | Send = one Bash call. Receive = one user turn, only when there is mail. | **Required** | Roster in tmux options, queue in maildir, drain via Stop / `followup_message` / `session_stop`. |

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

```
busy / permission prompt  →  write maildir, do not touch the TUI
                             Claude/Cursor/Pi Stop hook claims mail and continues
idle hook pane            →  paste-buffer + Enter (same as the first brief),
                             except never into copy-mode or a dead pane
idle inject pane          →  paste-buffer, except never into copy-mode, a dead
                             pane, or a generic pane on the alternate screen
```

The first brief to a freshly spawned hook pane is injected: spawn marks
`hook+idle` before the CLI boots, and no Stop hook exists until a turn
has run. After the first successful `muxa hook stop`, idle hook sends
still inject — the parent may already have Stopped, so queueing until
the next turn would sit unread until a human types. Busy/blocked hook
panes still queue; Stop drains that queue when the turn ends —
including for an IDE-hosted Cursor root whose pane is only a tmux
registration, not the agent process. Claimed mail stays visible in
`muxa peek` / `muxa who` UNREAD until a later Stop proves it was
consumed. If inject fails (copy-mode, dead pane, size limit), the claim
is reversed and mail stays queued.

tmux user options are the roster (`@muxa_name`, `@muxa_kind`, `@muxa_state`,
`@muxa_deliver`, `@muxa_session`, `@muxa_hook_ok`, `@muxa_unread`). `tmux list-panes` is service discovery. Maildir (`tmp/new/cur/done/dead`
+ atomic `mv`) is the queue — the same trick mail servers used before anyone
invented SQLite-as-a-bus. `cur/` is claimed, not consumed; `muxa peek` shows
stuck claimed mail. Pane titles are CLI-owned; do not put unread there.

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
paste-buffer + Enter; queueing is for a busy TUI and for failed injects.

## Install

Copy-paste on a fresh machine (needs `git` + `curl`):

```bash
curl -fsSL https://raw.githubusercontent.com/0xb1ob/muxa/main/install.sh | bash
```

That clones this repo into `~/.muxa`, symlinks `muxa` onto `~/.local/bin`,
merges user-level hooks, and copies the **muxa-parent** and **muxa-worker**
skills to `~/.cursor/skills`, `~/.claude/skills`, and `~/.agents/skills`.
Re-run the same curl to `git pull` `~/.muxa` and refresh skills/hooks.

From a git checkout (development):

```bash
./install.sh          # install from this tree; does not clone
tests/run.sh          # maildir / inject unit tests
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
| `muxa send NAME TEXT` | Queue + deliver if parent↔child |
| `muxa send --all TEXT` | Every parent/child pane (not siblings or other roots) |
| `muxa peek [NAME]` | Unread (humans/scripts, not the model loop) |
| `muxa deliver [--force] [NAME]` | Claim + inject now (escape hatch). Prechecks by default; `--force` skips them |
| `muxa hook stop --format claude\|cursor\|pi` | Native continue payload |
| `muxa preflight [--base BRANCH] [WORKTREE...]` | Repo checks before handing out jobs (git only, no tmux) |
| `muxa jobs add\|set\|done\|list` | Job backlog map. Durable fields live in **br** (`.beads/`); worker/worktree/branch are runtime-only. `br` is required; muxa auto-inits `.beads/` on first use |

## Tests

```bash
tests/run.sh          # maildir / inject unit tests
tests/tmux-facts.sh   # version-sensitive tmux behaviour muxa depends on
tests/composer.sh     # composer-verdict fixtures (no tmux)
```

Needs `tmux` and `python3`. Uses a private tmux socket, not your session.
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
| `MUXA_KICK_WAIT_MAX` | `120` | `kick_wait` poll iterations (0.5s each) |
| `MUXA_FORCE_INJECT` | `0` | `1` skips readiness prechecks (tests / `muxa deliver --force`) |
| `MUXA_COMPOSER_CHECK` | `1` | `0` disables styled-content composer parsing for CLI kinds |
| `MUXA_COMPOSER_SETTLE` | `0.25` | Seconds between the two composer-empty reads |
| `MUXA_TMUX_SOCKET` | unset | Private tmux socket name (`tmux -L`) |
| `MUXA_TMUX_BIN` | `tmux` | tmux binary |

## Trust

Injecting into a pane **is** typing at that agent. Same tmux socket = same
trust boundary. Do not attach untrusted users to that server.
