package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

type fakeTMUX struct {
	mu              sync.Mutex
	dead            bool
	mode            bool
	captures        []string
	capI            int
	injects         []string
	failInj         bool
	echo            string
	hideEcho        bool
	lastCap         string
	cursor          string // optional "#{cursor_y} #{cursor_x}" override
	parkCursor      string // if set, capture-pane leaves the hardware cursor here
	parkAfterInject string // after Inject, park on a blank footer row (#105)
	kind            string // @muxa_kind for this pane ("" = unregistered)
}

func (f *fakeTMUX) runner(args []string, stdin []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "display-message":
		fmt := args[len(args)-1]
		switch fmt {
		case "#{pane_dead}":
			if f.dead {
				return "1", nil
			}
			return "0", nil
		case "#{pane_in_mode}":
			if f.mode {
				return "1", nil
			}
			return "0", nil
		case "#{cursor_y} #{cursor_x}":
			if f.cursor != "" {
				return f.cursor, nil
			}
			return fakeCursorPos(f.lastCap), nil
		case "#{@muxa_kind}":
			return f.kind, nil
		}
		return "0", nil
	case "capture-pane":
		s := ""
		if f.capI < len(f.captures) {
			s = f.captures[f.capI]
			f.capI++
		} else if len(f.captures) > 0 {
			s = f.captures[len(f.captures)-1]
		}
		if f.echo != "" && !f.hideEcho {
			if s == "" {
				s = f.echo
			} else {
				s = f.echo + "\n" + s
			}
		}
		f.lastCap = s
		// emptyAtCursor sees a reacted pane without matching the payload.
		if f.parkCursor != "" {
			f.cursor = f.parkCursor
		} else {
			cursorSrc := s
			if f.echo != "" && !f.hideEcho {
				cursorSrc = f.echo
			}
			f.cursor = fakeCursorPos(cursorSrc)
		}
		return s, nil
	case "load-buffer":
		if f.failInj {
			return "", errPaste
		}
		f.injects = append(f.injects, string(stdin))
		f.echo = string(stdin)
		if f.parkAfterInject != "" {
			f.parkCursor = f.parkAfterInject
		}
		return "", nil
	case "paste-buffer":
		if f.failInj {
			return "", errPaste
		}
		return "", nil
	case "send-keys":
		return "", nil
	case "delete-buffer":
		return "", nil
	}
	return "", nil
}

func fakeCursorPos(cap string) string {
	lines := strings.Split(cap, "\n")
	y, x := 0, 0
	for i, l := range lines {
		runes := visibleRunes(l)
		empty := true
		for _, r := range runes {
			if !unicode.IsSpace(r) {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		y = i
		x = len(runes)
	}
	return strconv.Itoa(y) + " " + strconv.Itoa(x)
}

type pasteErr string

func (e pasteErr) Error() string { return string(e) }

const errPaste pasteErr = "paste failed"

func (f *fakeTMUX) injectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.injects)
}

func (f *fakeTMUX) lastInject() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.injects) == 0 {
		return ""
	}
	return f.injects[len(f.injects)-1]
}

func (f *fakeTMUX) injectCountOf(text string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.injects {
		if s == text {
			n++
		}
	}
	return n
}

func testTMUX(f *fakeTMUX) *TMUX {
	t := &TMUX{Delay: 0, Run: f.runner}
	return t
}

func TestRetryUntilFree(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{
		"ready> still typing",
		"ready> still typing",
		"ready>",
	}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{ID: "r1", Pane: "%1", From: "c", To: "p", Text: formatOne("c", "", "TOKEN-RETRY"), DeadlineUnix: 2000})

	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted while typing")
	}
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted on second busy capture")
	}
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted on first free frame (two-signal needs a pair)")
	}
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 paste after free, got %d", f.injectCount())
	}
	pending, _ := q.Pending()
	if len(pending) != 0 {
		t.Fatalf("still pending: %+v", pending)
	}
	if f.lastInject() != formatOne("c", "", "TOKEN-RETRY") {
		t.Fatalf("payload=%q", f.lastInject())
	}
	if p, doneN, failed, err := q.Counts(); err != nil || p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("counts pending=%d done=%d failed=%d err=%v", p, doneN, failed, err)
	}
}

