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
		// Cold CLI: empty capture LooksFree, but nothing has painted yet.
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
	if !d.confirmed(m) {
		log.Printf("unconfirmed %s → %s id=%s (paste not visible; left queued)", m.From, m.To, m.ID)
		return
	}
	d.mu.Lock()
	d.pastes = append(d.pastes, m.Pane+"|"+m.ID)
	delete(d.held, m.ID)
	d.mu.Unlock()
	log.Printf("delivered %s → %s id=%s", m.From, m.To, m.ID)
	_ = d.Q.MarkDone(m)
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
// silence. Drawing (%output inside the quiet window) waits; so does a pane
// that LooksFree rejects. Two-signal is observed and logged, not followed.
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
	parserFree, two, err := d.observe(pane)
	if err != nil {
		return false, err
	}
	if two != parserFree {
		log.Printf("free-detection %s: parser=%v two-signal=%v", pane, parserFree, two)
	}
	return parserFree, nil
}

func (d *Deliverer) drawing(pane string) bool {
	return d.Ctrl != nil && d.Ctrl.Live() && d.Ctrl.Drawing(pane)
}

// observe runs both free-detection rules. Paste still follows the parser
// (LooksFree): two-signal cannot see paused typing in a Cursor Agent
// composer, and neither can control-mode silence — a paused half-typed
// composer emits no %output, same as an empty one.
func (d *Deliverer) observe(pane string) (parserFree, twoFree bool, err error) {
	if d.T.PaneDead(pane) {
		return false, false, nil
	}
	if d.T.InMode(pane) {
		return false, false, nil
	}
	snap, err := d.T.Snapshot(pane)
	if err != nil {
		return false, false, err
	}
	d.mu.Lock()
	prev := d.prev[pane]
	d.prev[pane] = snap.Capture
	if visibleContent(snap.Capture) {
		d.drew[pane] = true
	}
	d.mu.Unlock()
	return LooksFree(snap.Capture), TwoSignalFree(prev, snap.Capture, snap.CursorY, snap.CursorX), nil
}

func visibleContent(capture string) bool {
	return strings.TrimSpace(stripANSI(capture)) != ""
}

func (d *Deliverer) confirmed(m *Msg) bool {
	needle := confirmNeedle(m.Text)
	if needle == "" {
		return false
	}
	for i := 0; i < 3; i++ {
		cap, err := d.T.Capture(m.Pane)
		if err == nil && landed(cap, needle) {
			return true
		}
		if i < 2 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return false
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

func (d *Deliverer) pasteIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.pastes))
	copy(out, d.pastes)
	return out
}
