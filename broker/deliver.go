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
	retryAt  map[string]int64  // msg id -> unix nanos before which a failed Inject is not retried
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
		retryAt:  map[string]int64{},
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
// clobber each other and still get filed as done. A message only reaches a
// second Tick if it was never pasted: the pane was not free, or Inject
// itself failed (muxa#129).
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
			d.clearRetryAt(m.ID)
			_ = d.Q.MarkFailed(m)
		}
		return
	}
	if !d.retryDue(m.ID, d.now()) {
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
		d.noteInjectFailed(m, err)
		return
	}
	// Inject returned nil: load-buffer, paste-buffer -p -d and send-keys
	// Enter all exited 0, so tmux has accepted the payload into the pane.
	// That is the delivery boundary. muxa mail is at-most-once (muxa#129):
	// past this line the message is filed and never re-pasted, whatever
	// confirm saw. Whether the CLI *renders* the text is a question about
	// pixels the broker neither controls nor versions, and inferring
	// "never arrived" from it is what double-delivered envelopes 16 and 18
	// times (muxa#127).
	res := d.confirm(m.Pane, m)
	d.fileDelivered(m, d.confidence(m, res))
	if res == confirmUnsubmitted && m.isDispatch() {
		// Positive evidence that the brief did not submit: collapsed paste
		// still visible and no agent turn after one beat (muxa#111). Tell
		// the parent — unlike generic inconclusive confirm, this signal is
		// not anti-correlated with healthy workers (muxa#110).
		d.notifyUnsubmitted(m)
	}
}

// fileDelivered files a paste tmux accepted, with the confidence that
// post-paste observation earned it. The confidence text is diagnostic: an
// operator can tell "delivered, confirmed" from "delivered, unverified" in
// the log, but no value of it can cause a second paste (muxa#129).
func (d *Deliverer) fileDelivered(m *Msg, confidence string) {
	d.notePaste(m)
	log.Printf("delivered %s → %s id=%s (%s)", m.From, m.To, m.ID, confidence)
	_ = d.Q.MarkDone(m)
}

// confidence renders the post-paste evidence for a message Inject already
// accepted. Every branch files done/; they differ only in what the log says
// (muxa#129). Absence of the payload is still worth naming — it separates
// a consuming CLI (muxa#127) from a pane that stayed visibly free — it is
// just not evidence the paste missed.
func (d *Deliverer) confidence(m *Msg, res confirmResult) string {
	switch res {
	case confirmPasted:
		if m.isDispatch() {
			return "confirmed: pane reacted to the paste"
		}
		// The envelope marker is the muxa#116 landing proof.
		if d.mailLanded(m) {
			return "confirmed: pane reacted and the [muxa] envelope is visible"
		}
		if d.paneConsumesPaste(m.Pane) {
			return "unverified: pane reacted; a consuming CLI does not echo the envelope"
		}
		return "unverified: pane reacted but the [muxa] envelope never appeared"
	case confirmConsumed:
		return "unverified: paste accepted by a pane that does not echo it"
	case confirmUnsubmitted:
		return "unverified: collapsed paste still visible, no agent turn started"
	}
	return "unverified: payload not visible after a successful paste"
}

// maxInjectAttempts bounds the one path that may still re-paste: Inject
// itself returning an error. That is an observable, unambiguous failure
// (a tmux command exited non-zero), not a scraped inference, so retrying
// it is sound — but only up to a ceiling, so a pane that rejects every
// paste cannot spin forever. After the ceiling the message is filed
// failed/ with an explicit outcome.
const maxInjectAttempts = 3

// injectRetryBackoff is the wait before the second attempt; it doubles for
// each further one. Without it the poll loop retries a failing tmux command
// several times a second.
const injectRetryBackoff = 500 * time.Millisecond

// noteInjectFailed schedules or gives up on the Inject-failed path.
func (d *Deliverer) noteInjectFailed(m *Msg, err error) {
	if m.Attempts >= maxInjectAttempts {
		d.failInject(m, err)
		return
	}
	back := injectRetryBackoff << uint(m.Attempts-1)
	d.mu.Lock()
	d.retryAt[m.ID] = d.now().Add(back).UnixNano()
	d.mu.Unlock()
	log.Printf("inject %s → %s id=%s attempt=%d/%d (retrying in %s): %v",
		m.From, m.To, m.ID, m.Attempts, maxInjectAttempts, back, err)
}

