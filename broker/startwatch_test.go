package main

import (
	"strings"
	"testing"
	"time"
)

// The three shapes muxa#142 has to tell apart, all of them kind=claude and
// all of them showing an idle-looking pane one beat after a successful paste.
const (
	// Brief parked in the composer of a pane that is doing nothing. One
	// Enter starts it. Three of these were reported from the field in one
	// afternoon and every one was real.
	pasteParked = "▄▄▄▄▄▄▄▄▄▄\n [Pasted text #1 +79 lines]\n▀▀▀▀▀▀▀▀▀▀\n ? for shortcuts"
	// Splash screen with an empty composer: the brief never reached the
	// application. Same afternoon, no notice at all.
	splashIdle = "✻ Welcome to Claude Code\n▄▄▄▄▄▄▄▄▄▄\n   \n▀▀▀▀▀▀▀▀▀▀\n Context: 0.0%"
)

// bootFrames renders a booting CLI: the collapsed brief is on screen (Claude
// keeps it there while it consumes) and something animates every frame.
func bootFrames(n int) []string {
	spin := []string{"✻", "✽", "✳", "✶"}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, spin[i%len(spin)]+" Booting…\n"+pasteParked)
	}
	return out
}

func fastWatch(d *Deliverer) {
	d.StartStep = time.Millisecond
	d.StartWindow = 40 * time.Millisecond
	d.StartStable = 3
}

func dispatchAndWatch(t *testing.T, f *fakeTMUX, tune func(*Deliverer)) *Queue {
	t.Helper()
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	fastWatch(d)
	if tune != nil {
		tune(d)
	}
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{
		ID: "w142", Pane: "%1", From: "parent", To: "kid", Text: "BRIEF-BODY",
		DeadlineUnix: 2000, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	d.waitWatchers()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	return q
}

func brokerNotice(t *testing.T, q *Queue) string {
	t.Helper()
	pending, _ := q.Pending()
	for _, m := range pending {
		if m.From == "broker" {
			if strings.Contains(m.Text, "BRIEF-BODY") {
				t.Fatal("broker notice leaked the child brief body")
			}
			return m.Text
		}
	}
	return ""
}

// muxa#142, the false positive. The brief was consumed; Claude is still
// booting and keeps the collapsed placeholder on screen the whole time. The
// pane never holds still, so the watch must not call this unsubmitted — a
// parent that "recovers" from that notice presses Enter into a live job.
func TestSlowBootDispatchNotifiesNothing(t *testing.T) {
	frames := append(bootFrames(6), "✻ Working…\n"+pasteParked+"\n esc to interrupt · ctrl+c to stop")
	f := &fakeTMUX{
		captures: append([]string{"ready>", "ready>"}, frames...),
		hideEcho: true, kind: "claude",
	}
	q := dispatchAndWatch(t, f, nil)
	if note := brokerNotice(t, q); note != "" {
		t.Fatalf("slow boot notified the parent: %s", note)
	}
}

// Same, without the turn ever appearing inside the budget: a pane that is
// still animating when the watch gives up is inconclusive, not a failure.
func TestBootStillAnimatingAtDeadlineNotifiesNothing(t *testing.T) {
	f := &fakeTMUX{
		captures: append([]string{"ready>", "ready>"}, bootFrames(4000)...),
		hideEcho: true, kind: "claude",
	}
	q := dispatchAndWatch(t, f, nil)
	if note := brokerNotice(t, q); note != "" {
		t.Fatalf("still-booting pane notified the parent: %s", note)
	}
}

// muxa#111's report, and three true positives in one afternoon on 1.0.19:
// collapse in the composer of a pane that has gone still. This must stay as
// loud as it was — fixing the false positive by muting Claude would trade one
// broken direction for the other.
func TestParkedPasteOnStillPaneNotifiesParent(t *testing.T) {
	f := &fakeTMUX{
		captures: []string{"ready>", "ready>", pasteParked},
		hideEcho: true, kind: "claude",
	}
	q := dispatchAndWatch(t, f, nil)
	note := brokerNotice(t, q)
	if !strings.Contains(note, "dispatch unsubmitted") || !strings.Contains(note, "brief paste visible") {
		t.Fatalf("parked paste notice: %q", note)
	}
	if !strings.Contains(note, "kid") || !strings.Contains(note, "w142") {
		t.Fatalf("notice missing name/id: %q", note)
	}
}

// The false negative: pane sat on its splash screen at Context 0.0% with an
// empty composer and the parent was told nothing at all. Nothing about the
// pane changed across the paste, and an idle Claude composer echoes a pasted
// brief — so the payload never reached the application.
func TestSplashPaneThatNeverGotTheBriefNotifiesParent(t *testing.T) {
	f := &fakeTMUX{
		captures:   []string{splashIdle, splashIdle, splashIdle},
		hideEcho:   true,
		kind:       "claude",
		parkCursor: "2 2",
	}
	q := dispatchAndWatch(t, f, nil)
	note := brokerNotice(t, q)
	if !strings.Contains(note, "dispatch unsubmitted") || !strings.Contains(note, "left no trace") {
		t.Fatalf("splash notice: %q", note)
	}
}

// muxa#110's guard, unchanged: a kind that swallows pasted input without
// echoing it tells the broker nothing by staying blank, so a blank pane of
// that kind still gets no failure-shaped turn.
func TestSwallowedPasteOnNonEchoingKindNotifiesNothing(t *testing.T) {
	for _, kind := range []string{"generic", "cursor", "pi", ""} {
		name := kind
		if name == "" {
			name = "unregistered"
		}
		t.Run(name, func(t *testing.T) {
			f := &fakeTMUX{
				captures: []string{"ready> ", "ready> ", "ready> "},
				hideEcho: true, kind: kind,
			}
			q := dispatchAndWatch(t, f, nil)
			if note := brokerNotice(t, q); note != "" {
				t.Fatalf("%s swallowed paste notified the parent: %s", name, note)
			}
		})
	}
}

// A brief that submitted and whose turn has already finished still shows
// "[Pasted text …]" — as the user turn in the transcript, not in the
// composer. Matching the whole capture would report a completed job as
// unsubmitted, which is muxa#142 from the other end.
func TestSubmittedPasteInTranscriptNotifiesNothing(t *testing.T) {
	done := "> [Pasted text #1 +79 lines] — read, PR open\n▄▄▄▄▄▄▄▄▄▄\n   \n▀▀▀▀▀▀▀▀▀▀\n Context: 12%"
	f := &fakeTMUX{
		captures:   []string{splashIdle, splashIdle, done},
		hideEcho:   true,
		kind:       "claude",
		parkCursor: "2 2",
	}
	q := dispatchAndWatch(t, f, nil)
	if note := brokerNotice(t, q); note != "" {
		t.Fatalf("finished turn notified the parent: %s", note)
	}
}
