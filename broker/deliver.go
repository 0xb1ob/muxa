package main

import (
	"log"
	"sync"
	"time"
)

type Deliverer struct {
	Q        *Queue
	T        *TMUX
	Poll     time.Duration
	now      func() time.Time
	mu       sync.Mutex
	inflight map[string]bool   // pane currently being injected
	pastes   []string          // test hook: pane ids pasted, in order
	prev     map[string]string // last capture-pane per pane, for two-signal
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
	}
}

func (d *Deliverer) Loop(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		d.Tick()
		select {
		case <-stop:
			return
		case <-time.After(d.Poll):
		}
	}
}

// Tick tries to deliver every pending message at most once.
// A pane that is not free is left queued until its deadline, then
// pasted once as a last-resort fallback.
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
	free, two, err := d.observe(m.Pane)
	if err != nil {
		log.Printf("free %s: %v", m.Pane, err)
		free = false
	}
	if two != free {
		log.Printf("free-detection %s: parser=%v two-signal=%v", m.Pane, free, two)
	}
	fallback := !free && now >= m.DeadlineUnix
	if !free && !fallback {
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
		if fallback || now >= m.DeadlineUnix {
			_ = d.Q.MarkFailed(m)
		}
		return
	}
	d.mu.Lock()
	d.pastes = append(d.pastes, m.Pane+"|"+m.ID)
	d.mu.Unlock()
	if fallback {
		log.Printf("delivered %s → %s id=%s (timeout fallback paste)", m.From, m.To, m.ID)
	} else {
		log.Printf("delivered %s → %s id=%s", m.From, m.To, m.ID)
	}
	_ = d.Q.MarkDone(m)
}

// observe runs both free-detection rules. Paste still follows the parser
// (LooksFree): two-signal cannot see paused typing in a Cursor Agent
// composer, and pasting over a half-typed prompt is worse than a slow brief.
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
	d.mu.Unlock()
	return LooksFree(snap.Capture), TwoSignalFree(prev, snap.Capture, snap.CursorY, snap.CursorX), nil
}

func (d *Deliverer) pasteIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.pastes))
	copy(out, d.pastes)
	return out
}
