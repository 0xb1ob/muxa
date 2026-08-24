package main

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

const version = "0.3.0"

type cliErr struct {
	code int
	msg  string
}

func (e cliErr) Error() string { return e.msg }

func die(msg string) error { return cliErr{code: 2, msg: msg} }
func dief(f string, a ...any) error {
	return cliErr{code: 2, msg: fmt.Sprintf(f, a...)}
}
func forbid(msg string) error { return cliErr{code: 4, msg: msg} }

func runMuxa(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return 0
	}
	cmd, rest := args[0], args[1:]
	err := dispatchMuxa(cmd, rest)
	if err == nil {
		return 0
	}
	if e, ok := err.(cliErr); ok {
		if e.msg != "" {
			fmt.Fprintf(os.Stderr, "muxa: %s\n", e.msg)
		}
		return e.code
	}
	fmt.Fprintf(os.Stderr, "muxa: %s\n", err)
	return 1
}

func dispatchMuxa(cmd string, args []string) error {
	switch cmd {
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return nil
	case "version", "-v", "--version":
		fmt.Println(version)
		return nil
	case "register":
		return cmdRegister(args)
	case "spawn":
		return cmdSpawn(args)
	case "dispatch":
		return cmdDispatch(args)
	case "who":
		return cmdWho(args)
	case "tail":
		return cmdTail(args)
	case "kill":
		return cmdKill(args)
	case "whoami":
		return cmdWhoami()
	case "parent":
		return cmdParent()
	case "send":
		return cmdSend(args)
	case "broker":
		return cmdBroker(args)
	case "hook":
		return cmdHook(args)
	default:
		printUsage(os.Stderr)
		return cliErr{code: 1, msg: ""}
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `muxa — message other AI CLIs in this tmux server

  muxa register [--name NAME] [--id ID] [--parent NAME] [--kind claude|cursor|pi|generic]
  muxa spawn [--name NAME] [--kind KIND] [--cwd DIR] [--window] -- COMMAND...
                                spawn flags must precede -- or the command word
                                omit --name to get a unique adjective-noun alias (swift-oak)
                                default: split into a tiled grid in this window (--window for a new window)
                                child cwd: --cwd, else process PWD, else this pane's path
                                warns on stderr if a live registered worker already has that cwd
  muxa dispatch [--name NAME] [--cwd DIR] [--brief-file F] -- COMMAND...
                                spawn + first brief (stdin or --brief-file, never a positional string)
                                stdout: {"name","id","pane","cwd","state":"dispatched","from","to"}
                                broker waits until the CLI has drawn, gone quiet, and looks free
                                if it never becomes ready: [muxa] from=broker in the parent, child unbriefed
                                if paste is accepted but the pane still looks free: same, filed done, never retried
  muxa who [--json]                 roster (tmux pane options, cwd, STATE: idle|busy|ghost)
  muxa whoami                       this pane's alias
  muxa kill NAME|ID                 remove the pane (gone from muxa who)
  muxa parent                       this pane's parent alias (empty if root)
  muxa tail NAME [-n N]             one-shot pane capture (visible; -n last N lines of history)
  muxa send [--json] [--no-reply] [--file F] NAME [TEXT]
                                enqueue on the pane-mail broker (parent↔child only)
                                body from argv, --file, or stdin (not both --file and argv)
  muxa broker [start|status|stop]   user-level paste broker (unix socket + file queue)
  muxa hook session-start [--kind KIND]
                                optional root self-registration; spawned panes are already registered
  muxa version

Reachability: a parent may message its children; a child may message its
parent. Children cannot message each other. Roots (no parent) cannot
message other roots.

Agents send with Bash. Incoming mail arrives as a user turn.
Do not poll an inbox. Do not add MCP tools.
`)
}

func needTmux() error {
	if os.Getenv("TMUX") == "" && os.Getenv("MUXA_TMUX_SOCKET") == "" {
		fmt.Fprintln(os.Stderr, "muxa: not in tmux (hard requirement)")
		return cliErr{code: 3, msg: ""}
	}
	return nil
}

func thisPane(t *TMUX) (string, error) {
	if p := os.Getenv("TMUX_PANE"); p != "" {
		return p, nil
	}
	out, err := t.Run([]string{"display-message", "-p", "#{pane_id}"}, nil)
	out = strings.TrimSpace(out)
	if err != nil || out == "" {
		return "", die("cannot detect pane; set TMUX_PANE")
	}
	return out, nil
}

func runtimeRoot(t *TMUX) string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		uid := os.Getuid()
		if u, err := user.Current(); err == nil && u.Uid != "" {
			base = "/tmp/muxa-" + u.Uid
		} else {
			base = fmt.Sprintf("/tmp/muxa-%d", uid)
		}
	}
	pid := t.FirstSessionPID()
	return filepath.Join(base, "muxa", pid)
}

type brokerPaths struct {
	Dir, Sock, PID string
}

func setupBrokerPaths(t *TMUX) brokerPaths {
	rt := runtimeRoot(t)
	dir := env("MUXA_BROKER_DIR", filepath.Join(rt, "broker"))
	return brokerPaths{
		Dir:  dir,
		Sock: env("MUXA_BROKER_SOCK", filepath.Join(dir, "broker.sock")),
		PID:  env("MUXA_BROKER_PID", filepath.Join(dir, "broker.pid")),
	}
}
