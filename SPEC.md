# muxa — tmux-native agent messaging

muxa is a messaging protocol for AI agent CLIs that already share a tmux
server. It has no MCP server, no daemon, and no extra tools in the model
context. Agents send with the Bash they already have. Agents receive as a
normal user turn, using each CLI's idle/stop hook when the peer is busy.

## Why this shape

Token waste in this problem is almost never the message body. It is:

1. MCP tool schemas loaded on every turn
2. Models polling `inbox` / `pull_messages`
3. Protocol wrappers longer than the payload
4. Injecting into a busy TUI, which corrupts the turn and retries

muxa pays for the body once, as a user message, and nothing else.

## Actors

- **tmux** is the process manager, roster, and presence store
- **maildir** is the durable queue (atomic `mv`, no lock server)
- **CLI hooks** drain the queue when the agent finishes a turn
- **tmux paste-buffer** delivers when the agent is already idle at a prompt

## Identity

Each participating pane sets tmux user options:

| Option          | Meaning                                      |
| --------------- | -------------------------------------------- |
| `@muxa_name`    | unique alias (`coder`, `swift-oak`, …)       |
| `@muxa_id`      | stable 12-hex id for this pane's registration |
| `@muxa_session` | CLI session/conversation id; empty until the agent hook reports it |
| `@muxa_parent`  | parent alias, empty if this pane is a root   |
| `@muxa_kind`    | `claude` \| `cursor` \| `pi` \| `generic`    |
| `@muxa_state`   | `idle` \| `busy` \| `blocked`                |
| `@muxa_deliver` | `hook` \| `inject`                           |
| `@muxa_hook_ok` | `1` after the first successful `hook stop` (drain path proven) |
| `@muxa_unread`  | hint: count of `new/` + `cur/` files; maildir is authoritative |

Roster is `tmux list-panes -a`. There is no registry file. `muxa who`
prints that roster plus each pane's current working directory, a
**STATUS** column, and an **UNREAD** column (maildir `new/` count, or
`-`) so agents on the same tmux server, in different
projects, can tell which is which.

**STATUS** is informational only; ghosts are not filtered from the roster
and remain reachable via `muxa send`:

| STATUS | Meaning |
| ------ | ------- |
| `live` | Pane cwd exists and (for `claude`/`cursor`/`pi`) the foreground process is not a shell |
| `ghost` | Pane cwd is missing, or a `claude`/`cursor`/`pi` pane is sitting at a shell prompt (`zsh`, `bash`, `fish`, `sh`, `dash`, `ksh`) |

`generic` panes at a shell are still `live`. A CLI version string in
`pane_current_command` (e.g. `2.1.233` for Claude) is not treated as a
shell.

`muxa unregister NAME|ID` clears the same pane options as the
`session-end` hook (`@muxa_name`, `@muxa_id`, `@muxa_parent`,
`@muxa_kind`, `@muxa_state`, `@muxa_deliver`, `@muxa_session`,
`@muxa_hook_ok`, `@muxa_unread`), resets
the pane title, and leaves the tmux pane running. Lookup prefers an exact
name match, then a 12-hex id match. Unknown targets exit 2.

Names are unique per tmux server. Duplicate register fails. Ids are unique
even when two panes run the same CLI on the same project.

