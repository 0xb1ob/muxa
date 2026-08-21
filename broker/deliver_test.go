package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTMUX struct {
	mu       sync.Mutex
	dead     bool
	mode     bool
	captures []string
	capI     int
	injects  []string
	failInj  bool
	echo     string
	hideEcho bool
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
			return "0 0", nil
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
				return f.echo, nil
			}
			return f.echo + "\n" + s, nil
		}
		return s, nil
	case "load-buffer":
		if f.failInj {
			return "", errPaste
		}
		f.injects = append(f.injects, string(stdin))
		f.echo = string(stdin)
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
	_ = q.Put(&Msg{ID: "r1", Pane: "%1", From: "c", To: "p", Text: "TOKEN-RETRY", DeadlineUnix: 2000})

	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted while typing")
	}
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted on second busy capture")
	}
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 paste after free, got %d", f.injectCount())
	}
	pending, _ := q.Pending()
	if len(pending) != 0 {
		t.Fatalf("still pending: %+v", pending)
	}
	if f.lastInject() != "TOKEN-RETRY" {
		t.Fatalf("payload=%q", f.lastInject())
	}
	if p, doneN, failed, unknown, err := q.Counts(); err != nil || p != 0 || doneN != 1 || failed != 0 || unknown != 0 {
		t.Fatalf("counts pending=%d done=%d failed=%d unknown=%d err=%v", p, doneN, failed, unknown, err)
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
	if p, doneN, failed, unknown, _ := q.Counts(); p != 1 || doneN != 0 || failed != 0 || unknown != 0 {
		t.Fatalf("after deadline pending=%d done=%d failed=%d unknown=%d", p, doneN, failed, unknown)
	}
}

func TestUnconfirmedPasteNotDone(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}, hideEcho: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{ID: "u1", Pane: "%1", From: "c", To: "p", Text: "TOKEN-GHOST", DeadlineUnix: 2000})

	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("want 1 inject, got %d", f.injectCount())
	}
	if p, doneN, failed, unknown, _ := q.Counts(); p != 1 || doneN != 0 || failed != 0 || unknown != 0 {
		t.Fatalf("unconfirmed pending=%d done=%d failed=%d unknown=%d", p, doneN, failed, unknown)
	}
}

func TestBusyPaneDeliversInOrderAfterFree(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready> BLOCKED"}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := int64(1000)
	d.now = func() time.Time { return time.Unix(now, 0) }
	_ = q.Put(&Msg{ID: "a", Pane: "%1", From: "c", To: "p", Text: "first-mail", EnqueuedUnix: 1, DeadlineUnix: 1005})
	_ = q.Put(&Msg{ID: "b", Pane: "%1", From: "c", To: "p", Text: "second-mail", EnqueuedUnix: 2, DeadlineUnix: 1005})

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
	if f.injectCount() != 1 || f.lastInject() != "first-mail" {
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
	if f.injectCount() != 2 || f.lastInject() != "second-mail" {
		t.Fatalf("second paste=%q count=%d", f.lastInject(), f.injectCount())
	}
	if ids := d.pasteIDs(); len(ids) != 2 || ids[0] != "%1|a" || ids[1] != "%1|b" {
		t.Fatalf("paste order: %v", ids)
	}
	if p, doneN, failed, unknown, _ := q.Counts(); p != 0 || doneN != 2 || failed != 0 || unknown != 0 {
		t.Fatalf("after both pending=%d done=%d failed=%d unknown=%d", p, doneN, failed, unknown)
	}
}

func TestNoDoubleDelivery(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{ID: "d1", Pane: "%1", From: "c", To: "p", Text: "ONCE", DeadlineUnix: 2000})

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
	_ = q.Put(&Msg{ID: "a", Pane: "%1", From: "c", To: "p", Text: "first", DeadlineUnix: 2000})
	_ = q.Put(&Msg{ID: "b", Pane: "%1", From: "c", To: "p", Text: "second", DeadlineUnix: 2000})
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
	if f.injectCount() != 1 || f.lastInject() != "FIRST-BRIEF" {
		t.Fatalf("want brief after ready, got %q count=%d", f.lastInject(), f.injectCount())
	}
	if p, doneN, failed, unknown, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 || unknown != 0 {
		t.Fatalf("after ready pending=%d done=%d failed=%d unknown=%d", p, doneN, failed, unknown)
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
	if p, doneN, failed, unknown, _ := q.Counts(); doneN != 0 || failed != 1 || p != 1 || unknown != 0 {
		t.Fatalf("want brief failed + parent notify pending, pending=%d done=%d failed=%d unknown=%d", p, doneN, failed, unknown)
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

func TestCursorCollapsedPasteIsDelivered(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>", "[Pasted text +48 lines]"}, hideEcho: true}
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
	if p, doneN, failed, unknown, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 || unknown != 0 {
		t.Fatalf("collapsed paste pending=%d done=%d failed=%d unknown=%d", p, doneN, failed, unknown)
	}
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("retried a collapsed Cursor paste: %d", f.injectCount())
	}
}

func TestBusyAfterPasteIsUnknownNoRetry(t *testing.T) {
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
	if p, doneN, failed, unknown, _ := q.Counts(); p != 0 || doneN != 0 || failed != 0 || unknown != 1 {
		t.Fatalf("want unknown not pending, pending=%d done=%d failed=%d unknown=%d", p, doneN, failed, unknown)
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
		t.Fatalf("retried an unknown Cursor paste after the pane went idle: %d", f.injectCount())
	}
}

func TestPasteCollapsed(t *testing.T) {
	if !pasteCollapsed("[Pasted text +48 lines]") {
		t.Fatal("cursor collapse marker")
	}
	if pasteCollapsed("ready>") {
		t.Fatal("idle prompt is not a collapse")
	}
}