func TestNoTimeoutFallbackPaste(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready> never clears"}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := int64(1000)
	d.now = func() time.Time { return time.Unix(now, 0) }
	_ = q.Put(&Msg{ID: "t1", Pane: "%1", From: "c", To: "p", Text: "TOKEN-TIMEOUT", DeadlineUnix: 1005})

	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted before deadline")
	}
	now = 1005
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("timeout fallback pasted into a busy pane: %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 1 || doneN != 0 || failed != 0 {
		t.Fatalf("after deadline pending=%d done=%d failed=%d", p, doneN, failed)
	}
}

// muxa#129: an unconfirmed paste is delivered-unverified, not undelivered.
// Inject returned nil, so tmux took the text; the pane simply never showed
// it. Re-pasting on that inference is what pasted one envelope 16 times.
func TestUnconfirmedPasteIsFiledNotRetried(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}, hideEcho: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	text := formatOne("c", "", "TOKEN-GHOST")
	_ = q.Put(&Msg{ID: "u1", Pane: "%1", From: "c", To: "p", Text: text, DeadlineUnix: 2000})

	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("unconfirmed pending=%d done=%d failed=%d", p, doneN, failed)
	}
	d.Tick()
	d.Tick()
	if n := f.injectCountOf(text); n != 1 {
		t.Fatalf("re-pasted an unconfirmed send: %d", n)
	}
	if a := doneAttempts(t, dir, "u1"); a != 1 {
		t.Fatalf("want attempts=1, got %d", a)
	}
}

func TestBusyPaneDeliversInOrderAfterFree(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready> BLOCKED"}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := int64(1000)
	d.now = func() time.Time { return time.Unix(now, 0) }
	_ = q.Put(&Msg{ID: "a", Pane: "%1", From: "c", To: "p", Text: formatOne("c", "", "first-mail"), EnqueuedUnix: 1, DeadlineUnix: 1005})
	_ = q.Put(&Msg{ID: "b", Pane: "%1", From: "c", To: "p", Text: formatOne("c", "", "second-mail"), EnqueuedUnix: 2, DeadlineUnix: 1005})

	d.Tick()
	now = 1010
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted while busy: %d", f.injectCount())
	}

	f.mu.Lock()
	f.captures = []string{"ready>"}
	f.capI = 0
	f.mu.Unlock()
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted on first free frame after busy (two-signal needs a pair): %d", f.injectCount())
	}
	d.Tick()
	if f.injectCount() != 1 || f.lastInject() != formatOne("c", "", "first-mail") {
		t.Fatalf("first paste=%q count=%d", f.lastInject(), f.injectCount())
	}
	if ids := d.pasteIDs(); len(ids) != 1 || ids[0] != "%1|a" {
		t.Fatalf("paste ids after first: %v", ids)
	}

	f.mu.Lock()
	f.echo = ""
	f.capI = 0
	f.mu.Unlock()
	d.Tick()
	if f.injectCount() != 2 || f.lastInject() != formatOne("c", "", "second-mail") {
		t.Fatalf("second paste=%q count=%d", f.lastInject(), f.injectCount())
	}
	if ids := d.pasteIDs(); len(ids) != 2 || ids[0] != "%1|a" || ids[1] != "%1|b" {
		t.Fatalf("paste order: %v", ids)
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 2 || failed != 0 {
		t.Fatalf("after both pending=%d done=%d failed=%d", p, doneN, failed)
	}
}

