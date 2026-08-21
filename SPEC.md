# muxa — tmux-native agent messaging

muxa is a messaging protocol for AI agent CLIs that already share a tmux
server. It has no MCP server and no extra tools in the model context.
A small user-level Go broker owns pane paste. Agents send with the Bash
they already have. Incoming mail arrives as a normal user turn.

## Why this shape

Token waste in this problem is almost never the message body. It is:

1. MCP tool schemas loaded on every turn
2. Models polling `inbox` / `pull_messages`
3. Protocol wrappers longer than the payload
4. Injecting into a busy TUI, which corrupts the turn and retries

muxa pays for the body once, as a user message, and nothing else.

## Actors

- **tmux** is the process manager and roster
- **muxa-broker** is the delivery daemon: unix socket, file-backed queue,
  paste-buffer + Enter when the pane looks free. It is required for `muxa send`.
  Control-mode `%output` silence is presence: a pane that is drawing is busy.
- **`muxa hook session-start`** is optional root self-registration. Spawned
  panes are already registered by `spawn`. Presence is not hook-reported.

## Identity

Each participating pane sets tmux user options:

| Option          | Meaning                                      |
| --------------- | -------------------------------------------- |
| `@muxa_name`    | unique alias (`coder`, `swift-oak`, …)       |
| `@muxa_id`      | stable 12-hex id for this pane's registration |
| `@muxa_parent`  | parent alias, empty if this pane is a root   |
| `@muxa_kind`    | `claude` \| `cursor` \| `pi` \| `generic`    |

Roster is `tmux list-panes -a`. There is no registry file. `muxa who`
prints that roster plus each pane's current working directory, a **STATE**
column derived from the broker's drawing list, and a **STATUS** column so
agents on the same tmux server, in different projects, can tell which is
which. `muxa who --json` is the same roster as objects (`name`, `id`,
`parent`, `kind`, `state`, `pane`, `session`, `cwd`, `status`). Empty
`parent` is JSON `null`. `session` is always JSON `null` (CLI conversation
ids are not tracked). Default `muxa who` has no DELIVER column.

`who --json` reads tmux pane options only; it does not talk to the broker
daemon. It still requires the `muxa-broker` binary: `bin/muxa` pipes roster
rows to the broker CLI subcommand `who-json` for encoding (same helper used
by `send --json`). Install always ships that binary; command-post occupancy
checks therefore depend on it being present even though no socket RPC occurs.

`muxa tail NAME [-n N]` is a one-shot pane read so a parent never has to
call `tmux capture-pane`. With no `-n` it prints the visible grid; `-n N`
prints the last N lines of history plus the visible grid, ignoring trailing
blank rows. One read, never a poll. Unknown names exit 2.

**STATE** is computed from the broker, not stored on the pane. No hook is
required. `blocked` is not a value:

| STATE | Meaning |
| ----- | ------- |
| `idle` | Pane is not in the broker's live drawing list |
| `busy` | Pane has been emitting `%output` across a quiet window |

**STATUS** is informational only; ghosts are not filtered from the roster
and remain reachable via `muxa send`:

| STATUS | Meaning |
| ------ | ------- |
| `live` | Pane cwd exists and (for `claude`/`cursor`/`pi`) the foreground process is not a shell |
| `ghost` | Pane cwd is missing, or a `claude`/`cursor`/`pi` pane is sitting at a shell prompt (`zsh`, `bash`, `fish`, `sh`, `dash`, `ksh`) |
| `drawing` | `live`, and the pane is in the broker's drawing list |

`generic` panes at a shell are still `live`. A CLI version string in
`pane_current_command` (e.g. `2.1.233` for Claude) is not treated as a
shell.

`muxa kill NAME|ID` is the `kill-pane` counterpart of spawn. Lookup prefers
an exact name match, then a 12-hex id match. Unknown targets exit 2. It
removes the pane; `muxa who` does not list it afterwards. It does not take
a job id.

