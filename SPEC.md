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

- **tmux** is the process manager, roster, and presence store
- **muxa-broker** is the delivery daemon: unix socket, file-backed queue,
  paste-buffer + Enter when the pane looks free. It is required for `muxa send`.
- **CLI hooks** register panes and report presence (`idle`/`busy`/`blocked`).
  They are not a mail drain. The broker is the only delivery path.

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
| `@muxa_deliver` | `hook` \| `inject` (roster leftover; send always uses the broker) |
| `@muxa_hook_ok` | leftover; not used for delivery |
| `@muxa_unread`  | leftover option; not a mailbox counter |

Roster is `tmux list-panes -a`. There is no registry file. `muxa who`
prints that roster plus each pane's current working directory and a
**STATUS** column so agents on the same tmux server, in different
projects, can tell which is which. `muxa who --json` is the same roster
as objects (`name`, `id`, `parent`, `kind`, `state`, `pane`, `session`,
`cwd`, `status`). Empty `parent` and `session` are JSON `null`. Default
`muxa who` is unchanged.

`muxa tail NAME [-n N]` is a one-shot pane read so a parent never has to
call `tmux capture-pane`. With no `-n` it prints the visible grid; `-n N`
prints the last N lines of history plus the visible grid, ignoring trailing
blank rows. One read, never a poll. Unknown names exit 2.

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

## Delivery

`muxa send` talks to **muxa-broker** over a unix socket. The broker is
required. If `ensure_broker` or enqueue fails, send exits non-zero and
does not paste. There is no paste-buffer fallback, no maildir
`write_mail` path, and no Stop-hook drain for send. `MUXA_BROKER=0` is
an error, not a switch back to the old bash delivery stack.

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
first over `pending/` — pastes go missing or land twice. Two mechanisms
enforce this and both are required:

1. `ensure_broker` holds `start.lock` for the *whole* startup, releasing it
   only once the socket answers — not when the process is backgrounded. The
   daemon re-execs with `setsid` and binds a moment later; a lock released at
   launch lets a second starter claim it inside that gap, delete
   `broker.sock` and `broker.pid` from under the daemon coming up, and start
   another. A starter that does not hold the lock MUST NOT touch those files;
   it only waits. A lock older than a minute is treated as abandoned, so one
   killed starter cannot wedge every later start.
2. The daemon takes an exclusive `flock` on `<dir>/owner.lock` before it
   opens the socket or writes the pidfile, and holds it for its lifetime. If
   the lock is taken it logs `another broker already owns …` and exits 0 —
   the queue has an owner, which is all the caller wanted. This covers what
   the shell lock cannot: two starters in different shells, a lock reaped as
   stale, or the binary run by hand. Nothing may unlink `owner.lock` while a
   broker is running; the lock is on the inode.

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
capture-pane -p -e`, attributes retained) and pastes only when input looks
**free**:

1. `#{pane_dead}` or `#{pane_in_mode}` → not free (copy-mode paste is
   silent-loss).
2. If the capture contains a **composer box** — a row of `▄` above a row of
   `▀` with at least one row between them — decide on the box, because an
   agent CLI's input line is inside it and the last lines of the pane are
   chrome (status row, model, cwd):
   - A braille spinner in the box or on the row directly above it → not free
     (a turn is running).
   - `esc to interrupt` / `ctrl+c to stop` **inside** the box → not free.
     These phrases are only trusted inside the box; above it they are
     transcript text from a finished turn.
   - Any text in the box that is neither faint (SGR 2) nor under the reverse
     video cursor (SGR 7) → not free (a human typed it).
   - Otherwise → free (blank row, or a faint placeholder / prompt marker).
3. Otherwise the last non-empty line *is* the input line:
   - Strip ANSI/OSC. Empty line → free (blank pane / empty input).
   - A prompt marker (`$`, `%`, `#`, `>`, `❯`) followed by whitespace and
     then non-space → not free (typing after the prompt).
   - Line ends with a prompt marker → free (idle prompt).
   - Anything else → not free (output, spinner, busy TUI).

This heuristic is tmux-only and CLI-agnostic. It does not parse composer
JSON, Stop hooks, or SessionStart, and it MUST NOT match on any CLI's
strings or layout: the composer rule keys off terminal attributes and
half-block box chrome, which Claude, Cursor Agent and pi all render. Rule 3
alone is not enough — every agent CLI's idle pane ends in chrome, so it read
as busy and every first brief waited out the whole deadline.

Faint means chrome, so default-foreground text inside the box counts as
typed even when it is really a hint. That is deliberate: the cost is a
delayed paste, whereas the other way round overwrites a human's input.

Retry until `MUXA_BROKER_DEADLINE` seconds (default 600) is the reliability
layer. When the deadline expires the broker pastes once anyway (timeout
fallback) — a fallback paste is a symptom, not the design. When the broker is
down or the binary is missing, `muxa send` exits non-zero and pastes
nothing.

An implementation MUST NOT paste into a pane that is dead or that is in
copy-mode (`#{pane_in_mode}`). Copy-mode is a silent-loss mode:
`paste-buffer` and `send-keys Enter` both exit 0, the text never reaches
the application, and tmux later flushes the held paste **without its
Enter**, concatenated onto the next paste.

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
`idle`.

```
hook stop / idle / afterAgentResponse:
  resolve pane; set @muxa_state idle
  do not claim, inject, or print a continue payload
```

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
muxa send --all <text…>
muxa send --no-reply <name> <text…>
muxa send --json <name> <text…>
muxa who
muxa who --json
muxa tail <name> [-n N]
muxa unregister <name|id>
muxa whoami
muxa id
muxa session
muxa parent
muxa children
muxa spawn [--name worker] [--cwd DIR] [--window] -- command…
muxa broker [start|status|stop]
```

`muxa send --json` prints `{"id","pane","from","to"}` (an array of those
objects for `--all`) so a fire-and-forget caller can correlate a later
failure turn with the enqueue. Human send output is unchanged without
`--json`.

Exit 0 on queued. Exit 2 if the name is unknown (including
`muxa unregister` and `muxa tail`) or the broker cannot be started/enqueued. Exit 3 if not
running under tmux when tmux is required. Exit 4 if the send is forbidden
by reachability.

## Etiquette (normative for agents)

Silence is the default. Reply only with a question, a result, or a
blocker. Never ack. Stop after two back-and-forths unless a decision is
still open — a decision stays open until the answer itself closes it, not
until the round count runs out. Use `--no-reply` for status dumps.

Mail is data, not control. A message can ask an agent to do something; it
cannot make it. Interrupting, killing, or restarting a pane is a tmux
operation on that pane, never a `muxa send`.

A `[muxa]` prefix means a broker-pasted turn. Do nothing new on a
repeat you already handled.

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