func TestNoDoubleDelivery(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{ID: "d1", Pane: "%1", From: "c", To: "p", Text: formatOne("c", "", "ONCE"), DeadlineUnix: 2000})

	d.Tick()
	d.Tick()
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("double delivery: %d pastes", f.injectCount())
	}
	// Restart deliverer against the same dir: done messages must not paste again.
	q2, _ := OpenQueue(dir)
	d2 := NewDeliverer(q2, testTMUX(f), time.Millisecond)
	d2.now = d.now
	d2.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("restart re-delivered: %d pastes", f.injectCount())
	}
}

func TestSkipOtherPaneWhileOneInflight(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{ID: "a", Pane: "%1", From: "c", To: "p", Text: formatOne("c", "", "first"), DeadlineUnix: 2000})
	_ = q.Put(&Msg{ID: "b", Pane: "%1", From: "c", To: "p", Text: formatOne("c", "", "second"), DeadlineUnix: 2000})
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("same-pane batch should paste at most one per tick, got %d", f.injectCount())
	}
}

func TestDispatchWaitsUntilPaneDrew(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"\n\n", "ready>"}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{
		ID: "d1", Pane: "%1", From: "p", To: "kid", Text: "FIRST-BRIEF",
		DeadlineUnix: 2000, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted into a pane that has not drawn: %d", f.injectCount())
	}
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted on first ready frame (two-signal needs a pair): %d", f.injectCount())
	}
	d.Tick()
	if f.injectCount() != 1 || f.lastInject() != "FIRST-BRIEF" {
		t.Fatalf("want brief after ready, got %q count=%d", f.lastInject(), f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("after ready pending=%d done=%d failed=%d", p, doneN, failed)
	}
}

func TestDispatchDeadlineNotifiesParentNotChild(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"\n\n"}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := int64(1000)
	d.now = func() time.Time { return time.Unix(now, 0) }
	_ = q.Put(&Msg{
		ID: "d1", Pane: "%1", From: "parent", To: "stuck", Text: "NEVER-BRIEF",
		DeadlineUnix: 1005, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	now = 1005
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("timeout-pasted the brief into a cold pane: %d %q", f.injectCount(), f.lastInject())
	}
	if p, doneN, failed, _ := q.Counts(); doneN != 0 || failed != 1 || p != 1 {
		t.Fatalf("want brief failed + parent notify pending, pending=%d done=%d failed=%d", p, doneN, failed)
	}
	pending, _ := q.Pending()
	if len(pending) != 1 || pending[0].From != "broker" || pending[0].Pane != "%2" {
		t.Fatalf("parent notify: %+v", pending)
	}
	if strings.Contains(pending[0].Text, "NEVER-BRIEF") {
		t.Fatal("failure turn must not include the undelivered brief")
	}
	if !strings.Contains(pending[0].Text, "stuck") || !strings.Contains(pending[0].Text, "d1") {
		t.Fatalf("failure turn missing name/id: %s", pending[0].Text)
	}

	f.mu.Lock()
	f.captures = []string{"ready>"}
	f.capI = 0
	f.echo = ""
	f.mu.Unlock()
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want parent failure paste, got %d", f.injectCount())
	}
	if strings.Contains(f.lastInject(), "NEVER-BRIEF") {
		t.Fatal("child brief must not be pasted after dispatch failure")
	}
	if !strings.Contains(f.lastInject(), "dispatch failed") {
		t.Fatalf("parent paste=%q", f.lastInject())
	}
}

func TestDispatchSwallowedPasteNoParentNotify(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}, hideEcho: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{
		ID: "un1", Pane: "%1", From: "parent", To: "swallowed",
		Text:         "SWALLOWED-BRIEF",
		DeadlineUnix: 2000, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	// Paste accepted but inconclusive (no visible collapse): files done/, no parent notify.
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("swallowed dispatch pending=%d done=%d failed=%d", p, doneN, failed)
	}
	pending, _ := q.Pending()
	if len(pending) != 0 {
		t.Fatalf("inconclusive dispatch must not notify parent: %+v", pending)
	}

	f.mu.Lock()
	f.captures = []string{"ready>"}
	f.capI = 0
	f.mu.Unlock()
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("must not re-paste or notify parent: %d injects", f.injectCount())
	}
}