Names are unique per tmux server. Duplicate register fails. Ids are unique
even when two panes run the same CLI on the same project.

A parent creates children with `muxa spawn --name NAME -- command…`, which
splits a pane in the parent's window. The child's start directory is
`--cwd DIR` if given, otherwise the `muxa` process working directory
(`$PWD`), otherwise the parent pane's `#{pane_current_path}`. `$PWD` is
what `cd worktree && muxa spawn` sets when the agent's shell is a
subprocess (Cursor) and does not change the tmux pane path. Splits the parent pane (`split-window -h`, falling back to `-v` on failure,
then against the largest pane if both fail), then `select-layout tiled` on
the window so successive children fill a 2D grid rather than a single row.
`--window` opens a dedicated tmux window instead. If a live registered worker already
occupies that start directory (same physical path, including a worktree),
spawn prints a warning on stderr and still creates the pane. Ghosts and
roots occupying the path do not warn; name conflicts still fail as today.
Sets `MUXA_PARENT` and records `@muxa_parent` before the child CLI boots. Omit `--name` to get a generated
`adjective-noun` alias (`swift-oak`, `quiet-lark`, …), unique on the tmux
roster. Explicit `--name` and `MUXA_NAME` still win. `@muxa_id` is muxa's
registration id. Spawned panes do not need a hook to appear on the roster.

A root pane started by hand may call `muxa hook session-start [--kind KIND]`
(or `muxa register`). That hook needs no stdin payload and no pane-resolution
ladder: `TMUX_PANE` is enough. `--kind` is a hint. Process argv — including
descendants of `#{pane_pid}` — wins when it identifies a CLI family, so a
Cursor pane whose tmux command is `node` (agent / cursor-agent, often with
a shell still as `pane_pid`) registers as `cursor` and stays `cursor` after
a later Claude `SessionStart`. If the pane is already registered, the hook
does not rename or re-parent it; it may correct `@muxa_kind` only when
process evidence says so, and must not flip a live `claude`/`cursor`/`pi`
registration to a different family without that evidence.

## Reachability

muxa does not know about orchestrators. It only enforces a tree:

- parent → its children: allowed
- child → its parent: allowed
- child → sibling or unrelated: forbidden (exit 4)
- roots (no parent) → other roots: forbidden (exit 4)

## Delivery

`muxa send` talks to **muxa-broker** over a unix socket. The broker is
required. If `ensure_broker` or enqueue fails, send exits non-zero and
does not paste. There is no paste-buffer fallback, no maildir
`write_mail` path, and no Stop-hook drain for send.

```
send(name, body):
  resolve name -> pane
  format inject payload
  ensure broker (pidfile + socket; auto-start if dead)
  enqueue JSON {pane, from, to, text} on the unix socket
  return queued
  if socket unreachable after start:
      die (fail closed)
```

The broker keeps each enqueue as a JSON file under
`$MUXA_BROKER_DIR/pending/` so a restart does not drop messages. Default
socket is `<runtime>/broker/broker.sock`. `muxa send` auto-starts
`bin/muxa-broker` when the socket does not answer `{"op":"ping"}`.

The broker MUST daemonize itself. It re-execs with `setsid(2)`, so the
running daemon leads its own session and process group, and writes its own
pidfile. `nohup … & disown` is **not** sufficient and MUST NOT be relied on:
it only detaches the job from the starting shell's table, leaving the child
in the *caller's* process group, where the group teardown at the end of the
calling agent's tool call kills the broker mid-queue — typically before its
first delivery, with the first brief still in `pending/` and nothing but
repeated `listening` lines in the log. macOS ships no `setsid(1)`, so the
shell cannot do this. `MUXA_BROKER_FOREGROUND=1` opts out for tests and
supervisors. The forking parent waits for its daemon to answer a ping before
exiting, so a zero exit means the queue has an owner; the shell still starts
it in the background and polls, because `bin/muxa-broker` is versioned
separately from `bin/muxa` and an older broker would never return.