// failInject files a message tmux never accepted. Nothing was pasted, so
// there is no duplicate to worry about; the loss is real and must be
// visible.
func (d *Deliverer) failInject(m *Msg, err error) {
	d.clearRetryAt(m.ID)
	log.Printf("undelivered %s → %s id=%s (paste failed %d/%d times, last: %v; filed failed/)",
		m.From, m.To, m.ID, m.Attempts, maxInjectAttempts, err)
	_ = d.Q.MarkFailed(m)
	if !m.isDispatch() || m.ParentPane == "" {
		return
	}
	fail := &Msg{
		Pane:         m.ParentPane,
		From:         "broker",
		To:           m.From,
		Text:         dispatchInjectFailedText(m),
		DeadlineUnix: d.now().Add(24 * time.Hour).Unix(),
	}
	if err := d.Q.Put(fail); err != nil {
		log.Printf("dispatch inject-fail notify %s: %v", m.ID, err)
	}
}

func dispatchInjectFailedText(m *Msg) string {
	return "[muxa] from=broker\n" +
		"dispatch failed: " + m.To + " pane=" + m.Pane + " paste command failed every attempt\n" +
		"id=" + m.ID + "\n" +
		"Do not reply.\n"
}

// retryDue reports whether a message whose Inject failed has waited out its
// backoff. Messages with no recorded failure are always due.
func (d *Deliverer) retryDue(id string, now time.Time) bool {
	d.mu.Lock()
	at, ok := d.retryAt[id]
	d.mu.Unlock()
	return !ok || now.UnixNano() >= at
}

func (d *Deliverer) clearRetryAt(id string) {
	d.mu.Lock()
	delete(d.retryAt, id)
	d.mu.Unlock()
}

func (d *Deliverer) notePaste(m *Msg) {
	d.mu.Lock()
	d.pastes = append(d.pastes, m.Pane+"|"+m.ID)
	delete(d.held, m.ID)
	delete(d.retryAt, m.ID)
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
	d.clearRetryAt(m.ID)
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
	d.clearRetryAt(m.ID)
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

// No confirmResult drives a retry (muxa#129). They rank how much evidence
// backs a paste tmux already accepted, for the log line only.
const (
	confirmMissed      confirmResult = iota // pane stayed free; payload never appeared
	confirmPasted                           // pane reacted
	confirmUnsubmitted                      // collapsed paste visible, no turn after wait
	confirmConsumed                         // pane kind takes paste without echoing it
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

// confirm takes post-paste snapshot(s) and reports how visible the paste
// is: a pane that reacted (cursor row no longer empty/prompt, control-mode
// drawing, or agent turn started) is confirmPasted; collapsed
// "[Pasted text …]" with no turn start is confirmUnsubmitted (muxa#111); a
// pane whose kind consumes paste without echoing it is confirmConsumed
// (muxa#127); a pane that stayed visibly free is confirmMissed.
//
// None of these decide delivery. Inject already did that: deliverOne files
// the message either way and never re-pastes it (muxa#129). Screen-scraped
// absence is not evidence a paste missed, and treating it as such is what
// pasted one envelope sixteen times.
func (d *Deliverer) confirm(pane string, m *Msg) confirmResult {
	snap, err := d.T.Snapshot(pane)
	if err != nil {
		return confirmMissed
	}
	if unsubmittedPasteVisible(snap.Capture) && !agentTurnStarted(snap.Capture) {
		// muxa#126: mid-confirm keystrokes emit %output and change the
		// capture; neither is proof the brief submitted. Wait for
		// quiescence, then re-check the collapsed placeholder.
		d.waitConfirmSettle(pane)
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
	if d.drawing(pane) {
		return confirmPasted
	}
	if snap.Capture != "" && !emptyAtCursor(snap.Capture, snap.CursorY, snap.CursorX) {
		return confirmPasted
	}
	if d.paneConsumesPaste(pane) {
		return confirmConsumed
	}
	return confirmMissed
}

// waitConfirmSettle sleeps one broker beat, then waits out any control-mode
// drawing on the pane so a mid-confirm keystroke (muxa#126) does not get
// mistaken for submission.
func (d *Deliverer) waitConfirmSettle(pane string) {
	delay := d.T.Delay
	if delay <= 0 {
		delay = time.Millisecond
	}
	time.Sleep(delay)
	if d.Ctrl == nil || !d.Ctrl.Live() {
		return
	}
	quiet := d.Ctrl.Quiet
	if quiet <= 0 {
		quiet = 250 * time.Millisecond
	}
	deadline := time.Now().Add(quiet + delay)
	step := quiet / 4
	if step <= 0 {
		step = time.Millisecond
	}
	for time.Now().Before(deadline) {
		if !d.drawing(pane) {
			return
		}
		time.Sleep(step)
	}
}

// paneConsumesPaste reports whether the target CLI takes pasted input into
// its own queue without echoing it. Claude Code does exactly that while a
// turn is running: paste-buffer + Enter is accepted and queued, and nothing
// about the payload reaches the capture until the turn ends (muxa#127).
//
// Since muxa#129 no pane kind is retried after a successful Inject, so this
// no longer gates a retry — it names *why* the payload is invisible in the
// log, which is the difference between a healthy consuming CLI and a pane
// that sat there doing nothing.
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