func TestCursorCollapsedPasteIsDelivered(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>", "ready>", "[Pasted text +48 lines]", "working...", "▄▄▄▄\n → Add a follow-up   ctrl+c to stop\n▀▀▀▀"}, hideEcho: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{
		ID: "c1", Pane: "%372", From: "parent", To: "muxa-darwin",
		Text:         "[muxa] from=parent\nNew job on feat/muxa-dispatch that Cursor will collapse.\nReply: muxa send parent \"…\"\n",
		DeadlineUnix: 2000, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("collapsed paste pending=%d done=%d failed=%d", p, doneN, failed)
	}
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("retried a collapsed Cursor paste: %d", f.injectCount())
	}
}

func TestDispatchRefusesComposerForeignInput(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	typed := "log\n▄▄▄▄▄\n operator-typed\n▀▀▀▀▀\n footer"
	f := &fakeTMUX{captures: []string{typed, typed}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := int64(1000)
	d.now = func() time.Time { return time.Unix(now, 0) }
	_ = q.Put(&Msg{
		ID: "f1", Pane: "%1", From: "parent", To: "kid", Text: "SECRET-BRIEF",
		DeadlineUnix: 1005, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted over foreign composer input: %d", f.injectCount())
	}
	now = 1005
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("timeout-pasted over foreign composer: %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); doneN != 0 || failed != 1 || p != 1 {
		t.Fatalf("want failed brief + parent notify pending, pending=%d done=%d failed=%d", p, doneN, failed)
	}
	pending, _ := q.Pending()
	if len(pending) != 1 || pending[0].From != "broker" {
		t.Fatalf("parent notify: %+v", pending)
	}
	if !strings.Contains(pending[0].Text, "composer holds foreign input") {
		t.Fatalf("notify text: %s", pending[0].Text)
	}
	if strings.Contains(pending[0].Text, "SECRET-BRIEF") {
		t.Fatal("failure turn must not include the undelivered brief")
	}
}

func TestUnsubmittedPasteDispatchNotifiesParent(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>", "ready>", "[Pasted text #1 +79 lines]", "[Pasted text #1 +79 lines]"}, hideEcho: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{
		ID: "up1", Pane: "%1", From: "parent", To: "kid", Text: "BRIEF-BODY",
		DeadlineUnix: 2000, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 1 || doneN != 1 || failed != 0 {
		t.Fatalf("unsubmitted paste pending=%d done=%d failed=%d", p, doneN, failed)
	}
	pending, _ := q.Pending()
	if len(pending) != 1 || pending[0].From != "broker" || pending[0].Pane != "%2" {
		t.Fatalf("parent notify: %+v", pending)
	}
	if strings.Contains(pending[0].Text, "BRIEF-BODY") {
		t.Fatal("unsubmitted notify must not include the child brief body")
	}
	if !strings.Contains(pending[0].Text, "kid") || !strings.Contains(pending[0].Text, "up1") {
		t.Fatalf("notify missing name/id: %s", pending[0].Text)
	}

	f.mu.Lock()
	f.captures = []string{"ready>"}
	f.capI = 0
	f.mu.Unlock()
	d.Tick()
	if f.injectCount() != 2 {
		t.Fatalf("want parent notify paste, got %d", f.injectCount())
	}
	if !strings.Contains(f.lastInject(), "dispatch unsubmitted") {
		t.Fatalf("parent paste=%q", f.lastInject())
	}
}

func TestBusyAfterPasteIsDoneNoRetry(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>", "esc to interrupt\nworking"}, hideEcho: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{
		ID: "u2", Pane: "%379", From: "parent", To: "muxa-presence",
		Text: "NEVER-VISIBLE-BODY", DeadlineUnix: 2000, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("want done not pending, pending=%d done=%d failed=%d", p, doneN, failed)
	}
	f.mu.Lock()
	f.captures = []string{"ready>"}
	f.capI = 0
	f.echo = ""
	f.hideEcho = false
	f.mu.Unlock()
	d.Tick()
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("retried a paste after the pane went idle: %d", f.injectCount())
	}
}

func TestParkedCursorCollapseFirstBriefNoRetry(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	// Production Cursor parks the hardware cursor on the blank row below
	// the splash footer. emptyAtCursor is true; drawing is false.
	f := &fakeTMUX{
		captures:        []string{"ready>", "splash footer\nplaceholder\n"},
		hideEcho:        true,
		parkAfterInject: "2 0",
	}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	brief := "[muxa] from=parent\nFirst brief that Cursor will collapse.\n"
	_ = q.Put(&Msg{
		ID: "c105", Pane: "%372", From: "parent", To: "muxa-darwin",
		Text:         brief,
		DeadlineUnix: 2000, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	// done/ for the first brief; inconclusive confirm — no parent notify.
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("parked-cursor first brief pending=%d done=%d failed=%d", p, doneN, failed)
	}
	f.mu.Lock()
	f.captures = []string{"ready>"}
	f.capI = 0
	f.parkCursor = ""
	f.parkAfterInject = ""
	f.echo = ""
	f.hideEcho = false
	f.cursor = ""
	f.mu.Unlock()
	d.Tick()
	d.Tick()
	if n := f.injectCountOf(brief); n != 1 {
		t.Fatalf("retried a parked-cursor first brief: %d", n)
	}
}

func TestDispatchParkedCursorMidJobNoRetry(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{
		captures:        []string{"ready>", "splash\nworking\n"},
		hideEcho:        true,
		parkAfterInject: "2 0",
	}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{
		ID: "pg1", Pane: "%1", From: "parent", To: "pipe-gate",
		Text: "FIRST-BRIEF", DeadlineUnix: 2000, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	// done/ for the first brief; inconclusive confirm — no parent notify.
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("after parked-cursor confirm pending=%d done=%d failed=%d", p, doneN, failed)
	}
	f.mu.Lock()
	f.captures = []string{"ready>", "ready>"}
	f.capI = 0
	f.parkCursor = ""
	f.parkAfterInject = ""
	f.echo = ""
	f.hideEcho = false
	f.cursor = ""
	f.mu.Unlock()
	d.Tick()
	d.Tick()
	if n := f.injectCountOf("FIRST-BRIEF"); n != 1 {
		t.Fatalf("re-pasted first brief while mid-job looked free: %d", n)
	}
}

func TestInjectErrorStaysPending(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}, failInj: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{
		ID: "e1", Pane: "%1", From: "c", To: "p", Text: "NOPE",
		DeadlineUnix: 2000, Kind: kindDispatch, ParentPane: "%2",
	})
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("failed inject counted: %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 1 || doneN != 0 || failed != 0 {
		t.Fatalf("inject error pending=%d done=%d failed=%d", p, doneN, failed)
	}
}

