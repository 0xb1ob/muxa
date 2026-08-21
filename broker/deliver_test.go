package main

import (
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
		if f.capI < len(f.captures) {
			s := f.captures[f.capI]
			f.capI++
			return s, nil
		}
		if len(f.captures) > 0 {
			return f.captures[len(f.captures)-1], nil
		}
		return "", nil
	case "load-buffer":
		if f.failInj {
			return "", errPaste
		}
		f.injects = append(f.injects, string(stdin))
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
	if p, doneN, failed, err := q.Counts(); err != nil || p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("counts pending=%d done=%d failed=%d err=%v", p, doneN, failed, err)
	}
}

func TestTimeoutFallbackPaste(t *testing.T) {
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
	if f.injectCount() != 1 {
		t.Fatalf("want fallback paste at deadline, got %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 1 || failed != 0 {
		t.Fatalf("after fallback pending=%d done=%d failed=%d", p, doneN, failed)
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