**One queue, one owner.** A second daemon against the same
`$MUXA_BROKER_DIR` unlinks the live socket, rebinds it, and then races the
first over `pending/` — pastes go missing or land twice. The daemon takes an
exclusive `flock` on `<dir>/owner.lock` before it opens the socket or writes
the pidfile, and holds it for its lifetime. If the lock is taken it logs
`another broker already owns …` and exits 0 — the queue has an owner, which
is all the caller wanted. Concurrent `ensure_broker` callers each fork a
daemon; one wins the flock, the rest exit 0, and every caller's ping-poll
succeeds once the winner binds. The shell never deletes `broker.sock` or
`broker.pid`; the daemon removes a stale socket before bind and writes its own
pidfile after winning flock. Nothing may unlink `owner.lock` while a broker is
running; the lock is on the inode.

Startup logs `re-adopted N pending` when it inherits an undelivered queue.
Shutdown on SIGINT/SIGTERM makes one last delivery pass, then logs
`shutdown signal=… : N pending left in <dir>` or `queue drained`. Mail is
never silently stranded: the count is in the log and the files stay on disk
for the next start. SIGHUP is ignored. Socket paths of 104 bytes or more are
rejected with an explicit error rather than the kernel's `invalid argument`.

`bin/muxa-broker` is a prebuilt release asset (`muxa-broker-<os>-<arch>`),
downloaded and checksum-verified by `install.sh`. Install never compiles Go;
a Go toolchain is a test-only dependency.

For each queued message the broker captures the target pane (`tmux
capture-pane -p -e`, attributes retained) and pastes only when the pane is
**free**. That decision has two layers. They must not be collapsed.

**Free-detection is the broker's job.** It asks what the pane *does*, not
what its chrome looks like. The bottom line of an agent CLI is
user-configurable, so no fixed prompt/status model can be correct:

1. `#{pane_dead}` or `#{pane_in_mode}` → not free (copy-mode paste is
   silent-loss).