// muxa#116 made a missing `[muxa] from=` envelope mean "not delivered".
// muxa#129 keeps the envelope check as logging confidence and drops the
// retry: the pane reacted to a paste tmux accepted, so a second paste would
// deliver the same envelope twice.
func TestMailReactedWithoutEnvelopeNotRetried(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>", "esc to interrupt\nworking"}, hideEcho: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	text := formatOne("worker", "", "JOB-DONE REPORT")
	_ = q.Put(&Msg{ID: "m116a", Pane: "%1", From: "worker", To: "parent", Text: text, DeadlineUnix: 2000})
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("mail filed once tmux accepted the paste, pending=%d done=%d failed=%d", p, doneN, failed)
	}
	f.mu.Lock()
	f.captures = []string{"ready>", "ready>"}
	f.capI = 0
	f.echo = ""
	f.hideEcho = false
	f.mu.Unlock()
	d.Tick()
	d.Tick()
	if n := f.injectCountOf(text); n != 1 {
		t.Fatalf("re-pasted a report whose envelope never showed: %d", n)
	}
}

func TestMailHoldsComposerForeignInput(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	typed := "log\n▄▄▄▄▄\n operator-typed\n▀▀▀▀▀\n footer"
	f := &fakeTMUX{captures: []string{typed, typed}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := int64(1000)
	d.now = func() time.Time { return time.Unix(now, 0) }
	text := formatOne("worker", "", "REPORT")
	_ = q.Put(&Msg{ID: "m116b", Pane: "%1", From: "worker", To: "parent", Text: text, DeadlineUnix: 1005})
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted over foreign composer input: %d", f.injectCount())
	}
	now = 1005
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("timeout-pasted mail over foreign composer: %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 1 || doneN != 0 || failed != 0 {
		t.Fatalf("mail stays queued past deadline, pending=%d done=%d failed=%d", p, doneN, failed)
	}
}

