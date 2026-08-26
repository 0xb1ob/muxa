package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// captureLog redirects the standard logger for one test and hands back the
// buffer. Line counts are the point of muxa#124, so flags are cleared to
// keep one log call at exactly one line.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	out, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(out)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	})
	return &buf
}

func logLines(buf *bytes.Buffer) []string {
	s := strings.TrimRight(buf.String(), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func countLinesWith(buf *bytes.Buffer, sub string) int {
	n := 0
	for _, l := range logLines(buf) {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// muxa#124: tmux reports a pane id that no longer exists by exiting 0 from
// display-message and expanding every pane format to the empty string.
// Reading "" as "alive" is what let a closed pane fall through to the
// free-check and hot-spin there.
func TestPaneDeadClosedPaneID(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	sock := fmt.Sprintf("mdead%d", os.Getpid())
	defer func() { _ = exec.Command("tmux", "-L", sock, "kill-server").Run() }()
	if err := exec.Command("tmux", "-L", sock, "new-session", "-d", "-s", "dp",
		"-x", "80", "-y", "12", "-n", "p", "sleep 300").Run(); err != nil {
		t.Fatal(err)
	}
	tm := &TMUX{Bin: "tmux", Socket: sock}
	tm.Run = tm.exec

	out, err := tm.Run([]string{"split-window", "-t", "dp", "-P", "-F", "#{pane_id}", "sleep 300"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := strings.TrimSpace(out)
	if closed == "" {
		t.Fatal("no pane id from split-window")
	}
	if tm.PaneDead(closed) {
		t.Fatalf("live pane %s reported dead", closed)
	}
	if err := tm.KillPane(closed); err != nil {
		t.Fatal(err)
	}
	// The id is gone from the server, not merely marked dead.
	for i := 0; i < 50; i++ {
		if _, err := tm.Capture(closed); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := tm.Capture(closed); err == nil {
		t.Fatalf("pane %s still capturable after kill-pane", closed)
	}
	if !tm.PaneDead(closed) {
		v, err := tm.fmt(closed, "#{pane_dead}")
		t.Fatalf("closed pane %s not reported dead (pane_dead=%q err=%v)", closed, v, err)
	}
	if tm.PaneDead("%0") {
		t.Fatal("surviving pane %0 reported dead")
	}
}

// The same fact without a tmux server: an empty pane_dead is a pane that is
// gone, not a pane that is alive.
func TestPaneDeadEmptyFormatIsGone(t *testing.T) {
	f := &fakeTMUX{gone: true}
	if !testTMUX(f).PaneDead("%1") {
		t.Fatal("empty #{pane_dead} read as alive")
	}
	if testTMUX(&fakeTMUX{}).PaneDead("%1") {
		t.Fatal("live pane read as dead")
	}
}

// muxa#124 regression: a message to a pane that no longer exists must reach
// failed/ with one drop line, and must not log per tick. This spun at ~8
// lines/second for 8 hours and wrote a 22 MB broker.log.
func TestClosedPaneMessageFailsWithBoundedLog(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{gone: true}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := int64(1000)
	d.now = func() time.Time { return time.Unix(now, 0) }
	_ = q.Put(&Msg{ID: "g1", Pane: "%1", From: "c", To: "p", Text: "TOKEN-GONE", DeadlineUnix: 1600})

	buf := captureLog(t)
	// Before the deadline: the pane is unreachable but the message is not
	// yet expired. Ticking at 250 ms for the whole 600 s window is 2400
	// ticks; a per-tick log line is the 22 MB.
	for i := 0; i < 2400; i++ {
		d.Tick()
		now += 1
		if now >= 1600 {
			now = 1599
		}
	}
	if p, doneN, failed, _ := q.Counts(); p != 1 || doneN != 0 || failed != 0 {
		t.Fatalf("before deadline pending=%d done=%d failed=%d", p, doneN, failed)
	}
	if n := len(logLines(buf)); n > 4 {
		t.Fatalf("hot-spinning log before deadline: %d lines\n%s", n, buf.String())
	}

	now = 1600
	for i := 0; i < 100; i++ {
		d.Tick()
		now++
	}
	if p, doneN, failed, _ := q.Counts(); p != 0 || doneN != 0 || failed != 1 {
		t.Fatalf("after deadline pending=%d done=%d failed=%d", p, doneN, failed)
	}
	if n := countLinesWith(buf, "drop g1"); n != 1 {
		t.Fatalf("want exactly 1 drop line, got %d\n%s", n, buf.String())
	}
	if n := len(logLines(buf)); n > 6 {
		t.Fatalf("unbounded log for one undeliverable message: %d lines\n%s", n, buf.String())
	}
	if f.injectCount() != 0 {
		t.Fatalf("pasted into a pane that does not exist: %d", f.injectCount())
	}
}

// muxa#124 defect 3, independent of the pane-dead fix: a delivery error
// that repeats on every tick must be rate-limited, not logged per tick.
// Here the pane exists and answers pane_dead, but capture-pane keeps
// failing, so the free-check errors forever.
func TestRepeatedDeliveryErrorIsRateLimited(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{capErr: errNoPane}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := time.Unix(1000, 0)
	d.now = func() time.Time { return now }
	_ = q.Put(&Msg{ID: "e1", Pane: "%1", From: "c", To: "p", Text: "TOKEN-ERR", DeadlineUnix: 9000})

	buf := captureLog(t)
	for i := 0; i < 1000; i++ {
		d.Tick()
		now = now.Add(10 * time.Millisecond)
	}
	first := len(logLines(buf))
	if first == 0 {
		t.Fatal("a persistent delivery error was never logged at all")
	}
	if first > 3 {
		t.Fatalf("free-check error logged %d times in 10s of ticks\n%s", first, buf.String())
	}
	// Long after the window, the operator hears about it again — once.
	now = now.Add(10 * time.Minute)
	for i := 0; i < 100; i++ {
		d.Tick()
		now = now.Add(10 * time.Millisecond)
	}
	if n := len(logLines(buf)); n <= first {
		t.Fatalf("error never re-logged after the window: %d lines", n)
	} else if n > first+2 {
		t.Fatalf("error re-logged %d times after the window\n%s", n-first, buf.String())
	}
}