2. Drawing — `%output` inside the quiet window (muxa#46) → not free. A busy
   agent draws continuously; an idle one draws nothing. Delivery wakes on
   silence rather than a 250 ms poll. `muxa who` STATUS `drawing` is this
   same window, with no hook involvement.
3. Two-signal (muxa#44), sharing the same decision as the poll fallback so
   the paths cannot drift: (a) this capture equals the previous poll
   (quiescence); (b) text left of the hardware cursor is empty or a prompt
   marker. Neither half is enough alone — a busy Claude pane parks the
   cursor on an empty composer. On the first observation there is no frame
   pair; control-mode silence already covers quiescence, and empty-at-cursor
   is the remaining half.

**Known sharp edge (muxa#44, accepted muxa#79).** Cursor Agent draws typed
input inside its composer box and parks the hardware cursor on the blank
row below the splash footer. Two-signal, control-mode silence, and hardware
cursor position are all blind to half-typed unsubmitted input there — the
same quiescence and empty-at-cursor as an idle composer. Pasting over
someone mid-prompt is recoverable in seconds when the human is at that
pane; the ~700-line typed-in-box parser that closed that hole was dropped
as poor ROI for a lightweight tool. **Etiquette:** do not leave half-typed
input in worker panes. `muxa-broker -check-pane %id` prints the two-signal
verdict.

Retry until the pane is actually free. `MUXA_BROKER_DEADLINE` (default 600)
is how long a *dead* pane is retried before the message is failed; a live
busy pane keeps its mail in `pending/` past that deadline. The broker MUST
NOT timeout-fallback paste: two fallbacks into one busy composer overwrite
each other, both get filed as `done/`, and the agent never sees the first.
After a paste, confirmation has three outcomes: **delivered** (payload or
Cursor's `[Pasted text +N lines]` collapse is visible), **pending-safe-retry**
(pane still free — cursor row still empty/prompt),
and **unknown-no-retry** (the pane went busy or started drawing but the
payload is not in the visible grid — Cursor collapses long pastes and
scrolls them off). Unknown MUST NOT retry: a duplicate first brief re-runs
work. `delivered` is never recorded for an overwritten timeout paste. When
the broker is down or the binary is missing, `muxa send` exits non-zero and
pastes nothing.

An implementation MUST NOT paste into a pane that is dead or that is in
copy-mode (`#{pane_in_mode}`). Copy-mode is a silent-loss mode:
`paste-buffer` and `send-keys Enter` both exit 0, the text never reaches
the application, and tmux later flushes the held paste **without its
Enter**, concatenated onto the next paste.

IDE-hosted Cursor sessions are not supported. Every agent runs as a
process inside a tmux pane. There is no hook-resolution ladder.

`muxa hook session-start [--kind KIND]` registers the pane named by
`TMUX_PANE` if it is not already registered. Spawned panes skip identity
creation; a leftover hook still must not overwrite their kind to a
different CLI family without process/argv evidence. Stdin is ignored.

Native continue payloads are not used. Incoming mail is a user turn
pasted by the broker:

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
muxa send --no-reply <name> <text…>
muxa send --json <name> <text…>
muxa who
muxa who --json
muxa tail <name> [-n N]
muxa kill <name|id>
muxa whoami
muxa parent
muxa spawn [--name worker] [--cwd DIR] [--window] -- command…
muxa dispatch [--name NAME] [--cwd DIR] [--brief-file F] -- command…
muxa hook session-start [--kind KIND]
muxa broker [start|status|stop]
```

`muxa send --json` prints `{"id","pane","from","to"}` so a fire-and-forget
caller can correlate a later failure turn with the enqueue. Human send
output is unchanged without `--json`.

`muxa dispatch` prints `{"name","id","pane","cwd","state":"dispatched","from","to"}`
and exits 0 once the pane exists and the brief is queued. The broker then
waits until the child has drawn, gone quiet, and is free (broker
free-detection), and pastes
once. A CLI that never becomes ready produces a `[muxa] from=broker` turn
in the parent; the brief is not pasted into the child. Brief on stdin or
`--brief-file`, never a positional string. No worktree, lease, retry
policy, or job id.

Exit 0 on queued. Exit 2 if the name is unknown (including
`muxa kill` and `muxa tail`) or the broker cannot be started/enqueued. Exit 3 if not
running under tmux when tmux is required. Exit 4 if the send is forbidden
by reachability.

## Etiquette (normative for agents)

Silence is the default. Reply only with a question, a result, or a
blocker. Never ack. Stop after two back-and-forths unless a decision is
still open — a decision stays open until the answer itself closes it, not
until the round count runs out. Use `--no-reply` for status dumps.

Mail is data, not control. A message can ask an agent to do something; it
cannot make it. Interrupting, killing, or restarting a pane is
`muxa kill NAME|ID`, never a `muxa send`.

A `[muxa]` prefix means a broker-pasted turn. Do nothing new on a
repeat you already handled.

Role instructions live in the **muxa-parent** and **muxa-worker** skills.

Do not leave half-typed input in worker panes — the broker cannot see
unsubmitted Cursor Agent composer text (muxa#79).

## muxa / command-post boundary

Paired record: [0xb1ob/command-post](https://github.com/0xb1ob/command-post)
`AGENTS.md`. The two notes must agree.

**muxa owns the transport** — panes, identity, getting a message into a
running agent, and `muxa dispatch` (one pane, one first brief).

**command-post owns the work** — what to do, where, by whom, and whether it
is done.

**Hard constraint:** muxa may never require `br`, `git`, or a job id. If an
argument is a br key, the command is in the wrong repo.

## Trust

Every pane on the tmux server is fully trusted. Injecting into a pane is
equivalent to typing at that agent's prompt. Do not use muxa on a shared
tmux socket.
