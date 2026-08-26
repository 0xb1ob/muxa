package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// muxa#133: canPaste must name which gate refused, not return a bare false.
func TestCanPasteNamesRefusalReason(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)

	t.Run("foreign-composer", func(t *testing.T) {
		busy := "  count slowly\n ▄▄▄▄▄\n → Add a follow-up                ctrl+c to stop\n ▀▀▀▀▀\n  footer pad\n"
		f := &fakeTMUX{
			captures:   []string{busy},
			parkCursor: "4 0",
		}
		d := NewDeliverer(q, testTMUX(f), time.Millisecond)
		d.mu.Lock()
		d.prev["%1"] = busy
		d.mu.Unlock()
		free, reason, err := d.canPaste("%1")
		if err != nil {
			t.Fatal(err)
		}
		if free {
			t.Fatal("want refused")
		}
		if reason != gateForeignComposer {
			t.Fatalf("reason=%q want %q", reason, gateForeignComposer)
		}
	})

	t.Run("in-mode", func(t *testing.T) {
		f := &fakeTMUX{mode: true, captures: []string{"ready>"}}
		d := NewDeliverer(q, testTMUX(f), time.Millisecond)
		free, reason, err := d.canPaste("%1")
		if err != nil {
			t.Fatal(err)
		}
		if free || reason != gateInMode {
			t.Fatalf("free=%v reason=%q want in-mode", free, reason)
		}
	})

	t.Run("two-signal", func(t *testing.T) {
		f := &fakeTMUX{captures: []string{"ready> typing"}}
		d := NewDeliverer(q, testTMUX(f), time.Millisecond)
		free, reason, err := d.canPaste("%1")
		if err != nil {
			t.Fatal(err)
		}
		if free || reason != gateTwoSignal {
			t.Fatalf("free=%v reason=%q want two-signal", free, reason)
		}
	})
}

// muxa#133: refusals count gate checks separately from paste attempts.
func TestRefusalsDistinctFromAttempts(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{captures: []string{
		"ready> still typing",
		"ready> still typing",
		"ready>",
	}}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{ID: "r1", Pane: "%1", From: "c", To: "p", Text: "TOKEN", DeadlineUnix: 2000})

	d.Tick() // gated: two-signal or foreign
	d.Tick() // gated again
	pending, _ := q.Pending()
	var m *Msg
	for _, msg := range pending {
		if msg.ID == "r1" {
			m = msg
		}
	}
	if m == nil {
		t.Fatal("message missing")
	}
	if m.Attempts != 0 {
		t.Fatalf("attempts=%d want 0 while gated", m.Attempts)
	}
	if m.Refusals < 2 {
		t.Fatalf("refusals=%d want >=2 gate checks recorded", m.Refusals)
	}
	if m.LastGate == "" {
		t.Fatal("last_gate empty after refusal")
	}

	d.Tick() // quiescent frame pair (needs two matching free frames)
	d.Tick() // free → paste
	if f.injectCount() != 1 {
		t.Fatalf("want 1 paste after free, got %d", f.injectCount())
	}
	if a := doneAttempts(t, dir, "r1"); a != 1 {
		t.Fatalf("attempts=%d want 1 after paste", a)
	}
}

func mustMsg(t *testing.T, q *Queue, id string) *Msg {
	t.Helper()
	for _, m := range mustPending(t, q) {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("no message %s", id)
	return nil
}

// muxa#133: tmux errors on pane_dead must not read as pane-dead.
func TestPaneDeadTmuxErrorNotDead(t *testing.T) {
	f := &fakeTMUX{fmtErr: errNoPane}
	tm := testTMUX(f)
	dead, err := tm.PaneDead("%1")
	if err == nil {
		t.Fatal("want tmux error from PaneDead")
	}
	if dead {
		t.Fatal("tmux error must not report pane dead")
	}
}

func TestInModeTmuxErrorNotInMode(t *testing.T) {
	f := &fakeTMUX{fmtErr: errNoPane}
	tm := testTMUX(f)
	inMode, err := tm.InMode("%1")
	if err == nil {
		t.Fatal("want tmux error from InMode")
	}
	if inMode {
		t.Fatal("tmux error must not report in-mode")
	}
}

// muxa#133: broker status surfaces gated pending mail with named reasons.
func TestBrokerStatusSurfacesHeldWithReason(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("m%d.sock", os.Getpid()))
	t.Cleanup(func() { os.Remove(sock) })
	busy := "  count slowly\n ▄▄▄▄▄\n → Add a follow-up                ctrl+c to stop\n ▀▀▀▀▀\n  footer pad\n"
	tmux := testTMUX(&fakeTMUX{
		captures:   []string{busy},
		parkCursor: "4 0",
	})
	d := NewDeliverer(q, tmux, time.Millisecond)
	d.mu.Lock()
	d.prev["%1"] = busy
	d.mu.Unlock()
	d.now = func() time.Time { return time.Unix(1000, 0) }
	s := &Server{Sock: sock, Q: q, Deadline: time.Minute, Held: d.HeldEntries}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	_ = q.Put(&Msg{ID: "s1", Pane: "%1", From: "c", To: "worker", Text: "hi", EnqueuedUnix: 990, DeadlineUnix: 2000})
	d.Tick()

	st := rpc(t, sock, Request{Op: "status"})
	if !st.OK {
		t.Fatalf("status: %+v", st)
	}
	if len(st.Held) != 1 {
		t.Fatalf("held=%+v want 1 gated entry", st.Held)
	}
	h := st.Held[0]
	if h.ID != "s1" || h.To != "worker" || h.Pane != "%1" {
		t.Fatalf("held entry: %+v", h)
	}
	if h.Reason != gateForeignComposer {
		t.Fatalf("reason=%q want %q", h.Reason, gateForeignComposer)
	}
	if h.Refusals < 1 {
		t.Fatalf("refusals=%d want >=1", h.Refusals)
	}
}

// muxa#133: held-past-deadline alert uses the real gate, not not-free.
func TestHeldAlertUsesNamedGate(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{
		captures: []string{"ready> never clears"},
		roster: []rosterEntry{
			{Pane: "%2", Name: "parent"},
			{Pane: "%1", Name: "stuck"},
		},
	}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := int64(1000)
	d.now = func() time.Time { return time.Unix(now, 0) }
	_ = q.Put(&Msg{ID: "h1", Pane: "%1", From: "parent", To: "stuck", Text: "TOKEN-HELD", DeadlineUnix: 1005, EnqueuedUnix: 900})

	d.Tick()
	now = 1005
	d.Tick()

	var alert *Msg
	for _, m := range mustPending(t, q) {
		if m.From == "broker" && m.ID != "h1" {
			alert = m
		}
	}
	if alert == nil {
		t.Fatal("want broker held alert")
	}
	if strings.Contains(alert.Text, "gate=not-free") {
		t.Fatalf("alert still uses placeholder gate: %s", alert.Text)
	}
	if !strings.Contains(alert.Text, "gate="+gateTwoSignal) && !strings.Contains(alert.Text, "gate="+gateForeignComposer) {
		t.Fatalf("alert missing named gate: %s", alert.Text)
	}
	if !strings.Contains(alert.Text, "refusals=") {
		t.Fatalf("alert missing refusals: %s", alert.Text)
	}
}
