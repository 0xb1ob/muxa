# muxa — tmux-native agent messaging

This file is the only normative specification. README and skills are not a
second source of MUST / MUST NOT.

muxa is a messaging protocol for AI agent CLIs that already share a tmux
server. It has no MCP server and no extra tools in the model context.
A small user-level Go binary owns the CLI and pane paste. Agents send with
the Bash they already have. Incoming mail arrives as a normal user turn.

## Why this shape

Token waste in this problem is almost never the message body. It is:

1. MCP tool schemas loaded on every turn
2. Models polling `inbox` / `pull_messages`
3. Protocol wrappers longer than the payload
4. Injecting into a busy TUI, which corrupts the turn and retries

muxa pays for the body once, as a user message, and nothing else.

## Actors

- **tmux** is the process manager and roster
- **muxa** is one Go binary: the CLI (`register`, `adopt`, `spawn`, `dispatch`, `who`,
  `tail`, `kill`, `whoami`, `parent`, `send`, `hook`, `broker`) and the
  delivery daemon (`muxa broker`). The daemon owns a unix socket, a
  file-backed queue, and paste-buffer + Enter when the pane looks free. It
  is required for `muxa send`. Control-mode `%output` silence is presence: a
  pane that is drawing is busy.
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
prints that roster plus each pane's current working directory and a
**STATE** column so agents on the same tmux server, in different projects,
can tell which is which. `muxa who --json` is the same roster as objects
(`name`, `id`, `parent`, `kind`, `state`, `pane`, `session`, `cwd`). Empty
`parent` is JSON `null`. `session` is always JSON `null` (CLI conversation
ids are not tracked). Default `muxa who` has no DELIVER column.

