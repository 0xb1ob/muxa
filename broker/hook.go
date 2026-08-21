package main

import (
	"io"
	"os"
)

func cmdHook(args []string) error {
	event := ""
	if len(args) > 0 {
		event, args = args[0], args[1:]
	}
	kind := ""
	for len(args) > 0 {
		switch args[0] {
		case "--kind":
			if len(args) >= 2 {
				kind, args = args[1], args[2:]
			} else {
				args = args[1:]
			}
		case "--format":
			if len(args) >= 2 {
				args = args[2:]
			} else {
				args = args[1:]
			}
		default:
			args = args[1:]
		}
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	switch event {
	case "session-start", "SessionStart", "sessionStart":
		event = "session-start"
	case "":
		return die("hook: event required")
	default:
		os.Stderr.WriteString("muxa: hook: ignoring unknown event " + event + " (only session-start exists; remove the stale hook registration)\n")
		return nil
	}
	if os.Getenv("TMUX") == "" && os.Getenv("MUXA_TMUX_SOCKET") == "" {
		return nil
	}
	t := NewTMUX()
	pane, err := thisPane(t)
	if err != nil {
		return nil
	}
	kind = resolveHookKind(t, pane, kind)
	if paneIsRegistered(t, pane) {
		existing, _ := t.fmt(pane, "#{@muxa_kind}")
		if kind != "" && kind != existing {
			_ = t.SetPaneOpt(pane, "@muxa_kind", kind)
		}
		return nil
	}
	return hookRegisterPane(t, pane, kind)
}

func hookRegisterPane(t *TMUX, pane, kind string) error {
	saved := os.Getenv("TMUX_PANE")
	_ = os.Setenv("TMUX_PANE", pane)
	defer func() {
		if saved != "" {
			_ = os.Setenv("TMUX_PANE", saved)
		} else {
			_ = os.Unsetenv("TMUX_PANE")
		}
	}()
	args := []string{"--kind", kind}
	if n := os.Getenv("MUXA_NAME"); n != "" {
		args = append([]string{"--name", n}, args...)
	}
	if p := os.Getenv("MUXA_PARENT"); p != "" {
		args = append(args, "--parent", p)
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return cmdRegister(args)
	}
	old := os.Stdout
	os.Stdout = devnull
	err = cmdRegister(args)
	os.Stdout = old
	_ = devnull.Close()
	return err
}
