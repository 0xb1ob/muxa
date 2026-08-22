package main

import (
	"log"
	"strings"
	"sync"
	"time"
)

type Deliverer struct {
	Q        *Queue
	T        *TMUX
	Ctrl     *ControlHub
	Poll     time.Duration
	now      func() time.Time
	mu       sync.Mutex
	inflight map[string]bool   // pane currently being injected
	pastes   []string          // test hook: pane ids pasted, in order
	prev     map[string]string // last capture-pane per pane, for two-signal
	held     map[string]bool   // already logged "holding past deadline"
	drew     map[string]bool   // pane has shown visible content (dispatch ready)
}

func NewDeliverer(q *Queue, t *TMUX, poll time.Duration) *Deliverer {
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	return &Deliverer{
		Q:        q,
		T:        t,
		Poll:     poll,
		now:      time.Now,
		inflight: map[string]bool{},
		prev:     map[string]string{},
		held:     map[string]bool{},
		drew:     map[string]bool{},
	}
}

func (d *Deliverer) Loop(stop <-chan struct{}) {
	if d.Ctrl != nil {
		go d.Ctrl.Run(stop)
	}
	ticker := time.NewTicker(d.Poll)
	defer ticker.Stop()
	d.Tick()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			d.Tick()
		case <-d.wake():
			d.Tick()
		}
	}
}

func (d *Deliverer) wake() <-chan struct{} {
	if d.Ctrl == nil {
		return nil
	}
	return d.Ctrl.Wake()
}

// Tick tries to deliver every pending message at most once per pane.
// A pane that is not free is left queued — including past its deadline.
// Timeout-fallback paste is how two messages into one busy composer
// clobber each other and still get filed as done.
func (d *Deliverer) Tick() {
	msgs, err := d.Q.Pending()
	if err != nil {
		log.Printf("queue: %v", err)
		return
	}
	now := d.now().Unix()
	seen := map[string]bool{}
	for _, m := range msgs {
		if seen[m.Pane] || d.busy(m.Pane) {
			continue
		}
		seen[m.Pane] = true
		d.deliverOne(m, now)
	}
}

func (d *Deliverer) busy(pane string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inflight[pane]
}

func (d *Deliverer) deliverOne(m *Msg, now int64) {
	if d.T.PaneDead(m.Pane) {
		if now >= m.DeadlineUnix {
			log.Printf("drop %s: pane %s dead after deadline", m.ID, m.Pane)
			_ = d.Q.MarkFailed(m)
		}
		return
	}
	free, err := d.canPaste(m.Pane)
	if err != nil {
		log.Printf("free %s: %v", m.Pane, err)
		free = false
	}
	if m.isDispatch() && !d.sawDraw(m.Pane) {
		// Cold CLI: empty capture is vacuously box-free, but nothing has painted yet.
		free = false
	}
	if !free {
		if now >= m.DeadlineUnix {
			if m.isDispatch() {
				d.failDispatch(m)
			} else {
				d.noteHeld(m)
			}
		}
		return
	}
	d.mu.Lock()
	d.inflight[m.Pane] = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.inflight, m.Pane)
		d.mu.Unlock()
	}()

	m.Attempts++
	_ = d.Q.Save(m)
	if err := d.T.Inject(m.Pane, m.Text); err != nil {
		log.Printf("inject %s: %v", m.ID, err)
		return
	}
	switch d.confirm(m.Pane) {
	case confirmPasted:
		d.notePaste(m)
		log.Printf("delivered %s → %s id=%s", m.From, m.To, m.ID)
		_ = d.Q.MarkDone(m)
	default:
		// Cursor Agent parks the hardware cursor on a blank footer row, so
		// confirmMissed is a false negative after a first brief already
		// submitted. Leaving Kind=dispatch queued would re-paste on the
		// next Tick. Later mail (muxa send) still retries a genuine ghost.
		if m.isDispatch() {
			d.notePaste(m)
			log.Printf("unknown %s → %s id=%s (paste accepted but payload not visible; will not retry)", m.From, m.To, m.ID)
			_ = d.Q.MarkDone(m)
			return
		}
		log.Printf("unconfirmed %s → %s id=%s (pane still free; left queued)", m.From, m.To, m.ID)
	}
}

func (d *Deliverer) notePaste(m *Msg) {
	d.mu.Lock()
	d.pastes = append(d.pastes, m.Pane+"|"+m.ID)
	delete(d.held, m.ID)
	d.mu.Unlock()
}

