package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseOutputLine(t *testing.T) {
	cases := []struct {
		line string
		pane string
		ok   bool
	}{
		{"%output %0 tick-1\\015\\012", "%0", true},
		{"%output %12 \\033[Hhello", "%12", true},
		{"%begin 1 2 0", "", false},
		{"%subscription-changed : 8,33,bash", "", false},
		{"%exit", "", false},
		{"%output", "", false},
		{"%output %", "", false},
	}
	for _, c := range cases {
		pane, ok := parseOutputLine(c.line)
		if ok != c.ok || pane != c.pane {
			t.Errorf("%q → pane=%q ok=%v, want %q %v", c.line, pane, ok, c.pane, c.ok)
		}
	}
}

func TestParseTmuxVersion(t *testing.T) {
	maj, min, ok := parseTmuxVersion("tmux 3.6a")
	if !ok || maj != 3 || min != 6 {
		t.Fatalf("3.6a → %d.%d ok=%v", maj, min, ok)
	}
	maj, min, ok = parseTmuxVersion("tmux 3.1")
	if !ok || maj != 3 || min != 1 {
		t.Fatalf("3.1 → %d.%d ok=%v", maj, min, ok)
	}
	if controlModeOK(3, 1) {
		t.Fatal("3.1 should not support control-mode subscriptions we need")
	}
	if !controlModeOK(3, 2) || !controlModeOK(3, 6) || !controlModeOK(4, 0) {
		t.Fatal("3.2+ should be ok")
	}
}

func controlModeOK(maj, min int) bool {
	return maj > controlMinMajor || (maj == controlMinMajor && min >= controlMinMinor)
}

func TestConsumeControlNotesPanesAndExit(t *testing.T) {
	h := NewControlHub(nil, 20*time.Millisecond)
	r := strings.NewReader("%begin 1 2 0\n%end 1 2 0\n%output %3 hello\n%output %4 more\n%exit\n")
	err := consumeControl(r, h, make(chan struct{}))
	if err != errControlExit {
		t.Fatalf("err=%v", err)
	}
	if !h.Drawing("%3") || !h.Drawing("%4") {
		t.Fatalf("expected both panes drawing, lastDraw=%v", h.lastDraw)
	}
	time.Sleep(40 * time.Millisecond)
	if h.Drawing("%3") {
		t.Fatal("quiet window should have elapsed")
	}
}

func TestDrawingPanesRequiresAStream(t *testing.T) {
	h := NewControlHub(nil, 50*time.Millisecond)
	h.note("%1")
	h.note("%1")
	if panes := h.DrawingPanes(); len(panes) != 0 {
		t.Fatalf("prompt burst should not be drawing: %v", panes)
	}
	time.Sleep(60 * time.Millisecond)
	h.note("%1")
	panes := h.DrawingPanes()
	if len(panes) != 1 || panes[0] != "%1" {
		t.Fatalf("streaming pane should be drawing: %v", panes)
	}
}

func TestDrawingBlocksPaste(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{"ready>"}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	h := NewControlHub(nil, time.Second)
	h.setLive(true)
	h.note("%1")
	d.Ctrl = h
	_ = q.Put(&Msg{ID: "g1", Pane: "%1", From: "c", To: "p", Text: "TOKEN-DRAW", DeadlineUnix: 2000})
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted while %%output was flowing: %d", f.injectCount())
	}
}
