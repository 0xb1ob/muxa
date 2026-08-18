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
| `@muxa_name`    | unique alias (`coder`, `worker-a`, …)        |
| `@muxa_id`      | stable 12-hex id for this pane's registration |
| `@muxa_parent`  | parent alias, empty if this pane is a root   |
| `@muxa_kind`    | `claude` \| `cursor` \| `pi` \| `generic`    |
| `@muxa_state`   | `idle` \| `busy` \| `blocked`                |
| `@muxa_deliver` | `hook` \| `inject`                           |

Roster is `tmux list-panes -a`. There is no registry file.

Names are unique per tmux server. Duplicate register fails. Ids are unique
even when two panes run the same CLI on the same project.

A parent creates children with `muxa spawn --name NAME -- command…`, which
opens a tmux window (or `--split` pane), sets `MUXA_PARENT`, and records
`@muxa_parent` before the child CLI boots.

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
$XDG_RUNTIME_DIR/muxa/<tmux-pid>/mail/<name>/{tmp,new,cur}
```

On macOS, `$XDG_RUNTIME_DIR` falls back to `/tmp/muxa-<uid>`.

One file per message. Writers create a unique name in `tmp/` and `mv` it
to `new/`. Readers `mv` `new/` → `cur/` to claim. This is maildir.

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
limit in the spec; inject delivery SHOULD refuse bodies over 8 KiB and
leave them in the mailbox for `muxa peek` / hook drain (hooks tolerate
more than a TUI paste).

## Delivery

```
send(name, body):
  resolve name -> pane
  write maildir new/
  if pane.state in (busy, blocked) and pane.deliver == hook:
      return queued          # Stop/session_stop/followup will claim
  if pane.state in (busy, blocked) and pane.deliver == inject:
      spawn waiter           # inject when state becomes idle
      return queued
  claim + inject             # idle at prompt
```

```
hook stop:
  claim all new mail for this pane's name
  if empty: set state=idle; print nothing
  else: set state=busy; print native continue payload
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
muxa whoami
muxa id
muxa parent
muxa children
muxa spawn --name worker -- command…
```

Exit 0 on queued or delivered. Exit 2 if the name is unknown. Exit 3 if
not running under tmux when tmux is required. Exit 4 if the send is
forbidden by reachability.

## Etiquette (normative for agents)

Silence is the default. Reply only with a question, a result, or a
blocker. Never ack. Stop after two back-and-forths unless the thread is
still producing decisions. Use `--no-reply` for status dumps.

## Trust

Every pane on the tmux server is fully trusted. Injecting into a pane is
equivalent to typing at that agent's prompt. Do not use muxa on a shared
tmux socket.