func (d *Deliverer) noteHeld(m *Msg) {
	d.mu.Lock()
	if d.held[m.ID] {
		d.mu.Unlock()
		return
	}
	d.held[m.ID] = true
	d.mu.Unlock()
	log.Printf("holding %s → %s id=%s (pane not free after deadline; left queued)", m.From, m.To, m.ID)
}

func (d *Deliverer) failDispatch(m *Msg) {
	log.Printf("dispatch failed %s → %s id=%s (never ready; left unbriefed)", m.From, m.To, m.ID)
	_ = d.Q.MarkFailed(m)
	if m.ParentPane == "" {
		return
	}
	fail := &Msg{
		Pane:         m.ParentPane,
		From:         "broker",
		To:           m.From,
		Text:         dispatchFailText(m),
		DeadlineUnix: d.now().Add(24 * time.Hour).Unix(),
	}
	if err := d.Q.Put(fail); err != nil {
		log.Printf("dispatch fail notify %s: %v", m.ID, err)
	}
}

func dispatchFailText(m *Msg) string {
	return "[muxa] from=broker\n" +
		"dispatch failed: " + m.To + " pane=" + m.Pane + " never became ready before deadline\n" +
		"id=" + m.ID + "\n" +
		"Do not reply.\n"
}

func (d *Deliverer) sawDraw(pane string) bool {
	if d.Ctrl != nil && d.Ctrl.EverDrew(pane) {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.drew[pane]
}

// canPaste is the single paste gate for both the poll loop and control-mode
// silence. Free-detection is the broker's: drawing (%output inside the
// quiet window) waits, and so does a pane two-signal rejects.
func (d *Deliverer) canPaste(pane string) (bool, error) {
	if d.T.PaneDead(pane) {
		return false, nil
	}
	if d.T.InMode(pane) {
		return false, nil
	}
	if d.drawing(pane) {
		return false, nil
	}
	two, empty, first, err := d.observe(pane)
	if err != nil {
		return false, err
	}
	if first {
		// No frame pair yet. Control-mode silence already passed (not
		// drawing); empty-at-cursor is the remaining two-signal half.
		// Without it a first tick would paste over shell typing.
		return empty, nil
	}
	return two, nil
}

func (d *Deliverer) drawing(pane string) bool {
	return d.Ctrl != nil && d.Ctrl.Live() && d.Ctrl.Drawing(pane)
}

// observe runs free-detection (two-signal).
func (d *Deliverer) observe(pane string) (twoFree, empty, first bool, err error) {
	if d.T.PaneDead(pane) {
		return false, false, false, nil
	}
	if d.T.InMode(pane) {
		return false, false, false, nil
	}
	snap, err := d.T.Snapshot(pane)
	if err != nil {
		return false, false, false, err
	}
	d.mu.Lock()
	prev := d.prev[pane]
	first = prev == ""
	d.prev[pane] = snap.Capture
	if visibleContent(snap.Capture) {
		d.drew[pane] = true
	}
	d.mu.Unlock()
	empty = emptyAtCursor(snap.Capture, snap.CursorY, snap.CursorX)
	return TwoSignalFree(prev, snap.Capture, snap.CursorY, snap.CursorX), empty, first, nil
}

func visibleContent(capture string) bool {
	return strings.TrimSpace(stripANSI(capture)) != ""
}

type confirmResult int

const (
	confirmMissed confirmResult = iota // still free — send may retry; dispatch must not
	confirmPasted                      // pane reacted; must not retry
)

// confirm takes one post-paste snapshot. A pane that reacted (cursor row
// no longer empty/prompt, or control-mode drawing) is pasted and must not
// be retried. A pane that stayed free is confirmMissed: deliverOne files
// a first brief done/ (no retry) and leaves later mail queued. No payload
// string-matching against the capture.
func (d *Deliverer) confirm(pane string) confirmResult {
	snap, err := d.T.Snapshot(pane)
	if err == nil && snap.Capture != "" && !emptyAtCursor(snap.Capture, snap.CursorY, snap.CursorX) {
		return confirmPasted
	}
	if d.drawing(pane) {
		return confirmPasted
	}
	return confirmMissed
}

func (d *Deliverer) pasteIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.pastes))
	copy(out, d.pastes)
	return out
}