func TestMailPreInjectComposerForeignBlocks(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	idle := "log\n▄▄▄▄▄\n → Plan, search, build anything\n▀▀▀▀▀\n footer"
	typed := "log\n▄▄▄▄▄\n operator-typed\n▀▀▀▀▀\n footer"
	f := &fakeTMUX{
		captures: []string{
			idle,
			idle,
			typed,
		},
		parkCursor: "2 20",
	}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	text := formatOne("worker", "", "report")
	_ = q.Put(&Msg{ID: "m116c", Pane: "%1", From: "worker", To: "parent", Text: text, DeadlineUnix: 2000})
	d.Tick()
	f.mu.Lock()
	f.parkCursor = "4 0"
	f.mu.Unlock()
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pre-inject composer gate must block stale free-detection: %d", f.injectCount())
	}
}

// muxa#127: a mid-turn Claude Code pane accepts a paste into its own input
// queue and echoes nothing — the payload is absent from Snapshot and from
// CaptureHistory, the composer row is empty, and the pane is not drawing.
// All three confirm guards then fall the wrong way at once and the message
// is left queued, so the next Tick pastes the same envelope a second time.
func TestClaudeConsumedPasteNotRetried(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}, hideEcho: true, kind: "claude"}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	text := formatOne("worker", "", "REPORT pr=https://example.invalid/pr/1")
	_ = q.Put(&Msg{ID: "m127", Pane: "%9", From: "worker", To: "crisp-lark", Text: text, DeadlineUnix: 2000})

	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("consumed paste pending=%d done=%d failed=%d", p, doneN, failed)
	}
	if a := doneAttempts(t, dir, "m127"); a != 1 {
		t.Fatalf("want attempts=1, got %d", a)
	}

	// The pane finishes its turn and goes idle; the envelope still never
	// appears in the capture. Nothing may re-paste it.
	f.mu.Lock()
	f.captures = []string{"ready>", "ready>"}
	f.capI = 0
	f.echo = ""
	f.mu.Unlock()
	d.Tick()
	d.Tick()
	if n := f.injectCountOf(text); n != 1 {
		t.Fatalf("re-pasted an envelope a claude pane had already consumed: %d", n)
	}
	if a := doneAttempts(t, dir, "m127"); a != 1 {
		t.Fatalf("attempts grew after retry ticks: %d", a)
	}
}

// muxa#128 spared only kind=claude. muxa#129 generalises it: a generic pane
// that never echoes the payload is also filed once, because the reason the
// broker cannot see the text is not something it can observe.
func TestGenericPaneGhostSendNotRetried(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}, hideEcho: true, kind: "generic"}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	text := formatOne("c", "", "GHOST")
	_ = q.Put(&Msg{ID: "g127", Pane: "%1", From: "c", To: "p", Text: text, DeadlineUnix: 2000})
	d.Tick()
	d.Tick()
	if n := f.injectCountOf(text); n != 1 {
		t.Fatalf("generic ghost send re-pasted: %d", n)
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("generic ghost send pending=%d done=%d failed=%d", p, doneN, failed)
	}
}

