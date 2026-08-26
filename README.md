# muxa

Messaging between AI agent CLIs that already share a **tmux** server.
No MCP server. No extra tools in the model context. One Go binary is the
CLI and the paste broker (`muxa broker`).

```
muxa who
muxa send reviewer "auth.ts is ready for review"
```

The other agent sees a normal user turn tagged `[muxa]`. That is the protocol.

**Scope:** muxa is tmux agent spawn and mail, not a job orchestrator.
Worktrees, leases, and multi-worker jobs: [command-post](https://github.com/0xb1ob/command-post).
Normative rules: [SPEC.md](SPEC.md).

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

MCP is the expensive mistake: a `send_message` schema is loaded on every
request. muxa uses surfaces the CLIs already pay for — Bash, on-demand
skills, and the prompt the idle agent is already blocked on.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/0xb1ob/muxa/main/install.sh | bash
```

Needs `git` + `curl`. Clones into `~/.muxa`, puts `muxa` on `~/.local/bin`,
and copies [muxa-parent](skills/muxa-parent/SKILL.md) /
[muxa-worker](skills/muxa-worker/SKILL.md) to `~/.cursor/skills`,
`~/.claude/skills`, and `~/.agents/skills`. Re-run to refresh from
`origin/main`. Install does not merge per-CLI hook config. Spawned panes
are registered by `muxa spawn`; a root started by hand can `muxa register`
or `muxa hook session-start`.

Go is not an install dependency. `install.sh` downloads `muxa-<os>-<arch>`
(darwin/linux × amd64/arm64), verifies `SHA256SUMS`, and installs that one
binary. Pin with `MUXA_BROKER_VERSION=<tag>`, or `MUXA_BROKER_URL=…`.
[release.yml](.github/workflows/release.yml) attaches assets per tag.
Darwin builds (and `go test` / the shell suites) need
`-ldflags=-linkmode=external` so the Mach-O has `LC_UUID`.

From a checkout:

```bash
./install.sh          # this tree; does not clone
tests/run.sh          # identity / spawn (builds bin/muxa)
tests/broker.sh       # broker integration
tests/install.sh      # ~/.muxa cache update
tests/tmux-facts.sh   # tmux behaviour muxa depends on
tests/e2e.sh          # live Claude / Cursor / Pi (optional)
```

Project-scoped `session-start` for hacking on muxa (spawned panes skip it):
`.claude/settings.json` and `.cursor/hooks.json` → `scripts/muxa-hook.sh`.
Put `bin/` on `PATH` in the panes, or `examples/agents.sh`.

```bash
tmux new -s agents
claude
muxa spawn --name cursor -- agent
muxa who
```

## Commands

| Command | What |
| --- | --- |
| `muxa register [--name --id --parent --kind]` | Set pane identity (spawn already does this for children) |
| `muxa adopt DEAD-ALIAS` | Re-parent orphans of a dead alias onto this root |
| `muxa spawn [--name NAME] [--cwd DIR] [--window] -- CMD` | Child pane in a tiled grid. cwd: `--cwd`, else `$PWD`, else pane path |
| `muxa dispatch [--name NAME] [--cwd DIR] [--brief-file F] -- CMD` | Spawn + first brief (stdin or `--brief-file`) |
| `muxa who` / `muxa who --json` | Roster |
| `muxa tail NAME [-n N]` | One-shot pane read |
| `muxa kill NAME\|ID` | Remove the pane |
| `muxa send NAME TEXT` | Enqueue mail |
| `muxa send --file F NAME` | Same; body read from file |
| `muxa send --json NAME TEXT` | Same; stdout `{"id","pane","from","to"}` |
| `muxa broker [start\|status\|stop]` | Paste broker |
| `muxa hook session-start [--kind KIND]` | Optional root self-registration |

Exits, JSON shapes, spawn cwd occupancy, and hook fail-open:
[SPEC — Identity](SPEC.md#identity), [Send API](SPEC.md#send-api).

## Spec

- [Reachability](SPEC.md#reachability)
- [Delivery](SPEC.md#delivery) (daemonization, free-detection, fail-closed send)
- [Etiquette](SPEC.md#etiquette-normative-for-agents)
- [muxa / command-post](SPEC.md#muxa--command-post-boundary)
- [Environment](SPEC.md#environment)
- [Trust](SPEC.md#trust)
