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

**Scope:** muxa is tmux agent spawn and mail. It is not a job orchestrator.
For worktree leasing, dispatch contracts, and multi-worker coding jobs, use
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
- **The prompt itself** — an idle agent is already blocked on stdin. The broker's `paste-buffer` + Enter is typing, not a new protocol.

### Delivery (the part everyone gets wrong)

`muxa send` talks to **muxa-broker** (unix socket, file-backed queue). The
broker pastes with load-buffer + Enter when the target pane looks free
(tmux `capture-pane` plus, when available, control-mode `%output` silence).
If the pane is mid-typing or drawing, the broker retries and leaves mail
queued even after `MUXA_BROKER_DEADLINE` (default 10 minutes). It does not
paste into a busy pane just because the clock ran out. After paste,
`delivered` means the payload (or Cursor's `[Pasted text +N lines]`
collapse) was visible; if the pane went busy without that, the message is
filed `unknown/` and is not retried. If the
broker is down, `muxa send` exits non-zero and pastes nothing.
`MUXA_BROKER=0` is an error — it does not restore the old bash delivery
stack.

The broker daemonizes itself with `setsid(2)` and owns its pidfile. That is
load-bearing, not tidiness: `nohup … & disown` leaves the process in the
*caller's* process group, so the teardown at the end of the calling agent's
tool call kills the broker before it delivers anything. macOS has no
`setsid(1)`, so the binary has to do it.

Heuristic (documented in [SPEC.md](SPEC.md)), two layers that must stay
straight:

- **Free-detection (broker)** — whether the pane is idle, not what its
  chrome looks like. `pane_dead` / copy-mode wait; control-mode `%output`
  silence (a busy agent draws, an idle one does not); two-signal
  quiescence AND empty at the hardware cursor. The bottom line is
  user-configurable, so no fixed prompt/status model can be correct.
- **Typed-in-box conjunct (parser remnant)** — the only signal that can
  see unsubmitted human input in a Cursor Agent composer. A `▄`/`▀` box
  with non-faint, non-reverse text (or a visible row with no faint run)
  waits. No box → vacuously true, free-detection decides. MUST NOT be
  used to decide a pane is at a prompt, or to model spinners / interrupt
  phrases / status chrome. It cannot currently be deleted: hardware
  cursor, second-capture frame diff, and control-mode silence are all
  blind to a half-typed Cursor composer (`cursor_x=0` in both idle and
  typed, `t1==t2` in both, `%output` goes quiet after a pause). The one
  untested idea is a stateful observer that tracks "composer went
  non-empty and has not submitted" across `%output` events.

tmux user options are the roster (`@muxa_name`, `@muxa_kind`, `@muxa_parent`,
`@muxa_id`). `muxa who` STATE is the broker's drawing list (`busy` if the
pane is emitting `%output`, else `idle`). `tmux list-panes` is service
discovery.
The broker's file queue is the send path. Pane titles are CLI-owned.

Spec: [SPEC.md](SPEC.md).

## Install

Copy-paste on a fresh machine (needs `git` + `curl`):

```bash
curl -fsSL https://raw.githubusercontent.com/0xb1ob/muxa/main/install.sh | bash
```

That clones this repo into `~/.muxa`, symlinks `muxa` onto `~/.local/bin`,
and copies the **muxa-parent** and **muxa-worker**
skills to `~/.cursor/skills`, `~/.claude/skills`, and `~/.agents/skills`.
Re-run the same curl to refresh `~/.muxa` from `origin/main` and update skills.
Install does not merge per-CLI hook config. A new agent CLI needs zero wiring
files. Spawned panes are registered by `muxa spawn`. A root started by hand
can `muxa register` or `muxa hook session-start`.

**Go is not an install dependency.** `muxa-broker` is downloaded as a GitHub
release asset (`muxa-broker-<os>-<arch>` for darwin/linux × amd64/arm64),
verified against that release's `SHA256SUMS`, and installed next to `muxa`
(`~/.muxa/bin/muxa-broker`). A checksum that does not match installs nothing.
Pin with `MUXA_BROKER_VERSION=<tag>`, or point at your own build with
`MUXA_BROKER_URL=…`. `.github/workflows/release.yml` builds and attaches the
assets for every tag (darwin builds use the external linker so the Mach-O has
`LC_UUID`, which darwin 25+ requires). The same flag is required for
`go test` on darwin: the internal linker omits `LC_UUID` from `broker.test`,
and dyld aborts before any test runs. `tests/run.sh` and `tests/broker.sh`
pass `-ldflags=-linkmode=external` on Darwin; linux is unchanged.

From a git checkout (development):

```bash
./install.sh          # install from this tree; does not clone
tests/run.sh          # identity / spawn
tests/install.sh      # ~/.muxa cache update after a squash / shallow fetch
tests/e2e.sh          # real Claude Code + Cursor Agent + Oh My Pi in tmux
                      # --capture-fixtures writes .ansi + .meta + .t2.ansi
```