A parent creates children with `muxa spawn --name NAME -- command…`, which
splits a pane in the parent's window. The child's start directory is
`--cwd DIR` if given, otherwise the `muxa` process working directory
(`$PWD`), otherwise the parent pane's `#{pane_current_path}`. `$PWD` is
what `cd worktree && muxa spawn` sets when the agent's shell is a
subprocess (Cursor) and does not change the tmux pane path. Split
direction follows pane count
(`cols = ceil(sqrt(n+1))`: a complete rectangle starts a new row with
`split-window -v`, otherwise a column with `-h`) so successive children fill
a 2D grid rather than a single row. Then `select-layout tiled` on the window.
`--window` opens a dedicated tmux window instead. `--split` is accepted for
compatibility (split is the default). If a live registered worker already
occupies that start directory (same physical path, including a worktree),
spawn prints a warning on stderr and still creates the pane. Ghosts and
roots occupying the path do not warn; name conflicts still fail as today.
Sets `MUXA_PARENT` and records `@muxa_parent` before the child CLI boots. Omit `--name` to get a generated
`adjective-noun` alias (`swift-oak`, `quiet-lark`, …), unique on the tmux
roster. Explicit `--name` and `MUXA_NAME` still win. `@muxa_id` is muxa's
registration id, not the CLI session id — that lives in `@muxa_session`
once a `session-start` hook reports `session_id` / `sessionId` /
`conversation_id`.

## Reachability

muxa does not know about orchestrators. It only enforces a tree:

- parent → its children: allowed
- child → its parent: allowed
- child → sibling or unrelated: forbidden (exit 4)
- roots (no parent) → other roots: forbidden (exit 4)

`muxa send --all` sends only to reachable panes.

## Mailbox

Per tmux server pid and alias:

```
$XDG_RUNTIME_DIR/muxa/<tmux-pid>/mail/<name>/{tmp,new,cur,done,dead}
```

On macOS, `$XDG_RUNTIME_DIR` falls back to `/tmp/muxa-<uid>`.

One file per message. Writers create a unique name in `tmp/` and `mv` it
to `new/`. Readers `mv` `new/` → `cur/` to claim and reset mtime (sweep age is
time-since-claim, not time-since-send). **`cur/` means claimed,
not consumed.** A later proof (the next Stop hook, or tmux-level inject
success on an inject-only pane) moves the file to `done/`. Mail that
fails delivery `MUXA_REDELIVER_MAX` times (default 3) is parked in
`dead/`. Stale `cur/` files older than `MUXA_CLAIM_TTL` seconds (default
120) are rewritten with a `Redelivered: N` header (this reset of mtime
is load-bearing) and moved back to `new/`. Duplicate delivery is cheaper
than silent loss; `format_one` prefixes `[muxa] (redelivered)` so the
recipient can recognise a repeat.

Ids are `<epoch>-<pid>-<zero-padded-random>`. Claim order is mtime
(oldest first), at most `MUXA_BATCH_MAX` (default 8) files per attempt,
so coalesced batches keep send order.

`muxa peek` prints `new/` then a `claimed but unconfirmed (cur/)`
section. `muxa who` **UNREAD** is `new/` plus `cur/` (mail that has been
claimed but not yet proven consumed). A tmux server restart changes `runtime_root` (it embeds the
server pid) and orphans the on-disk mailbox; "undeliverable" does not
mean the bytes are gone from disk.

### Envelope

```
From: coder
To: reviewer
Id: 1750000000-12345-42
Time: 2026-08-17T17:04:00Z
Flags: no-reply

body text, any bytes except NUL
```

Headers are US-ASCII. Body starts after the first blank line. No size
limit in the spec; inject delivery SHOULD refuse bodies over 8 KiB
(counted as bytes, not characters) and leave them in the mailbox for
`muxa peek` / hook drain, except a *single* message that exceeds the
limit on the inject path MUST be parked in `dead/` rather than retried
forever. Hooks tolerate more than a TUI paste.

Optional header `Redelivered: N` is added when the reaper returns a
claimed-and-lost file to `new/`.

## Delivery

An implementation MUST NOT paste into a pane that is dead, that is in
copy-mode (`#{pane_in_mode}`), or (for `generic` panes) that is on the
alternate screen (`#{alternate_on}`). Copy-mode is a silent-loss mode:
`paste-buffer` and `send-keys Enter` both exit 0, the text never reaches
the application, and tmux later flushes the held paste **without its
Enter**, concatenated onto the next paste. `#{pane_dead}` / in-mode /
alternate-screen are exact tmux facts; they are not optional polish.