`who --json` reads tmux pane options and encodes JSON in-process. It does
not talk to the broker daemon and does not need a second binary. Occupancy
checks consume `cwd` and `state` from that JSON, and spawn/dispatch cwd
warnings use the same `loadRoster` path — they MUST agree on which live
workers occupy a path. A pane with `@muxa_parent` or `@muxa_id` but no
`@muxa_name` yet still appears as `pending-<id>` (muxa#121). Spawn sets all
registration options in one tmux command before layout or focus changes.
`busy` still consults the daemon's drawing list when the socket is up, and
treats a down broker as not-drawing (idle, unless ghost).

`muxa tail NAME [-n N]` is a one-shot pane read so a parent never has to
call `tmux capture-pane`. With no `-n` it prints the visible grid; `-n N`
prints the last N lines of history plus the visible grid, ignoring trailing
blank rows. One read, never a poll. Unknown names exit 2.

**STATE** is computed, not stored on the pane. No hook is required.
`blocked`, `live`, and `drawing` are not values. Ghosts are not filtered
from the roster and remain reachable via `muxa send`:

| STATE | Meaning |
| ----- | ------- |
| `idle` | Registered, reachable, not in the broker's drawing list |
| `busy` | Registered, in the broker's drawing list (emitting `%output` across a quiet window) |
| `ghost` | Pane cwd is missing, or a `claude`/`cursor`/`pi` pane is sitting at a shell prompt (`zsh`, `bash`, `fish`, `sh`, `dash`, `ksh`) |

`generic` panes at a shell are still `idle` (or `busy` if drawing). A CLI
version string in `pane_current_command` (e.g. `2.1.233` for Claude) is
not treated as a shell. Ghost wins over drawing: a missing cwd is `ghost`,
not `busy`.

`muxa kill NAME|ID` is the `kill-pane` counterpart of spawn. Lookup prefers
an exact name match, then a 12-hex id match. Unknown targets exit 2. It
removes the pane; `muxa who` does not list it afterwards. It does not take
a job id.

Names are unique per tmux server. Duplicate register fails. Ids are unique
even when two panes run the same CLI on the same project.

When a parent pane dies, its children keep `@muxa_parent` set to the dead
alias. `muxa who` lists them with that parent string even though no roster
row has that name. A registered **root** may run `muxa adopt DEAD-ALIAS` to
set `@muxa_parent` on every live orphan to the caller's own alias, restoring
parent↔child mail without impersonating the dead alias. The dead name stays
vacated unless someone later `muxa register --name DEAD-ALIAS` on a new
pane (vacated-name registration — same live-roster uniqueness rule as
today, not orphan recovery).

A parent creates children with `muxa spawn --name NAME -- command…`, which
splits a pane in the parent's window. The child's start directory is
`--cwd DIR` if given, otherwise the `muxa` process working directory
(`$PWD`), otherwise the parent pane's `#{pane_current_path}`. `$PWD` is
what `cd worktree && muxa spawn` sets when the agent's shell is a
subprocess (Cursor) and does not change the tmux pane path. Splits the parent pane (`split-window -h`, falling back to `-v` on failure,
then against the largest pane if both fail), then `select-layout tiled` on
the window so successive children fill a 2D grid rather than a single row,
then `select-pane` back to the parent so spawn does not steal keyboard
focus (muxa#111).
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
ladder: `TMUX_PANE` is enough. `--kind` is a hint: process argv of `#{pane_pid}`
and its direct children wins, otherwise `--kind`, otherwise keep existing
`@muxa_kind`, otherwise `generic`. If the pane is already registered, the hook
does not rename or re-parent it. Presence events (`busy`, `idle`, `blocked`,
`stop`) are not part of muxa.

`muxa hook` fails open. It runs from registrations inside agent harnesses,
where a non-zero exit — 2 especially — denies the tool call or turn that
fired it. An unknown event warns on stderr and exits 0; outside tmux the
hook exits 0 silently. A stale registration left by an older muxa must
degrade to a warning, never lock an agent out of its own session.

## Reachability

muxa does not know about orchestrators. It only enforces a tree:

- parent → its children: allowed
- child → its parent: allowed
- child → sibling or unrelated: forbidden (exit 4)
- roots (no parent) → other roots: forbidden (exit 4)

`muxa adopt DEAD-ALIAS` is roster-only: a registered root re-parents every
live pane whose `@muxa_parent` equals `DEAD-ALIAS` onto the caller. It does
not paste into children, does not take the dead alias onto the caller, and
does not notify anyone. Refuses if the caller is unregistered, is not a root,
equals `DEAD-ALIAS`, or `DEAD-ALIAS` is still a live roster name (including
`ghost`). Zero orphans exit 2. Child process `MUXA_PARENT` env stays stale
until that process restarts; `@muxa_parent` is the source of truth.

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
the same binary as the daemon when the socket does not answer `{"op":"ping"}`.

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
exiting, so a zero exit means the queue has an owner. The CLI copies itself
into `$MUXA_BROKER_DIR/muxa-broker` and launches that file so a live daemon
is never overwritten by `go build -o`.

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

`muxa` is a prebuilt release asset (`muxa-<os>-<arch>`), downloaded and
checksum-verified by `install.sh`. Install never compiles Go and never
places a second binary; a Go toolchain is a test-only dependency.

For each queued message the broker captures the target pane (`tmux
capture-pane -p -e`, attributes retained) and pastes only when the pane is
**free**. That decision has two layers. They must not be collapsed.

**Free-detection is the broker's job.** It asks what the pane *does*, not
what its chrome looks like. The bottom line of an agent CLI is
user-configurable, so no fixed prompt/status model can be correct:

1. `#{pane_dead}` or `#{pane_in_mode}` → not free (copy-mode paste is
   silent-loss). A pane id the server has *forgotten* counts as dead:
   `display-message` exits 0 for it and expands every pane format to the
   empty string, so an empty `#{pane_dead}` MUST NOT be read as alive
   (muxa#124).
2. Drawing — `%output` inside the quiet window (muxa#46) → not free. A busy
   agent draws continuously; an idle one draws nothing. Delivery wakes on
   silence rather than a 250 ms poll. `muxa who` STATE `busy` is this
   same window, with no hook involvement.
3. Two-signal (muxa#44), sharing the same decision as the poll fallback so
   the paths cannot drift: (a) this capture equals the previous poll
   (quiescence); (b) text left of the hardware cursor is empty or a prompt
   marker. Neither half is enough alone — a busy Claude pane parks the
   cursor on an empty composer. On the first observation there is no frame
   pair; control-mode silence already covers quiescence, and empty-at-cursor
   is the remaining half.

**Composer-box gate (muxa#111).** Agent CLIs that draw a half-block composer
box (Cursor, Claude, pi) expose typed or collapsed-paste input on the row
between the `▄`/`▀` borders. The broker MUST treat non-placeholder content
there as not free — including `[Pasted text …]` sitting unsubmitted — and
MUST NOT paste a first brief over it. Past deadline, `Kind=dispatch` is
failed and the parent gets a `[muxa] from=broker` **refused** turn (never
the brief body). Later mail stays queued until the composer clears. Immediately before
paste, the broker re-checks the composer box on a fresh capture so a
quiescent two-signal pair cannot race operator typing (muxa#116).

**Known sharp edge (muxa#44).** Two-signal, control-mode silence, and
hardware cursor position are still blind to half-typed input that never
reaches the composer box (e.g. a shell prompt). **Etiquette:** do not leave
half-typed input in worker panes. `muxa -check-pane %id` prints the
two-signal verdict.

Retry until the pane is actually free. `MUXA_BROKER_DEADLINE` (default 600)
is how long a *dead or missing* pane is retried before the message is
failed; a live busy pane keeps its mail in `pending/` past that deadline.
The broker MUST NOT timeout-fallback paste: two fallbacks into one busy
composer overwrite each other, both get filed as `done/`, and the agent
never sees the first.

Every retry path MUST consult that deadline. A message that cannot make
progress — the pane is dead or gone, the free-check keeps erroring, an
`Inject` backoff outlives the deadline — MUST end in `failed/`, never spin
in `pending/` indefinitely. A repeated per-message delivery error MUST be
rate-limited in the log rather than written once per poll tick: an
undeliverable message produced 22 MB of two alternating lines in eight
hours and buried every other event in `broker.log` (muxa#124).

**At-most-once (muxa#129).** A successful `Inject` — `load-buffer`,
`paste-buffer -p -d` and `send-keys Enter` all exiting 0 — is the delivery
boundary. Past it the broker MUST file the message `done/` and MUST NOT
paste that payload into that pane again, for **every** `@muxa_kind` and for
both mail (`muxa send`) and first briefs (`Kind=dispatch`). No post-paste
observation may cause a second paste.

The one retryable failure is `Inject` itself returning an error: a tmux
command exited non-zero, nothing was pasted, and there is nothing to
duplicate. That path MUST be bounded — at most 3 attempts, backing off from
500 ms and doubling — after which the message is filed `failed/`, and a
dispatch additionally gets its parent a `[muxa] from=broker` failure turn.

Why that direction. Whether a CLI *displays* pasted text is a rendering
question, not a delivery question, and the broker can only infer it by
scraping chrome it neither controls nor versions. The two error directions
are not symmetric. **Loss is visible**: the reply never arrives, the parent
notices, and it can resend deliberately. **Duplication is invisible** until
someone notices doubled work, because a receiving agent cannot tell a
duplicate envelope from a genuine repeat instruction — it does the job
twice. muxa therefore resolves every inconclusive scrape to "delivered,
unverified", never to "not delivered, try again". Genuine at-least-once
would need a real acknowledgement channel — the receiving agent replying —
not a screen scrape; that is the caller's concern, not the transport's.

Post-paste confirmation survives as **logging confidence** only. The broker
MUST still compute and record it, so an operator can tell these apart:

| Confidence | What the broker saw after a successful `Inject` |
| ---------- | ----------------------------------------------- |
| **confirmed** | the pane reacted — cursor row no longer empty/prompt, control-mode drawing, or an agent turn started (e.g. `ctrl+c to stop`) — and, for mail, the `[muxa] from=` envelope is in the capture or history (muxa#116) |
| **unverified** | anything else: the pane stayed free, the payload never appeared, `@muxa_kind` consumes paste without echoing it (muxa#127), or a collapsed `[Pasted text …]` was still visible with no turn started |

The broker does not match dispatch payloads against pane captures; both
outcomes file `done/` either way.

The parent is mailed only when a **dispatch** confirm is **unsubmitted** —
collapsed `[Pasted text …]` still visible and no agent turn started after
one beat. That is positive evidence the brief did not submit. Generic
inconclusive confirm MUST NOT mail the parent: it is anti-correlated with
real failure (muxa#110). Mail has no parent pane to warn and enqueues
nothing.

Mail delivery matches only the `[muxa] from=` envelope marker. `delivered` is
never recorded for an overwritten timeout paste. When the broker is down
or the binary is missing, `muxa send` exits non-zero and pastes nothing.

**Consuming CLIs (muxa#127).** A `kind=claude` pane mid-turn takes a paste
into its own input queue and echoes nothing: the payload is absent from the
capture *and* from the history on a **successful** paste. muxa#128 made that
one kind at-most-once. muxa#129 generalises it, because the broker cannot
know which CLI, UI restyle or theme reproduces the same shape next. The kind
is still read from `@muxa_kind`, a roster fact — it now names *why* a
payload is invisible in the log instead of deciding whether to retry. It
does not model pane chrome.

An implementation MUST NOT paste into a pane that is dead or that is in
copy-mode (`#{pane_in_mode}`). Copy-mode is a silent-loss mode:
`paste-buffer` and `send-keys Enter` both exit 0, the text never reaches
the application, and tmux later flushes the held paste **without its
Enter**, concatenated onto the next paste.

IDE-hosted Cursor sessions are not supported. Every agent runs as a
process inside a tmux pane. There is no hook-resolution ladder.

`muxa hook session-start [--kind KIND]` registers the pane named by
`TMUX_PANE` if it is not already registered. Spawned panes skip identity
creation. Stdin is ignored.

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
muxa send --file <file> <name>
muxa send --no-reply <name> <text…>
muxa send --json <name> <text…>
muxa who
muxa who --json
muxa tail <name> [-n N]
muxa kill <name|id>
muxa adopt <dead-alias>
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
in the parent; the brief is not pasted into the child. If the pane became
ready and Inject ran but confirm stayed inconclusive (paste accepted, pane
still looked free), the brief is filed `done/` as usual — at-most-once,
never retried — with no parent turn. If confirm is **unsubmitted** (paste
collapse visible, no agent turn after one beat), the parent gets a
`[muxa] from=broker` turn saying so; envelope facts only, never the brief
body. Silence after a never-ready or refused turn is the bug; inconclusive
confirm is not a failure report. Brief on stdin or `--brief-file`, never a
positional string. No worktree, lease, retry policy, or job id.

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

Do not leave half-typed input in worker panes — the broker checks
composer-box content but cannot see unsubmitted shell-prompt typing.

## muxa / command-post boundary

**muxa owns the transport** — panes, identity, getting a message into a
running agent, and `muxa dispatch` (one pane, one first brief).

**command-post owns the work** — what to do, where, by whom, and whether it
is done.

[command-post](https://github.com/0xb1ob/command-post) cites this SPEC for
transport semantics and MUST NOT restate them. Muxa does not keep a paired
note in lockstep with command-post.

**Hard constraint:** muxa may never require `br`, `git`, or a job id. If an
argument is a br key, the command is in the wrong repo.

## Environment

| Variable | Default | What |
| --- | --- | --- |
| `MUXA_BROKER_DIR` | `<runtime>/broker` | File-backed queue + pidfile + log |
| `MUXA_BROKER_SOCK` | `$MUXA_BROKER_DIR/broker.sock` | Unix socket |
| `MUXA_BROKER_PID` | `$MUXA_BROKER_DIR/broker.pid` | Pidfile |
| `MUXA_BROKER_BIN` | this `muxa` binary | Optional override of the daemon executable |
| `MUXA_BROKER_DEADLINE` | `600` | Seconds after which a *dead or missing* pane's mail is failed; a live busy pane stays queued |
| `MUXA_BROKER_POLL_MS` | `250` | Broker retry interval (fallback if control-mode attach fails) |
| `MUXA_BROKER_QUIET_MS` | `250` | Control-mode silence window before a pane is considered not drawing |
| `MUXA_BROKER_VERSION` | latest release | Install-time: release tag to fetch the muxa asset from |
| `MUXA_BROKER_URL` | unset | Install-time: exact asset URL (skips the checksum lookup) |
| `MUXA_BROKER_BASE_URL` | `https://github.com/0xb1ob/muxa/releases` | Install-time: release base URL |
| `MUXA_BROKER_SKIP_VERIFY` | `0` | Install-time: `1` skips `SHA256SUMS` verification |
| `MUXA_TMUX_SOCKET` | unset | Private tmux socket name (`tmux -L`) |
| `MUXA_TMUX_BIN` | `tmux` | tmux binary |

## Trust

Every pane on the tmux server is fully trusted. Injecting into a pane is
equivalent to typing at that agent's prompt. Do not use muxa on a shared
tmux socket.