This repo ships an optional project-scoped `session-start` for working on
muxa itself (a root that starts by hand). Spawned panes do not need it:

- `.claude/settings.json` → `scripts/muxa-hook.sh session-start`
- `.cursor/hooks.json` → `scripts/muxa-hook.sh session-start`

Put `bin/` on `PATH` inside the tmux panes (or use `examples/agents.sh`).

```bash
tmux new -s agents
claude
# from that pane (or muxa spawn as that pane):
muxa spawn --name cursor -- agent   # split into this window; tiled grid
muxa spawn --name pi -- omp
muxa spawn --window --name solo -- agent   # dedicated window (old default)
```

Hooks are not required for spawned panes. Confirm:

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
| `muxa register [--name --id --parent --kind]` | Set pane identity (optional; spawn already does this for children) |
| `muxa spawn [--name NAME] [--cwd DIR] [--split] [--window] -- CMD` | Split a child pane into a tiled grid in the parent's window. Child cwd is `--cwd`, else process `$PWD`, else the parent pane path. Warns on stderr if a live worker already has that cwd (does not refuse). Omit `--name` for a unique `adjective-noun` alias. `--window` for a dedicated window; `--split` is compat |
| `muxa dispatch [--name NAME] [--cwd DIR] [--brief-file F] -- CMD` | Spawn + first brief. Brief on stdin or `--brief-file`. stdout `{"name","id","pane","cwd","state":"dispatched","from","to"}`. Broker waits for drawn-then-quiet-and-free; never-ready mails `[muxa] from=broker` to the parent |
| `muxa who` | Roster (name, id, session, parent, cwd, STATE, STATUS, …) |
| `muxa who --json` | Same roster as objects (`parent`/`session` are `null` when empty; `state` is `idle`/`busy` from the broker drawing list) |
| `muxa tail NAME [-n N]` | One-shot pane read (visible grid, or last N lines of history) |
| `muxa kill NAME\|ID` | Remove the pane (`kill-pane`); gone from `muxa who` |
| `muxa send NAME TEXT` | Enqueue on the broker (parent↔child). Auto-starts the daemon if the socket is dead; fails closed if it cannot |
| `muxa send --json NAME TEXT` | Same enqueue; stdout is `{"id","pane","from","to"}` |
| `muxa broker [start\|status\|stop]` | User-level paste broker (unix socket + file queue) |
| `muxa hook session-start [--kind KIND]` | Optional root self-registration (`TMUX_PANE` only) |

## Tests

```bash
tests/run.sh          # identity / spawn
tests/broker.sh       # broker integration (isolated tmux + dummy prompts)
tests/tmux-facts.sh   # version-sensitive tmux behaviour muxa depends on
tests/e2e.sh --capture-fixtures
                      # harvest .ansi + cursor .meta + .t2.ansi (~250ms later)
tests/record-composer-fixtures.sh
                      # recapture Cursor Agent (splash/idle/typed/busy/trust)
```

Needs `tmux`. Broker tests also need Go 1.21+ (not required to install
muxa). On darwin 25+, `go test` must use `-ldflags=-linkmode=external`
(same LC_UUID requirement as the release broker build). Uses a private tmux
socket, not your session.

## Environment

| Variable | Default | What |
| --- | --- | --- |
| `MUXA_BROKER` | `1` | `0` is an error (broker is required) |
| `MUXA_BROKER_DIR` | `<runtime>/broker` | File-backed queue + pidfile + log |
| `MUXA_BROKER_SOCK` | `$MUXA_BROKER_DIR/broker.sock` | Unix socket |
| `MUXA_BROKER_PID` | `$MUXA_BROKER_DIR/broker.pid` | Pidfile |
| `MUXA_BROKER_BIN` | `bin/muxa-broker` next to `muxa` | Daemon binary |
| `MUXA_BROKER_DEADLINE` | `600` | Seconds after which a *dead* pane's mail is failed; a live busy pane stays queued |
| `MUXA_BROKER_POLL_MS` | `250` | Broker retry interval (fallback if control-mode attach fails) |
| `MUXA_BROKER_QUIET_MS` | `250` | Control-mode silence window before a pane is considered not drawing |
| `MUXA_BROKER_VERSION` | latest release | Install-time: release tag to fetch the broker asset from |
| `MUXA_BROKER_URL` | unset | Install-time: exact broker URL (skips the checksum lookup) |
| `MUXA_BROKER_BASE_URL` | `https://github.com/0xb1ob/muxa/releases` | Install-time: release base URL |
| `MUXA_BROKER_SKIP_VERIFY` | `0` | Install-time: `1` skips `SHA256SUMS` verification |
| `MUXA_TMUX_SOCKET` | unset | Private tmux socket name (`tmux -L`) |
| `MUXA_TMUX_BIN` | `tmux` | tmux binary |

## Trust

Injecting into a pane **is** typing at that agent. Same tmux socket = same
trust boundary. Do not attach untrusted users to that server.