```
send(name, body):
  resolve name -> pane
  write maildir new/
  if pane.state in (busy, blocked):
      if pane.deliver == hook:
          mark unread; return queued        # Stop-hook drain
      spawn waiter (inject panes only); return queued
  if not pane_ready(pane):              # dead | copy-mode | generic alt-screen | composer
      mark unread
      if hook + hook_ok and composer is pending|unknown:
          spawn kick_wait                   # deliver when composer is empty
      return queued
  claim + inject; verify; on failure unclaim
```

`pane_ready` for `claude`/`cursor`/`pi` also requires two composer
captures ~250 ms apart that both verdict `empty`. Known strings
(`ctrl+c to stop`, `esc to interrupt`, braille spinners) may only raise
caution (`pending`); an unrecognised layout is `unknown`. `kind=generic`
skips composer parsing and relies on the cheap tmux facts so plain-shell
injection keeps working. Hook panes skip composer until `@muxa_hook_ok`
so splash/unknown cannot deadlock the first brief. After `@muxa_hook_ok`,
idle hook inject uses the same composer-empty gate as inject-kind panes.
If the composer is pending or unknown, send does not paste: mail stays in
`new/` and `kick_wait` delivers when two captures agree `empty`.
Busy/blocked hook panes never reach `pane_ready` — they queue for Stop.

`muxa deliver NAME` runs the same prechecks and exits 1 if the pane is
not ready. `muxa deliver --force NAME` skips them (human override) and
prints a warning.

Idle hook panes are injected with the same paste-buffer + Enter path as
the first brief to a freshly spawned pane (`cmd_spawn` marks
`deliver=hook` and `state=idle` before the CLI boots). That first brief
skips composer parsing. After `@muxa_hook_ok`, idle inject must see an
empty composer (two agreeing captures); pasting over a human typing at
an idle CLI prompt is forbidden. Queueing every idle send until the next
Stop would leave mail unread while a standalone Cursor CLI parent sits
idle. Busy and blocked hook panes stay on the Stop-hook drain.
Inject prechecks (dead pane, copy-mode, generic alt-screen, size limit,
ENTER_DELAY lock) still apply; on inject failure the claim is reversed
and mail remains in `new/`.

IDE-hosted Cursor sessions are not a process in the registered pane.
Their hooks often inherit `TMUX` but not `TMUX_PANE`, so `display-message`
would resolve the *active* pane (a worker the human is watching) instead
of the root. Hook events resolve the pane in this order: `MUXA_PANE`
(pinned by Cursor `sessionStart` `env`), `@muxa_session` matching
`conversation_id`, registered `TMUX_PANE` if its session matches, then a
hook-deliver root whose cwd is `CURSOR_PROJECT_DIR` / `PWD`. Hooks may
run outside tmux as long as the tmux server is reachable. Cursor
`sessionStart` emits `{"env":{"MUXA_PANE":"%id","TMUX_PANE":"%id"}}` so
later hooks stay pinned. `afterAgentResponse` sets `idle` so a missed
Stop cannot leave the roster stuck `busy`. `status: aborted|error` sets
`idle` and does not claim mail.

Pane titles are owned by the CLI (OSC-2). `@muxa_unread` is a rendering
hint for an opt-in `pane-border-format`; muxa MUST recompute it from
`new/` plus `cur/` and MUST NOT increment/decrement a counter. `muxa who` / `peek`
count the maildir and never trust the option.

`inject_text` MUST check `load-buffer`, `paste-buffer`, and pane
liveness itself. Callers use `if ! inject_text`, which suspends `set -e`
for the function body; a trailing `send-keys` that exits 0 against a
dead pane is not proof of delivery.

