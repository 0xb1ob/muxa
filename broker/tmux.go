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
	delay := enterDelayFromEnv()
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

// PaneDead reports whether a pane cannot be pasted into: it is marked dead
// (`remain-on-exit`), or its id no longer exists on the server at all.
//
// The second case does not look like a failure. tmux runs
// `display-message -t %gone -p '#{pane_dead}'`, finds no pane to resolve
// the format against, expands it to the **empty string**, and exits **0**
// — only commands that need the pane itself (`capture-pane`,
// `paste-buffer`) report `can't find pane`. So an empty answer means gone,
// never alive. Reading it as alive let a closed pane fall through to the
// free-check, where `capture-pane` failed on every poll tick: 22 MB of
// `can't find pane` in eight hours and the message never expired
// (muxa#124). tests/tmux-facts.sh pins the tmux behaviour.
func (t *TMUX) PaneDead(pane string) (dead bool, err error) {
	v, err := t.fmt(pane, "#{pane_dead}")
	if err != nil {
		return false, err
	}
	return v == "" || v == "1", nil
}

func (t *TMUX) InMode(pane string) (inMode bool, err error) {
	v, err := t.fmt(pane, "#{pane_in_mode}")
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

// Capture reads the visible pane. -e keeps SGR attributes so empty-at-cursor
// can decode the cursor row without counting escape bytes as text.
func (t *TMUX) Capture(pane string) (string, error) {
	return t.Run([]string{"capture-pane", "-p", "-e", "-t", pane}, nil)
}

// Snapshot captures the pane and the hardware cursor in adjacent tmux calls.
func (t *TMUX) Snapshot(pane string) (Snapshot, error) {
	cap, err := t.Capture(pane)
	if err != nil {
		return Snapshot{}, err
	}
	pos, err := t.fmt(pane, "#{cursor_y} #{cursor_x}")
	if err != nil {
		return Snapshot{Capture: cap}, err
	}
	var y, x int
	n, _ := fmt.Sscanf(pos, "%d %d", &y, &x)
	if n != 2 {
		return Snapshot{Capture: cap}, fmt.Errorf("cursor format %q", pos)
	}
	return Snapshot{Capture: cap, CursorY: y, CursorX: x}, nil
}

func enterDelayFromEnv() time.Duration {
	const defaultDelay = 400 * time.Millisecond
	v := os.Getenv("MUXA_ENTER_DELAY")
	if v == "" {
		return defaultDelay
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	// Legacy: bare decimal seconds (tests/run.sh uses "0.05").
	if d, err := time.ParseDuration(v + "s"); err == nil {
		return d
	}
	return defaultDelay
}

func (t *TMUX) SubmitEnter(pane string) error {
	time.Sleep(t.Delay)
	if _, err := t.Run([]string{"send-keys", "-t", pane, "Enter"}, nil); err != nil {
		return fmt.Errorf("send-keys: %w", err)
	}
	if dead, _ := t.PaneDead(pane); dead {
		return fmt.Errorf("pane %s died during enter", pane)
	}
	return nil
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
	return t.SubmitEnter(pane)
}

func (t *TMUX) FirstSessionPID() string {
	out, err := t.Run([]string{"list-sessions", "-F", "#{pid}"}, nil)
	if err != nil {
		return "0"
	}
	line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if line == "" {
		return "0"
	}
	return line
}

func (t *TMUX) SetPaneOpt(pane, key, val string) error {
	_, err := t.Run([]string{"set-option", "-p", "-t", pane, key, val}, nil)
	return err
}

func (t *TMUX) UnsetPaneOpt(pane, key string) {
	_, _ = t.Run([]string{"set-option", "-p", "-t", pane, "-u", key}, nil)
}

func (t *TMUX) SetTitle(pane, title string) {
	_, _ = t.Run([]string{"select-pane", "-t", pane, "-T", title}, nil)
}

// SelectPane restores keyboard focus without changing pane options.
func (t *TMUX) SelectPane(pane string) {
	_, _ = t.Run([]string{"select-pane", "-t", pane}, nil)
}

func (t *TMUX) KillPane(pane string) error {
	_, err := t.Run([]string{"kill-pane", "-t", pane}, nil)
	return err
}

func (t *TMUX) CapturePlain(pane string) (string, error) {
	return t.Run([]string{"capture-pane", "-p", "-t", pane}, nil)
}

func (t *TMUX) CaptureHistory(pane string) (string, error) {
	return t.Run([]string{"capture-pane", "-p", "-S", "-", "-t", pane}, nil)
}

func (t *TMUX) NewWindow(sess, name, cwd, cmd string) (string, error) {
	out, err := t.Run([]string{"new-window", "-t", sess, "-n", name, "-c", cwd, "-P", "-F", "#{pane_id}", cmd}, nil)
	return strings.TrimSpace(out), err
}

func (t *TMUX) SplitWindow(flag, target, cwd, cmd string) (string, error) {
	out, err := t.Run([]string{"split-window", flag, "-t", target, "-c", cwd, "-P", "-F", "#{pane_id}", cmd}, nil)
	return strings.TrimSpace(out), err
}

func (t *TMUX) LargestPane(target string) string {
	out, err := t.Run([]string{"list-panes", "-t", target, "-F", "#{pane_id} #{pane_width} #{pane_height}"}, nil)
	if err != nil {
		return ""
	}
	best, ma := "", 0
	for _, line := range strings.Split(out, "\n") {
		fs := strings.Fields(line)
		if len(fs) < 3 {
			continue
		}
		var w, h int
		fmt.Sscanf(fs[1], "%d", &w)
		fmt.Sscanf(fs[2], "%d", &h)
		a := w * h
		if a > ma || (a == ma && fs[0] > best) {
			ma = a
			best = fs[0]
		}
	}
	return best
}

func (t *TMUX) SelectLayout(winid, layout string) {
	_, _ = t.Run([]string{"select-layout", "-t", winid, layout}, nil)
}

func (t *TMUX) SetWindowOption(target, key, val string) {
	_, _ = t.Run([]string{"set-window-option", "-t", target, key, val}, nil)
}
