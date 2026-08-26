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
				if d.composerBlocked(m.Pane) {
					d.failDispatchComposerForeign(m)
				} else {
					d.failDispatch(m)
				}
			} else {
				d.noteHeld(m)
			}
		}
		return
	}
	pre, err := d.T.Snapshot(m.Pane)
	if err != nil {
		log.Printf("snapshot %s: %v", m.Pane, err)
		return
	}
	if composerInputForeign(pre.Capture) {
		if now >= m.DeadlineUnix && !m.isDispatch() {
			d.noteHeld(m)
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
	switch d.confirm(m.Pane, m) {
	case confirmPasted:
		if !m.isDispatch() && !d.mailLanded(m) {
			// The envelope marker is the muxa#116 landing proof. A pane
			// that consumes paste without echoing it cannot produce that
			// proof mid-turn, and its absence must not mean retry
			// (muxa#127).
			if d.paneConsumesPaste(m.Pane) {
				d.fileConsumed(m)
				return
			}
			log.Printf("unconfirmed %s → %s id=%s (pane reacted but mail did not land; left queued)", m.From, m.To, m.ID)
			return
		}
		d.notePaste(m)
		log.Printf("delivered %s → %s id=%s", m.From, m.To, m.ID)
		_ = d.Q.MarkDone(m)
	case confirmConsumed:
		d.fileConsumed(m)
	case confirmUnsubmitted:
		// Positive evidence: collapsed paste still visible and no agent turn
		// started after one beat (muxa#111). File done/ (at-most-once) and
		// tell the parent — unlike generic inconclusive confirm, this signal
		// is not anti-correlated with healthy workers (muxa#110).
		if m.isDispatch() {
			d.notePaste(m)
			log.Printf("unsubmitted %s → %s id=%s (paste visible but turn did not start; filed done/)", m.From, m.To, m.ID)
			_ = d.Q.MarkDone(m)
			d.notifyUnsubmitted(m)
			return
		}
		log.Printf("unconfirmed %s → %s id=%s (pane still free; left queued)", m.From, m.To, m.ID)
	default:
		// Generic inconclusive: parked cursor, fast agent, swallowed paste
		// with no visible collapse — free-detection cannot distinguish
		// success from failure (muxa#110). File first brief done/ (no retry)
		// but do not mail the parent a failure-shaped turn.
		if m.isDispatch() {
			d.notePaste(m)
			log.Printf("unknown %s → %s id=%s (paste accepted but payload not visible; will not retry)", m.From, m.To, m.ID)
			_ = d.Q.MarkDone(m)
			return
		}
		log.Printf("unconfirmed %s → %s id=%s (pane still free; left queued)", m.From, m.To, m.ID)
	}
}

// fileConsumed files a paste into a consuming CLI as delivered (muxa#127).
// Inject returned nil, so paste-buffer and Enter both reached the pane; the
// payload is simply invisible because the CLI queued it. Filing done/ trades
// rare silent loss for never double-delivering: a receiving agent cannot
// tell a duplicate envelope from a genuine repeat instruction, and acting on
// a worker report twice is worse than the parent noticing a missing reply.
func (d *Deliverer) fileConsumed(m *Msg) {
	d.notePaste(m)
	log.Printf("consumed %s → %s id=%s (paste accepted by a pane that does not echo it; will not retry)", m.From, m.To, m.ID)
	_ = d.Q.MarkDone(m)
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

func (d *Deliverer) failDispatchComposerForeign(m *Msg) {
	log.Printf("dispatch refused %s → %s id=%s (composer holds foreign input)", m.From, m.To, m.ID)
	_ = d.Q.MarkFailed(m)
	if m.ParentPane == "" {
		return
	}
	fail := &Msg{
		Pane:         m.ParentPane,
		From:         "broker",
		To:           m.From,
		Text:         dispatchComposerForeignText(m),
		DeadlineUnix: d.now().Add(24 * time.Hour).Unix(),
	}
	if err := d.Q.Put(fail); err != nil {
		log.Printf("dispatch composer-foreign notify %s: %v", m.ID, err)
	}
}

func dispatchComposerForeignText(m *Msg) string {
	return "[muxa] from=broker\n" +
		"dispatch refused: " + m.To + " pane=" + m.Pane + " composer holds foreign input (clear it and re-dispatch)\n" +
		"id=" + m.ID + "\n" +
		"Do not reply.\n"
}

func (d *Deliverer) composerBlocked(pane string) bool {
	d.mu.Lock()
	cur := d.prev[pane]
	d.mu.Unlock()
	return composerInputForeign(cur)
}

// notifyUnsubmitted mails the parent when a dispatch brief was pasted but
// confirm saw collapsed paste with no agent turn after one beat (muxa#111).
// Generic inconclusive confirm does not notify — that signal was
// anti-correlated with real failure (muxa#110).
func (d *Deliverer) notifyUnsubmitted(m *Msg) {
	if m.ParentPane == "" {
		return
	}
	note := &Msg{
		Pane:         m.ParentPane,
		From:         "broker",
		To:           m.From,
		Text:         dispatchUnsubmittedText(m),
		DeadlineUnix: d.now().Add(24 * time.Hour).Unix(),
	}
	if err := d.Q.Put(note); err != nil {
		log.Printf("dispatch unsubmitted notify %s: %v", m.ID, err)
	}
}

func dispatchUnsubmittedText(m *Msg) string {
	return "[muxa] from=broker\n" +
		"dispatch unsubmitted: " + m.To + " pane=" + m.Pane + " brief paste visible but agent turn did not start\n" +
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
		if !empty {
			return false, nil
		}
	} else if !two {
		return false, nil
	}
	d.mu.Lock()
	cur := d.prev[pane]
	d.mu.Unlock()
	if composerInputForeign(cur) {
		return false, nil
	}
	return true, nil
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
	confirmMissed      confirmResult = iota // inconclusive — still free; send may retry; dispatch files done/
	confirmPasted                           // pane reacted; must not retry
	confirmUnsubmitted                      // collapsed paste visible, no turn after wait
	confirmConsumed                         // paste accepted by a CLI that does not echo it; must not retry
)

// mailLanded reports whether a muxa send actually reached the target pane
// as its own turn (muxa#116). Pane-busy alone is not enough — Enter can
// submit unrelated composer text. The envelope marker is authoritative.
func (d *Deliverer) mailLanded(m *Msg) bool {
	if m == nil || m.isDispatch() {
		return true
	}
	marker := "[muxa] from=" + m.From
	check := func(s string) bool {
		return strings.Contains(stripANSI(s), marker)
	}
	if snap, err := d.T.Snapshot(m.Pane); err == nil && check(snap.Capture) {
		return true
	}
	if hist, err := d.T.CaptureHistory(m.Pane); err == nil && check(hist) {
		return true
	}
	return false
}

// confirm takes post-paste snapshot(s). A pane that reacted (cursor row
// no longer empty/prompt, control-mode drawing, or agent turn started) is
// pasted and must not be retried. Collapsed "[Pasted text …]" with no turn
// start is unsubmitted (muxa#111). A pane that stayed free is
// confirmMissed: deliverOne files a first brief done/ (no retry) and leaves
// later mail queued. Dispatch does not match payload against the capture;
// muxa send must (muxa#116).
func (d *Deliverer) confirm(pane string, m *Msg) confirmResult {
	snap, err := d.T.Snapshot(pane)
	if err != nil {
		return confirmMissed
	}
	if d.drawing(pane) {
		return confirmPasted
	}
	if unsubmittedPasteVisible(snap.Capture) && !agentTurnStarted(snap.Capture) {
		time.Sleep(d.T.Delay)
		if d.drawing(pane) {
			return confirmPasted
		}
		snap2, err2 := d.T.Snapshot(pane)
		if err2 == nil {
			snap = snap2
		}
		if agentTurnStarted(snap.Capture) {
			return confirmPasted
		}
		if unsubmittedPasteVisible(snap.Capture) {
			return confirmUnsubmitted
		}
	}
	if snap.Capture != "" && !emptyAtCursor(snap.Capture, snap.CursorY, snap.CursorX) {
		return confirmPasted
	}
	if d.paneConsumesPaste(pane) {
		return confirmConsumed
	}
	return confirmMissed
}

// paneConsumesPaste reports whether the target CLI takes pasted input into
// its own queue without echoing it. Claude Code does exactly that while a
// turn is running: paste-buffer + Enter is accepted and queued, and nothing
// about the payload reaches the capture until the turn ends. For such a
// pane "payload not visible after a successful Inject" is not evidence the
// paste missed, so no absence-of-evidence outcome may drive a retry — the
// pane already has the message and a retry double-delivers it (muxa#127).
//
// This reads @muxa_kind, the roster fact `muxa who` prints. It is not
// chrome modelling: nothing here inspects what the pane drew.
func (d *Deliverer) paneConsumesPaste(pane string) bool {
	k, err := d.T.fmt(pane, "#{@muxa_kind}")
	if err != nil {
		return false
	}
	return strings.TrimSpace(k) == "claude"
}

func (d *Deliverer) pasteIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.pastes))
	copy(out, d.pastes)
	return out
}