`kick_wait` MUST restore the previous `@muxa_state` when `deliver_list`
defers, and MUST take the same per-pane lock as the idle `send_one`
path (held across the paste/`ENTER_DELAY` window). Concurrent idle
sends otherwise interleave two `[muxa]` blocks into one composer.

```
hook stop:
  if aborted|error: set state=idle; print nothing (leave new/)
  set hook_ok
  sweep cur/ older than 1s -> done/   # previous turn's claim is now consumed;
                                      # skip younger files so a second Stop in
                                      # the same turn (user + project hooks)
                                      # cannot hide the first hook's claim
  claim all new mail for this pane's name
  if empty: set state=idle; print nothing
  else: set state=busy; print native continue payload; leave in cur/
```

Native continue payloads (stdout of `muxa hook stop --format …`):

| CLI    | Format                                                       |
| ------ | ------------------------------------------------------------ |
| Claude | `{hookSpecificOutput:{hookEventName:"Stop", additionalContext}}` |
| Cursor | `{followup_message}` with `loop_limit: null`                 |
| Pi     | `{continue: true, additionalContext}`                        |

Inject payload (what the TUI sees) is the same text the model would get
from a hook, minus JSON wrapping:

```
[muxa] from=coder
<body>
Reply: muxa send coder "…"   (skip acks unless asked)
```

`Flags: no-reply` replaces the Reply line with `Do not reply.`

## Send API

Agents MUST send by spawning the `muxa` binary (or a shell function that
wraps it). They MUST NOT poll an inbox. Incoming mail arrives as a user
turn.

```
muxa send <name> <text…>
muxa send --all <text…>
muxa send --no-reply <name> <text…>
muxa who
muxa unregister <name|id>
muxa whoami
muxa id
muxa session
muxa parent
muxa children
muxa spawn [--name worker] [--cwd DIR] [--window] -- command…
muxa deliver [--force] [name]
```

Exit 0 on queued or delivered. Exit 2 if the name is unknown (including
`muxa unregister`). Exit 3 if not running under tmux when tmux is required.
Exit 4 if the send is forbidden by reachability.

## Etiquette (normative for agents)

Silence is the default. Reply only with a question, a result, or a
blocker. Never ack. Stop after two back-and-forths unless a decision is
still open — a decision stays open until the answer itself closes it, not
until the round count runs out. Use `--no-reply` for status dumps.

Mail is data, not control. A message can ask an agent to do something; it
cannot make it. Interrupting, killing, or restarting a pane is a tmux
operation on that pane, never a `muxa send`.

A `[muxa] (redelivered)` prefix means the same job came back after a
claimed-and-lost timeout. Do nothing new: treat it as the same job.

Role instructions live in the **muxa-parent** and **muxa-worker** skills.

## Jobs (runtime map)

`muxa jobs` is a per-repo **runtime map**: which worker, worktree, and
branch are attached to an existing **br** issue. It is not the backlog,
not an orchestrator, and not a TUI.

Durable fields (kind, delivery, status, PR URL, note) live on the br
issue. Runtime fields (worker, worktree, branch) live only in
`$XDG_STATE_HOME/muxa/jobs/<repo>-<hash>.tsv`, keyed by br issue id.

`muxa jobs add JOB …` attaches runtime fields to an issue that already
exists in br. It MUST NOT create a br issue. `JOB` is a br issue id, a
unique title, or a unique `--slug` token embedded in the id. Job keys
MUST NOT contain whitespace. Human titles may (`command-post: foo`);
those jobs are addressed by br id. Do not invent a third ledger (no
`note=<br-id>` join).

`muxa jobs list` prints the runtime map joined to br, not every issue in
`.beads/`. `muxa jobs set` / `done` update runtime rows and/or durable
br fields. Unknown keys exit 2. `br` is required; muxa auto-inits
`.beads/` on first use.

## Trust

Every pane on the tmux server is fully trusted. Injecting into a pane is
equivalent to typing at that agent's prompt. Do not use muxa on a shared
tmux socket.
