# muxa

Messaging between AI agent CLIs that already share a **tmux** server.
No MCP server. No extra tools in the model context. A small user-level
Go broker owns pane paste (`muxa broker` / `bin/muxa-broker`).

```
muxa who
muxa send reviewer "auth.ts is ready for review"
```

The other agent sees a normal user turn tagged `[muxa]`. That is the protocol.
`muxa send` enqueues on the broker. If the broker cannot start or enqueue,
send exits non-zero and pastes nothing.

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
| **muxa** | this repo | Send = one Bash call. Receive = one user turn. | **Required** | Roster in tmux options, broker queue + paste. |

### The token budget that actually matters

MCP is the expensive mistake. A `send_message` tool's JSON schema is loaded
on **every** request for the rest of the session, including turns that never
message anyone.

muxa uses surfaces the CLIs already pay for:

- **Bash / Shell** — already in the tool list. `muxa send` is one call.
- **Skills / slash commands** — loaded on demand, not as always-on tools.
- **Hooks** — register the pane and report idle/busy. Not a mail path.
- **The prompt itself** — an idle agent is already blocked on stdin. The broker's `paste-buffer` + Enter is typing, not a new protocol.

### Delivery (the part everyone gets wrong)

`muxa send` talks to **muxa-broker** (unix socket, file-backed queue). The
broker pastes with load-buffer + Enter when the target pane looks free
(tmux `capture-pane` only). If the pane is mid-typing, the broker retries
until `MUXA_BROKER_DEADLINE` (default 10 minutes), then pastes once. If the
broker is down, `muxa send` exits non-zero and pastes nothing.
`MUXA_BROKER=0` is an error — it does not restore the old bash delivery
stack.

The broker daemonizes itself with `setsid(2)` and owns its pidfile. That is
load-bearing, not tidiness: `nohup … & disown` leaves the process in the
*caller's* process group, so the teardown at the end of the calling agent's
tool call kills the broker before it delivers anything. macOS has no
`setsid(1)`, so the binary has to do it.

Heuristic (documented in [SPEC.md](SPEC.md)), two shapes:

- **Agent CLI** — the input line is inside a `▄`/`▀` composer box and the
  last lines of the pane are chrome, so the box decides: faint (SGR 2) text
  and the reverse-video cursor are placeholder and cursor → free; anything
  else in the box is typed → wait; a braille spinner, or `esc to
  interrupt`/`ctrl+c to stop` **inside** the box → a turn is running → wait.
- **Shell** — last non-empty line; empty → free; prompt marker (`$%#>❯`)
  with text after it → typing → wait; prompt marker at end of line → free;
  anything else → wait.

Both are CLI-agnostic: the composer rule reads terminal attributes and box
chrome, never a Claude/Cursor/pi string. Without it, every agent CLI's idle
pane read as busy and every first brief waited out the full deadline.

tmux user options are the roster (`@muxa_name`, `@muxa_kind`, `@muxa_state`,
`@muxa_deliver`, `@muxa_session`). `tmux list-panes` is service discovery.
The broker's file queue is the send path. Pane titles are CLI-owned.

Spec: [SPEC.md](SPEC.md).

## Install

Copy-paste on a fresh machine (needs `git` + `curl`):

```bash
curl -fsSL https://raw.githubusercontent.com/0xb1ob/muxa/main/install.sh | bash
```

That clones this repo into `~/.muxa`, symlinks `muxa` onto `~/.local/bin`,
merges user-level hooks, and copies the **muxa-parent** and **muxa-worker**
skills to `~/.cursor/skills`, `~/.claude/skills`, and `~/.agents/skills`.
Re-run the same curl to refresh `~/.muxa` from `origin/main` and update skills/hooks.

