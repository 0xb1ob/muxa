package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner runs a tmux argv (without the binary name).
type Runner func(args []string, stdin []byte) (stdout string, err error)

type TMUX struct {
	Bin    string
	Socket string
	Run    Runner
	Delay  time.Duration
}

func NewTMUX() *TMUX {
	bin := os.Getenv("MUXA_TMUX_BIN")
	if bin == "" {
		bin = "tmux"
	}
	delay := 150 * time.Millisecond
	if v := os.Getenv("MUXA_ENTER_DELAY"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil {
			delay = d
		}
	}
	t := &TMUX{Bin: bin, Socket: os.Getenv("MUXA_TMUX_SOCKET"), Delay: delay}
	t.Run = t.exec
	return t
}

func (t *TMUX) args(rest ...string) []string {
	if t.Socket != "" {
		return append([]string{"-L", t.Socket}, rest...)
	}
	return rest
}

func (t *TMUX) exec(args []string, stdin []byte) (string, error) {
	cmd := exec.Command(t.Bin, t.args(args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out bytes.Buffer
	var errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		if errb.Len() > 0 {
			return out.String(), fmt.Errorf("%s: %s", err, strings.TrimSpace(errb.String()))
		}
		return out.String(), err
	}
	return out.String(), nil
}

func (t *TMUX) fmt(pane, format string) (string, error) {
	out, err := t.Run([]string{"display-message", "-t", pane, "-p", format}, nil)
	return strings.TrimSpace(out), err
}

func (t *TMUX) PaneDead(pane string) bool {
	v, err := t.fmt(pane, "#{pane_dead}")
	return err != nil || v == "1"
}

func (t *TMUX) InMode(pane string) bool {
	v, err := t.fmt(pane, "#{pane_in_mode}")
	return err != nil || v == "1"
}

func (t *TMUX) Capture(pane string) (string, error) {
	return t.Run([]string{"capture-pane", "-p", "-t", pane}, nil)
}

func (t *TMUX) Inject(pane, text string) error {
	buf := fmt.Sprintf("muxa-broker-%d", time.Now().UnixNano())
	if _, err := t.Run([]string{"load-buffer", "-b", buf, "-"}, []byte(text)); err != nil {
		return fmt.Errorf("load-buffer: %w", err)
	}
	if _, err := t.Run([]string{"paste-buffer", "-p", "-d", "-b", buf, "-t", pane}, nil); err != nil {
		_, _ = t.Run([]string{"delete-buffer", "-b", buf}, nil)
		return fmt.Errorf("paste-buffer: %w", err)
	}
	time.Sleep(t.Delay)
	if _, err := t.Run([]string{"send-keys", "-t", pane, "Enter"}, nil); err != nil {
		return fmt.Errorf("send-keys: %w", err)
	}
	if t.PaneDead(pane) {
		return fmt.Errorf("pane %s died during inject", pane)
	}
	return nil
}

// Free reports whether the pane may receive a paste right now.
func (t *TMUX) Free(pane string) (bool, error) {
	if t.PaneDead(pane) {
		return false, nil
	}
	if t.InMode(pane) {
		return false, nil
	}
	cap, err := t.Capture(pane)
	if err != nil {
		return false, err
	}
	return LooksFree(cap), nil
}