// The incident test. Both times an operator had to kill a CLI, one message
// had been pasted into one pane 16 and 18 times. The pane never confirmed;
// nothing else was wrong. Assert the exact paste count, for every kind —
// a ceiling alone would still have let this through N times (muxa#129).
func TestNeverConfirmingPaneGetsExactlyOnePaste(t *testing.T) {
	for _, kind := range []string{"claude", "cursor", "pi", "generic", ""} {
		name := kind
		if name == "" {
			name = "unregistered"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			q, _ := OpenQueue(dir)
			// hideEcho: the payload never reaches the capture or the
			// history, and the pane sits at a quiescent prompt — the exact
			// shape of muxa#127, and of every future CLI that consumes
			// pasted input.
			f := &fakeTMUX{captures: []string{"ready>"}, hideEcho: true, kind: kind}
			d := NewDeliverer(q, testTMUX(f), time.Millisecond)
			now := time.Unix(1000, 0)
			d.now = func() time.Time { return now }
			text := formatOne("worker", "", "REPORT pr=https://example.invalid/pr/1")
			_ = q.Put(&Msg{ID: "n129", Pane: "%9", From: "worker", To: "parent", Text: text, DeadlineUnix: 2000})

			for i := 0; i < 20; i++ {
				d.Tick()
				now = now.Add(time.Second)
			}
			if n := f.injectCountOf(text); n != 1 {
				t.Fatalf("%s pane: want exactly 1 paste, got %d", name, n)
			}
			if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
				t.Fatalf("%s pane: pending=%d done=%d failed=%d", name, p, doneN, failed)
			}
			if a := doneAttempts(t, dir, "n129"); a != 1 {
				t.Fatalf("%s pane: want attempts=1, got %d", name, a)
			}
		})
	}
}

// A dispatch brief into a never-confirming pane is already at-most-once
// (muxa#105); pin it alongside mail so the two paths cannot drift apart.
func TestNeverConfirmingPaneGetsExactlyOneBrief(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}, hideEcho: true, kind: "cursor"}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := time.Unix(1000, 0)
	d.now = func() time.Time { return now }
	_ = q.Put(&Msg{
		ID: "n129d", Pane: "%1", From: "parent", To: "kid", Text: "FIRST-BRIEF",
		DeadlineUnix: 2000, Kind: kindDispatch, ParentPane: "%2",
	})
	for i := 0; i < 20; i++ {
		d.Tick()
		now = now.Add(time.Second)
	}
	if n := f.injectCountOf("FIRST-BRIEF"); n != 1 {
		t.Fatalf("want exactly 1 brief paste, got %d", n)
	}
}

// muxa#128 left this window live: mail whose confirm resolves to
// confirmUnsubmitted stayed queued and re-pasted on the next Tick. A
// collapsed "[Pasted text …]" is a rendering fact, and Cursor parks one on
// screen long after the paste was taken (muxa#111/#105).
func TestMailUnsubmittedPasteNotRetried(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{
		captures: []string{"ready>", "ready>", "[Pasted text #1 +79 lines]", "[Pasted text #1 +79 lines]"},
		hideEcho: true, kind: "cursor",
	}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	text := formatOne("worker", "", "REPORT")
	_ = q.Put(&Msg{ID: "m129u", Pane: "%1", From: "worker", To: "parent", Text: text, DeadlineUnix: 2000})
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("unsubmitted mail pending=%d done=%d failed=%d", p, doneN, failed)
	}
	// Unlike dispatch, mail has no parent pane to warn; nothing is enqueued.
	if pending, _ := q.Pending(); len(pending) != 0 {
		t.Fatalf("mail must not enqueue a broker turn: %+v", pending)
	}
	f.mu.Lock()
	f.captures = []string{"ready>", "ready>"}
	f.capI = 0
	f.echo = ""
	f.hideEcho = false
	f.mu.Unlock()
	d.Tick()
	d.Tick()
	if n := f.injectCountOf(text); n != 1 {
		t.Fatalf("re-pasted mail that showed as an unsubmitted collapse: %d", n)
	}
}

