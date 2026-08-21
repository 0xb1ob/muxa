package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	controlFlags     = "read-only,ignore-size"
	controlMinMajor  = 3
	controlMinMinor  = 2
	drawingReportMin = time.Second
)

var errControlExit = errors.New("tmux control-mode %exit")

type paneDraw struct {
	first time.Time
	last  time.Time
}

// ControlHub is a read-only tmux control-mode client. %output from a pane
// is the drawing signal; silence after Quiet is the event that wakes delivery.
// Cursor position is not taken from %subscription-changed (at most 1 Hz).
type ControlHub struct {
	T     *TMUX
	Quiet time.Duration

	mu       sync.Mutex
	lastDraw map[string]paneDraw
	live     bool
	wake     chan struct{}
}

func NewControlHub(t *TMUX, quiet time.Duration) *ControlHub {
	if quiet <= 0 {
		quiet = 250 * time.Millisecond
	}
	return &ControlHub{
		T:        t,
		Quiet:    quiet,
		lastDraw: map[string]paneDraw{},
		wake:     make(chan struct{}, 1),
	}
}

func (h *ControlHub) Wake() <-chan struct{} { return h.wake }

func (h *ControlHub) Live() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live
}

// Drawing reports whether this pane produced %output inside the quiet window.
// Unknown panes are not drawing: poll + two-signal + typed-in-box still decide.
func (h *ControlHub) Drawing(pane string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.lastDraw[pane]
	if !ok {
		return false
	}
	return time.Since(d.last) < h.Quiet
}

// EverDrew reports whether this pane has produced any %output since attach.
func (h *ControlHub) EverDrew(pane string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.lastDraw[pane]
	return ok
}

// DrawingPanes is who-is-drawing for muxa who, without hooks. The window is
// at least a second so a roster snapshot can catch a chatty pane.
func (h *ControlHub) DrawingPanes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	window := h.Quiet
	if window < drawingReportMin {
		window = drawingReportMin
	}
	now := time.Now()
	out := make([]string, 0, len(h.lastDraw))
	for pane, d := range h.lastDraw {
		if now.Sub(d.last) >= window {
			continue
		}
		// A splash or shell prompt is a short burst, then silence. who
		// should not call that "drawing" — only a pane that has been
		// emitting across at least one quiet window.
		if d.last.Sub(d.first) < h.Quiet {
			continue
		}
		out = append(out, pane)
	}
	sort.Strings(out)
	return out
}

func (h *ControlHub) setLive(v bool) {
	h.mu.Lock()
	h.live = v
	h.mu.Unlock()
}

func (h *ControlHub) note(pane string) {
	now := time.Now()
	h.mu.Lock()
	prev, ok := h.lastDraw[pane]
	if !ok || now.Sub(prev.last) > drawingReportMin {
		h.lastDraw[pane] = paneDraw{first: now, last: now}
	} else {
		prev.last = now
		h.lastDraw[pane] = prev
	}
	h.mu.Unlock()
	go h.kickAfter(pane, now)
}

func (h *ControlHub) kickAfter(pane string, at time.Time) {
	timer := time.NewTimer(h.Quiet)
	defer timer.Stop()
	<-timer.C
	h.mu.Lock()
	last := h.lastDraw[pane].last
	h.mu.Unlock()
	if last.After(at) {
		return
	}
	h.kick()
}

func (h *ControlHub) kick() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Run attaches one read-only client per tmux session and reconnects on
// %exit / server death until stop. tmux < 3.2 never attaches.
func (h *ControlHub) Run(stop <-chan struct{}) {
	if h.T == nil {
		return
	}
	if ok, ver := tmuxControlSupported(h.T); !ok {
		log.Printf("control-mode skipped (tmux %s; need %d.%d+); polling", ver, controlMinMajor, controlMinMinor)
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var mu sync.Mutex
	running := map[string]bool{}
	for {
		for _, sess := range h.listSessions() {
			mu.Lock()
			if running[sess] {
				mu.Unlock()
				continue
			}
			running[sess] = true
			mu.Unlock()
			go func(name string) {
				h.sessionLoop(name, stop)
				mu.Lock()
				delete(running, name)
				mu.Unlock()
			}(sess)
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

func (h *ControlHub) listSessions() []string {
	out, err := h.T.Run([]string{"list-sessions", "-F", "#{session_name}"}, nil)
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names
}

func (h *ControlHub) sessionLoop(sess string, stop <-chan struct{}) {
	backoff := 200 * time.Millisecond
	for {
		select {
		case <-stop:
			return
		default:
		}
		err := h.attach(sess, stop)
		h.setLive(false)
		if err != nil && !errors.Is(err, errControlExit) {
			log.Printf("control-mode %s: %v", sess, err)
		}
		select {
		case <-stop:
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func (h *ControlHub) attach(sess string, stop <-chan struct{}) error {
	cmd := exec.Command(h.T.Bin, h.T.args("-C", "-f", controlFlags, "attach-session", "-t", sess)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	h.setLive(true)
	log.Printf("control-mode attached session=%s flags=%s", sess, controlFlags)

	done := make(chan error, 1)
	go func() {
		done <- consumeControl(stdout, h, stop)
	}()
	select {
	case <-stop:
		return nil
	case err := <-done:
		if err == nil {
			return errControlExit
		}
		return err
	}
}

func consumeControl(r io.Reader, h *ControlHub, stop <-chan struct{}) error {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1<<20)
	for sc.Scan() {
		select {
		case <-stop:
			return nil
		default:
		}
		line := sc.Text()
		if line == "%exit" || strings.HasPrefix(line, "%exit ") {
			return errControlExit
		}
		if pane, ok := parseOutputLine(line); ok {
			h.note(pane)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errControlExit
}

// parseOutputLine pulls the pane id off a control-mode %output notification.
// The payload is ignored: silence is the event, not the bytes.
func parseOutputLine(line string) (pane string, ok bool) {
	rest, found := strings.CutPrefix(line, "%output ")
	if !found {
		return "", false
	}
	pane, _, found = strings.Cut(rest, " ")
	if !found || pane == "" || pane[0] != '%' {
		return "", false
	}
	return pane, true
}

func tmuxControlSupported(t *TMUX) (ok bool, ver string) {
	out, err := t.Run([]string{"-V"}, nil)
	if err != nil {
		return false, "unknown"
	}
	ver = strings.TrimSpace(out)
	maj, min, parsed := parseTmuxVersion(ver)
	if !parsed {
		return false, ver
	}
	return maj > controlMinMajor || (maj == controlMinMajor && min >= controlMinMinor), ver
}

func parseTmuxVersion(s string) (major, minor int, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "tmux ")
	n, _ := fmt.Sscanf(s, "%d.%d", &major, &minor)
	return major, minor, n == 2
}