**Go is not an install dependency.** `muxa-broker` is downloaded as a GitHub
release asset (`muxa-broker-<os>-<arch>` for darwin/linux × amd64/arm64),
verified against that release's `SHA256SUMS`, and installed next to `muxa`
(`~/.muxa/bin/muxa-broker`). A checksum that does not match installs nothing.
Pin with `MUXA_BROKER_VERSION=<tag>`, or point at your own build with
`MUXA_BROKER_URL=…`. `.github/workflows/release.yml` builds and attaches the
assets for every tag (darwin builds use the external linker so the Mach-O has
`LC_UUID`, which darwin 25+ requires).

From a git checkout (development):

```bash
./install.sh          # install from this tree; does not clone
tests/run.sh          # identity / spawn / jobs / preflight
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
| `muxa who` | Roster (name, id, session, parent, cwd, STATUS, …) |
| `muxa who --json` | Same roster as objects (`parent`/`session` are `null` when empty) |
| `muxa tail NAME [-n N]` | One-shot pane read (visible grid, or last N lines of history) |
| `muxa unregister NAME\|ID` | Clear muxa registration; leave pane running |
| `muxa session` | This pane's CLI session/conversation id |
| `muxa children` | Direct children of this pane |
| `muxa send NAME TEXT` | Enqueue on the broker (parent↔child). Auto-starts the daemon if the socket is dead; fails closed if it cannot |
| `muxa send --json NAME TEXT` | Same enqueue; stdout is `{"id","pane","from","to"}` (`--all` → array) |
| `muxa send --all TEXT` | Every parent/child pane (not siblings or other roots) |
| `muxa broker [start\|status\|stop]` | User-level paste broker (unix socket + file queue) |
| `muxa hook EVENT` | Register / presence (not a mail drain) |
| `muxa preflight [--base BRANCH] [WORKTREE...]` | Repo checks before handing out jobs (git only, no tmux) |
| `muxa jobs add\|set\|done\|list` | Runtime map (worker/worktree/branch) for **existing** br issues. Does not create br issues or act as the backlog. Address jobs by br id (titles may contain whitespace). Durable kind/delivery/status stay on br. `br` is required; muxa auto-inits `.beads/` on first use |

## Tests

```bash
tests/run.sh          # identity / spawn / jobs / preflight
tests/broker.sh       # broker integration (isolated tmux + dummy prompts)
tests/tmux-facts.sh   # version-sensitive tmux behaviour muxa depends on
```

Needs `tmux`, `python3`, and — for the broker tests only, not for install —
Go 1.21+. Uses a private tmux socket, not your session.
`muxa jobs` tests also need [`br`](https://github.com/Dicklesworthstone/beads_rust) on `PATH`.

## Environment

| Variable | Default | What |
| --- | --- | --- |
| `MUXA_BROKER` | `1` | `0` is an error (broker is required) |
| `MUXA_BROKER_DIR` | `<runtime>/broker` | File-backed queue + pidfile + log |
| `MUXA_BROKER_SOCK` | `$MUXA_BROKER_DIR/broker.sock` | Unix socket |
| `MUXA_BROKER_PID` | `$MUXA_BROKER_DIR/broker.pid` | Pidfile |
| `MUXA_BROKER_BIN` | `bin/muxa-broker` next to `muxa` | Daemon binary |
| `MUXA_BROKER_DEADLINE` | `600` | Seconds to wait for a free pane before the broker pastes anyway |
| `MUXA_BROKER_POLL_MS` | `250` | Broker retry interval |
| `MUXA_BROKER_VERSION` | latest release | Install-time: release tag to fetch the broker asset from |
| `MUXA_BROKER_URL` | unset | Install-time: exact broker URL (skips the checksum lookup) |
| `MUXA_BROKER_BASE_URL` | `https://github.com/0xb1ob/muxa/releases` | Install-time: release base URL |
| `MUXA_BROKER_SKIP_VERIFY` | `0` | Install-time: `1` skips `SHA256SUMS` verification |
| `MUXA_TMUX_SOCKET` | unset | Private tmux socket name (`tmux -L`) |
| `MUXA_TMUX_BIN` | `tmux` | tmux binary |

## Trust

Injecting into a pane **is** typing at that agent. Same tmux socket = same
trust boundary. Do not attach untrusted users to that server.