// The seatbelt (muxa#129). Inject returning an error is observable and
// unambiguous — nothing was pasted — so it is the one path that may retry.
// It is bounded and backed off, and files failed/ when the ceiling trips.
func TestInjectFailureRetriesToCeilingThenFails(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}, failInj: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := time.Unix(1000, 0)
	d.now = func() time.Time { return now }
	_ = q.Put(&Msg{ID: "inj1", Pane: "%1", From: "c", To: "p", Text: formatOne("c", "", "NOPE"), DeadlineUnix: 9000})

	d.Tick()
	if a := queuedAttempts(t, dir, "pending", "inj1"); a != 1 {
		t.Fatalf("attempt 1: got %d", a)
	}
	// Inside the backoff window the poll loop must not re-run the command.
	d.Tick()
	d.Tick()
	if a := queuedAttempts(t, dir, "pending", "inj1"); a != 1 {
		t.Fatalf("retried inside the backoff window: attempts=%d", a)
	}
	now = now.Add(time.Second)
	d.Tick()
	if a := queuedAttempts(t, dir, "pending", "inj1"); a != 2 {
		t.Fatalf("attempt 2: got %d", a)
	}
	now = now.Add(2 * time.Second)
	d.Tick()
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 0 || failed != 1 {
		t.Fatalf("ceiling must file failed/, pending=%d done=%d failed=%d", p, doneN, failed)
	}
	if a := queuedAttempts(t, dir, "failed", "inj1"); a != maxInjectAttempts {
		t.Fatalf("want attempts=%d at the ceiling, got %d", maxInjectAttempts, a)
	}
	// Nothing was ever pasted, so nothing can be double-delivered.
	if f.injectCount() != 0 {
		t.Fatalf("failed inject counted as a paste: %d", f.injectCount())
	}
	now = now.Add(time.Minute)
	d.Tick()
	if p, _, failed, _ := q.Counts(); p != 0 || failed != 1 {
		t.Fatalf("kept retrying past the ceiling, pending=%d failed=%d", p, failed)
	}
}

// A dispatch brief tmux never accepted is a real loss, and the parent is
// told — the same shape as the deadline path, which already notifies.
func TestInjectFailureCeilingNotifiesDispatchParent(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}, failInj: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := time.Unix(1000, 0)
	d.now = func() time.Time { return now }
	_ = q.Put(&Msg{
		ID: "inj2", Pane: "%1", From: "parent", To: "kid", Text: "SECRET-BRIEF",
		DeadlineUnix: 9000, Kind: kindDispatch, ParentPane: "%2",
	})
	for i := 0; i < maxInjectAttempts; i++ {
		d.Tick()
		now = now.Add(time.Minute)
	}
	if _, _, failed, _ := q.Counts(); failed != 1 {
		t.Fatalf("brief not filed failed/: failed=%d", failed)
	}
	pending, _ := q.Pending()
	if len(pending) != 1 || pending[0].From != "broker" || pending[0].Pane != "%2" {
		t.Fatalf("parent notify: %+v", pending)
	}
	if strings.Contains(pending[0].Text, "SECRET-BRIEF") {
		t.Fatal("failure turn must not include the undelivered brief")
	}
	if !strings.Contains(pending[0].Text, "kid") || !strings.Contains(pending[0].Text, "inj2") {
		t.Fatalf("notify missing name/id: %s", pending[0].Text)
	}
}

func queuedAttempts(t *testing.T, dir, sub, id string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, sub, id+".json"))
	if err != nil {
		t.Fatalf("read %s/%s: %v", sub, id, err)
	}
	var m Msg
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s/%s: %v", sub, id, err)
	}
	return m.Attempts
}

func doneAttempts(t *testing.T, dir, id string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "done", id+".json"))
	if err != nil {
		t.Fatalf("read done/%s: %v", id, err)
	}
	var m Msg
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode done/%s: %v", id, err)
	}
	return m.Attempts
}
