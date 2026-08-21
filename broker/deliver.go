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
	for enterTry := 0; ; enterTry++ {
		switch d.confirm(m) {
		case confirmLanded:
			d.notePaste(m)
			log.Printf("delivered %s → %s id=%s", m.From, m.To, m.ID)
			_ = d.Q.MarkDone(m)
			return
		case confirmUnknown:
			d.notePaste(m)
			log.Printf("unknown %s → %s id=%s (paste accepted but payload not visible; will not retry)", m.From, m.To, m.ID)
			_ = d.Q.MarkUnknown(m)
			return
		case confirmNeedsEnter:
			if enterTry >= maxEnterRetries {
				log.Printf("unconfirmed %s → %s id=%s (collapsed paste still idle after enter retries; left queued)", m.From, m.To, m.ID)
				return
			}
			if err := d.T.SubmitEnter(m.Pane); err != nil {
				log.Printf("enter %s: %v", m.ID, err)
				return
			}
			continue
		default:
			log.Printf("unconfirmed %s → %s id=%s (pane still free; left queued)", m.From, m.To, m.ID)
			return
		}
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
	confirmMissed     confirmResult = iota // still free, payload absent — safe to retry
	confirmLanded                          // payload or paste-collapse visible
	confirmUnknown                         // pane reacted; must not retry
	confirmNeedsEnter                      // collapsed paste visible but composer still idle
)

const maxEnterRetries = 3

func (d *Deliverer) confirm(m *Msg) confirmResult {
	needle := confirmNeedle(m.Text)
	var last string
	var lastY, lastX int
	sawCollapsed := false
	for i := 0; i < 5; i++ {
		snap, err := d.T.Snapshot(m.Pane)
		if err == nil {
			last = snap.Capture
			lastY, lastX = snap.CursorY, snap.CursorX
			if needle != "" && landed(last, needle) {
				return confirmLanded
			}
			if pasteCollapsed(last) {
				sawCollapsed = true
			}
		}
		if i < 4 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if sawCollapsed {
		return d.collapsedPasteConfirm(m.Pane, last, lastY, lastX)
	}
	if hist, err := d.T.CaptureHistory(m.Pane); err == nil {
		if needle != "" && landed(hist, needle) {
			return confirmLanded
		}
		if pasteCollapsed(hist) {
			return d.collapsedPasteConfirm(m.Pane, hist, lastY, lastX)
		}
	}
	// unknown-no-retry: the pane reacted (cursor row no longer empty/prompt)
	// or started drawing, but the payload is not visible.
	// pending-safe-retry is the remaining case.
	if last != "" && !emptyAtCursor(last, lastY, lastX) {
		return confirmUnknown
	}
	if d.drawing(m.Pane) {
		return confirmUnknown
	}
	return confirmMissed
}

func confirmNeedle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "[muxa]") || strings.HasPrefix(s, "Reply:") || s == "Do not reply." {
			continue
		}
		return s
	}
	return strings.TrimSpace(text)
}

func landed(capture, needle string) bool {
	if needle == "" {
		return false
	}
	if strings.Contains(capture, needle) {
		return true
	}
	if len(needle) > 24 && strings.Contains(capture, needle[:24]) {
		return true
	}
	return false
}

func pasteCollapsed(capture string) bool {
	s := stripANSI(capture)
	return strings.Contains(s, "[Pasted text") || strings.Contains(s, "Pasted text +")
}

// collapsedPasteConfirm treats Cursor's paste placeholder as delivered only when
// the pane has reacted. A stable collapsed placeholder on an empty cursor row
// means the paste landed but Enter did not submit (muxa#79).
func (d *Deliverer) collapsedPasteConfirm(pane, capture string, cursorY, cursorX int) confirmResult {
	if d.drawing(pane) {
		return confirmLanded
	}
	if !pasteCollapsed(capture) || !emptyAtCursor(capture, cursorY, cursorX) {
		return confirmLanded
	}
	snap2, err := d.T.Snapshot(pane)
	if err != nil {
		return confirmLanded
	}
	if snap2.Capture == capture &&
		emptyAtCursor(snap2.Capture, snap2.CursorY, snap2.CursorX) {
		return confirmNeedsEnter
	}
	return confirmLanded
}

func (d *Deliverer) pasteIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.pastes))
	copy(out, d.pastes)
	return out
}
