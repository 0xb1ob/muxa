package main

import (
	"log"
	"time"
)

// Post-paste start watch (muxa#142).
//
// #111 asked one question one time: is a collapsed "[Pasted text …]" still on
// screen a beat after the paste? That reads a *slow* CLI as a *dropped*
// brief. Claude Code keeps the placeholder up through a cold boot, so three
// workers that were already writing code got told, via their parent, that
// their brief had never submitted — and a parent following AGENTS.md
// "recovers" from that by pressing Enter into a live job.
//
// The same single frame is blind the other way: a pane still on its splash
// screen with an empty composer, which never received the brief at all, is
// indistinguishable from a healthy consuming CLI (muxa#127) in one snapshot,
// so it gets no notice whatsoever.
//
// Neither direction is fixable by moving the threshold, because the frame is
// the wrong evidence. The watch samples the pane over seconds and decides on
// what the pane *did*:
//
//   - an agent turn appears        → started, no notice
//   - the pane keeps animating     → still consuming; budget out, no notice
//   - the pane goes and stays still, collapse in the composer → unsubmitted
//   - the pane goes and stays still, showing the very same picture it showed
//     *before* the paste, on a kind that echoes pasted text when idle
//     → the brief left no trace at all
//
// Stillness is the discriminator both cases were missing. A booting CLI
// animates — spinner, elapsed timer, token count — and sameFrame (muxa#144)
// already ignores caret blink, so "still" means still.
type startVerdict int

const (
	startTurn         startVerdict = iota // an agent turn is on screen
	startUnsubmitted                      // collapse parked in a static composer
	startNoTrace                          // pane unchanged by the paste
	startInconclusive                     // still moving, or unreadable
)

func (v startVerdict) String() string {
	switch v {
	case startTurn:
		return "turn started"
	case startUnsubmitted:
		return "unsubmitted: collapse parked in a static composer"
	case startNoTrace:
		return "no trace: pane unchanged by the paste"
	}
	return "inconclusive: pane still moving when the watch budget ran out"
}

// watchDispatchStart runs the watch off the delivery loop. Tick must not
// block for seconds per dispatch, and the message is already filed done/ —
// nothing here can re-paste it (muxa#129), it only decides what the parent
// is told. The pane is not held: a paste from anywhere else during the watch
// moves the picture, which can only ever suppress a notice, never invent one.
func (d *Deliverer) watchDispatchStart(m *Msg, pre string) {
	if m == nil || m.ParentPane == "" {
		return
	}
	d.mu.Lock()
	if d.watching[m.Pane] {
		d.mu.Unlock()
		return
	}
	d.watching[m.Pane] = true
	d.mu.Unlock()
	d.watchers.Add(1)
	go func() {
		defer d.watchers.Done()
		defer func() {
			d.mu.Lock()
			delete(d.watching, m.Pane)
			d.mu.Unlock()
		}()
		v := d.dispatchStart(m.Pane, pre)
		log.Printf("dispatch start %s pane=%s id=%s (%s)", m.To, m.Pane, m.ID, v)
		switch v {
		case startUnsubmitted:
			d.notifyUnsubmitted(m)
		case startNoTrace:
			d.notifyNoTrace(m)
		}
	}()
}

// dispatchStart samples the pane until it either shows a turn, holds still
// long enough to be judged, or runs out of budget.
func (d *Deliverer) dispatchStart(pane, pre string) startVerdict {
	step, window, need := d.startWatch()
	// Wall clock, not d.now: this is a real wait on a real CLI booting, and
	// the queue clock is frozen in tests and stepped by hand.
	deadline := time.Now().Add(window)
	prev := ""
	stable := 0
	for {
		if dead, err := d.T.PaneDead(pane); err != nil || dead {
			return startInconclusive
		}
		snap, err := d.T.Snapshot(pane)
		if err != nil {
			return startInconclusive
		}
		if agentTurnStarted(snap.Capture) {
			return startTurn
		}
		switch {
		case d.drawing(pane), prev == "", !sameFrame(prev, snap.Capture):
			stable = 0
		default:
			stable++
		}
		prev = snap.Capture
		if stable >= need {
			return d.stillVerdict(pane, pre, snap)
		}
		if !time.Now().Before(deadline) {
			return startInconclusive
		}
		time.Sleep(step)
	}
}

// stillVerdict judges a pane that has stopped moving. Nothing is going to
// happen on its own now, so absence is evidence at last.
func (d *Deliverer) stillVerdict(pane, pre string, snap Snapshot) startVerdict {
	if pasteInComposer(snap.Capture) {
		// The brief is sitting in the composer of a pane that is doing
		// nothing. One Enter would start it — that is the #111 report, and
		// it stays exactly as loud as it was.
		return startUnsubmitted
	}
	if !d.untouchedByPaste(pane, pre, snap) {
		// Payload invisible on a pane that has content of its own, or on a
		// kind whose invisibility means nothing (muxa#110, muxa#127). Still
		// not a failure report.
		return startInconclusive
	}
	return startNoTrace
}

// untouchedByPaste reports whether the pane is showing the identical picture
// it showed before the paste, on a kind for which that is meaningful.
//
// The kind read is @muxa_kind, the roster fact `muxa who` prints — the same
// one muxa#127 uses to name why a payload is invisible. An idle Claude
// composer echoes a pasted brief as a collapsed placeholder and a busy one is
// visibly busy, so "idle, and not one cell different from before the paste"
// is positive evidence the payload never reached the application. On a kind
// with no such contract — a shell reading stdin with echo off, muxa#110's
// swallowed paste — invisibility still means nothing and nothing is mailed.
func (d *Deliverer) untouchedByPaste(pane, pre string, snap Snapshot) bool {
	if !d.paneConsumesPaste(pane) {
		return false
	}
	if !d.sawDraw(pane) || pre == "" {
		return false
	}
	if !emptyAtCursor(snap.Capture, snap.CursorY, snap.CursorX) {
		return false
	}
	return sameFrame(pre, snap.Capture)
}

// startWatch resolves the watch timings.
//
// step is the sampling interval, window the total budget, need the number of
// consecutive identical frames that make a pane "still". Defaults: sample at
// the broker poll (250ms floor), call a pane still after 4 matching frames
// (~1s of stillness), give up after 20s. The budget is sized for a cold
// Claude boot on a large repo — the field reports that opened muxa#142 had
// placeholders outlive a 250ms settle by many seconds — and it costs only
// notice latency, never a paste.
func (d *Deliverer) startWatch() (step, window time.Duration, need int) {
	step, window, need = d.StartStep, d.StartWindow, d.StartStable
	if step <= 0 {
		step = d.Poll
		if step < 250*time.Millisecond {
			step = 250 * time.Millisecond
		}
	}
	if window <= 0 {
		window = 20 * time.Second
	}
	if need <= 0 {
		need = 4
	}
	return step, window, need
}

// waitWatchers blocks until every start watch has finished. Test hook: the
// watch is deliberately off the delivery loop, so a test that asserts on the
// parent queue after Tick has to join it.
func (d *Deliverer) waitWatchers() { d.watchers.Wait() }
