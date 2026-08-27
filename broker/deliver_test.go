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
	gone            bool     // pane id no longer exists (muxa#124)
	fmtErr          pasteErr // if set, display-message keeps failing (muxa#133)
	roster          []rosterEntry
	capErr          pasteErr // if set, capture-pane keeps failing
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
	afterInject     func() // test hook: runs after a successful Inject
}

func (f *fakeTMUX) runner(args []string, stdin []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "list-panes":
		if len(f.roster) == 0 {
			return "", nil
		}
		var lines []string
		for _, r := range f.roster {
			lines = append(lines, strings.Join([]string{
				r.Pane, r.Name, r.ID, r.Parent, r.Kind,
				r.Where, r.Cwd, r.Cmd,
			}, rosterSep))
		}
		return strings.Join(lines, "\n"), nil
	case "display-message":
		if f.fmtErr != "" {
			return "", f.fmtErr
		}
		if f.gone {
			// Real tmux resolves display-message with no pane in scope,
			// exits 0, and expands every pane format to empty (muxa#124).
			return "", nil
		}
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
		if f.gone {
			return "", errNoPane
		}
		if f.capErr != "" {
			return "", f.capErr
		}
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
		if f.afterInject != nil {
			f.afterInject()
		}
		return "", nil
	case "paste-buffer":
		if f.gone {
			return "", errNoPane
		}
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

// errNoPane is what tmux says about a pane id that no longer exists
// (muxa#124). display-message does not report it; capture-pane does.
const errNoPane pasteErr = "exit status 1: can't find pane: %1"

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

// muxa#125: past-deadline mail left queued must tell the sender once, with
// attempts and the refusing gate — not only a log line nobody reads.
func TestHeldPastDeadlineNotifiesSender(t *testing.T) {
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
	if f.injectCount() != 0 {
		t.Fatalf("timeout-pasted held mail: %d", f.injectCount())
	}
	pending, _ := q.Pending()
	var alert *Msg
	for _, m := range pending {
		if m.From == "broker" && m.ID != "h1" {
			alert = m
		}
	}
	if alert == nil {
		t.Fatal("want broker held alert enqueued for sender, got none")
	}
	if alert.Pane != "%2" {
		t.Fatalf("alert pane=%q want %%2", alert.Pane)
	}
	if !strings.Contains(alert.Text, "past deadline") {
		t.Fatalf("alert text: %s", alert.Text)
	}
	if !strings.Contains(alert.Text, "stuck") || !strings.Contains(alert.Text, "pane=%1") {
		t.Fatalf("alert missing target: %s", alert.Text)
	}
	if !strings.Contains(alert.Text, "attempts=0") {
		t.Fatalf("alert missing attempts: %s", alert.Text)
	}
	if !strings.Contains(alert.Text, "refusals=") {
		t.Fatalf("alert missing refusals: %s", alert.Text)
	}
	if strings.Contains(alert.Text, "gate=not-free") {
		t.Fatalf("alert still uses placeholder gate: %s", alert.Text)
	}
	if !strings.Contains(alert.Text, "gate=") {
		t.Fatalf("alert missing named gate: %s", alert.Text)
	}

	f.mu.Lock()
	f.captures = []string{"ready>"}
	f.capI = 0
	f.echo = ""
	f.mu.Unlock()
	d.Tick()
	if f.injectCount() != 1 || !strings.Contains(f.lastInject(), "past deadline") {
		t.Fatalf("parent alert paste=%q count=%d", f.lastInject(), f.injectCount())
	}
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("held alert repeated: %d pastes", f.injectCount())
	}
}

func TestHeldAlertCoalescedAcrossTargets(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{
		captures: []string{"ready> blocked", "ready> blocked"},
		roster: []rosterEntry{
			{Pane: "%9", Name: "parent"},
			{Pane: "%1", Name: "brave-grove"},
			{Pane: "%2", Name: "noble-sparrow"},
		},
	}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := int64(1000)
	d.now = func() time.Time { return time.Unix(now, 0) }
	_ = q.Put(&Msg{ID: "a1", Pane: "%1", From: "parent", To: "brave-grove", Text: "one", DeadlineUnix: 1005, EnqueuedUnix: 900})
	_ = q.Put(&Msg{ID: "a2", Pane: "%2", From: "parent", To: "noble-sparrow", Text: "two", DeadlineUnix: 1005, EnqueuedUnix: 901})

	now = 1005
	d.Tick()
	var alerts []*Msg
	for _, m := range mustPending(t, q) {
		if m.From == "broker" {
			alerts = append(alerts, m)
		}
	}
	if len(alerts) != 1 {
		t.Fatalf("want 1 coalesced alert, got %d: %+v", len(alerts), alerts)
	}
	body := alerts[0].Text
	if !strings.Contains(body, "2 messages") {
		t.Fatalf("coalesce count: %s", body)
	}
	if !strings.Contains(body, "brave-grove") || !strings.Contains(body, "noble-sparrow") {
		t.Fatalf("coalesce targets: %s", body)
	}
}

func mustPending(t *testing.T, q *Queue) []*Msg {
	t.Helper()
	p, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	return p
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

// muxa#126: a human keystroke during the confirm window emits %output and
// changes the capture. Neither is proof the brief submitted; the parent must
// still be told when the collapsed placeholder remains after quiescence.
func TestDispatchUnsubmittedNotifiesDespiteMidConfirmActivity(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	f := &fakeTMUX{
		captures: []string{
			"ready>",
			"ready>",
			"[Pasted text #1 +79 lines]",
			"→ x\n[Pasted text #1 +79 lines]",
			"[Pasted text #1 +79 lines]",
		},
		hideEcho: true,
	}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.T.Delay = 5 * time.Millisecond
	h := NewControlHub(nil, 10*time.Millisecond)
	h.setLive(true)
	f.afterInject = func() { h.note("%1") }
	d.Ctrl = h
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{
		ID: "u126", Pane: "%1", From: "parent", To: "kid", Text: "BRIEF-BODY",
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
	if !strings.Contains(pending[0].Text, "dispatch unsubmitted") {
		t.Fatalf("notify text: %s", pending[0].Text)
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

func TestMailHoldsCursorParentComposerTyping(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	// muxa#139: dim placeholder substring remains while operator text is non-faint.
	typing := "log\n▄▄▄▄▄\n" +
		"\x1b[48;2;38;38;38m \x1b[2m→ Plan, search, build anything\x1b[0moperator-typed\x1b[0m\n" +
		"▀▀▀▀▀\n footer"
	f := &fakeTMUX{captures: []string{typing, typing}, kind: "cursor"}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	text := formatOne("worker", "", "REPORT")
	_ = q.Put(&Msg{ID: "m139", Pane: "%1", From: "worker", To: "parent", Text: text, DeadlineUnix: 1005})
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted over cursor parent composer typing: %d", f.injectCount())
	}
	if p, doneN, failed, _ := q.Counts(); p != 1 || doneN != 0 || failed != 0 {
		t.Fatalf("mail stays queued, pending=%d done=%d failed=%d", p, doneN, failed)
	}
}

// muxa#141: a cold cursor worker draws a reverse-video caret on the first
// character of its placeholder, and SGR 7 arrives with a reset that clears
// faint. Reading that cell as typed text held every dispatch at
// last_gate=foreign-composer with attempts=0 until the deadline, so a freshly
// spawned cursor worker never received its brief.
func TestDispatchPastesIntoColdCursorComposerCaret(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	cold := "\n\n  Cursor Agent\n" +
		" \x1b[38;2;38;38;38m▄▄▄▄▄\x1b[39m\n" +
		" \x1b[48;2;38;38;38m \x1b[2m→ \x1b[0;7m\x1b[48;2;38;38;38mP\x1b[0;2m\x1b[48;2;38;38;38mlan, search, build anything\x1b[0m\x1b[48;2;38;38;38m   \x1b[49m\n" +
		" \x1b[38;2;38;38;38m▀▀▀▀▀\x1b[39m\n" +
		"  \x1b[2mComposer 2.5 Fast\x1b[0m\n"
	// Cursor leaves the hardware cursor on the blank row below its footer
	// (the muxa#44/#79 hole two-signal accepts), so the composer gate is the
	// only thing standing between this pane and the brief.
	f := &fakeTMUX{captures: []string{cold, cold}, kind: "cursor", parkCursor: "7 0"}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	text := formatOne("parent", "", "BRIEF")
	_ = q.Put(&Msg{ID: "m141", Pane: "%1", From: "parent", To: "worker", Text: text, DeadlineUnix: 1600, Kind: "dispatch", ParentPane: "%0"})
	d.Tick()
	if f.injectCount() != 1 {
		t.Fatalf("dispatch must paste into an empty cold cursor composer, injects=%d", f.injectCount())
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

// claudeDraftPane and claudeIdlePane are the two frames muxa#147 turns on:
// a Claude Code composer holding an operator draft, and the same composer
// empty. Both are quiescent, and in both the hardware cursor is parked at
// the head of the composer row — where it lands when the operator arrows or
// Ctrl-A back into what they are typing. emptyAtCursor calls that row empty
// (it ends at the ❯ prompt marker), so two-signal says free either way and
// the composer gate is the only thing that can tell them apart.
const claudeDraftRow = "\x1b[39m❯ please review the auth module and tell me"
const claudeIdleRow = "\x1b[39m❯ "
const claudeRuleLine = "\x1b[38;5;244m────────────────────────────────────────"

func claudeFrame(row string) string {
	return "  prior turn output\n\n" +
		claudeRuleLine + "\n" + row + "\n" + claudeRuleLine + "\n" +
		"\x1b[39m  ~/w/muxa (main) | Opus 5 | Context: 4.0%\x1b[39m\n"
}

// claudeCursorHome is "#{cursor_y} #{cursor_x}" for the composer row of
// claudeFrame: line 3, two cells in, just past "❯ ".
const claudeCursorHome = "3 2"

// muxa#147, muxa send path. The operator is composing in their root pane;
// a worker mails a result in. The draft must survive and the mail must stay
// queued.
func TestSendHoldsForClaudeComposerDraft(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	draft := claudeFrame(claudeDraftRow)
	f := &fakeTMUX{captures: []string{draft, draft, draft, draft}, cursor: claudeCursorHome, kind: "claude"}
	// The repro only means anything if two-signal would have let this
	// through; assert that before asserting the gate catches it.
	if !TwoSignalFree(draft, draft, 3, 2) {
		t.Fatal("fixture no longer reproduces: two-signal already refuses this frame")
	}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{ID: "cd1", Pane: "%1", From: "worker", To: "root", Text: "TOKEN-DRAFT", DeadlineUnix: 2000})

	for i := 0; i < 4; i++ {
		d.Tick()
	}
	if f.injectCount() != 0 {
		t.Fatalf("pasted over a claude composer draft: %q", f.lastInject())
	}
	pending, _ := q.Pending()
	if len(pending) != 1 {
		t.Fatalf("mail must stay queued, not drop: pending=%d", len(pending))
	}
	if pending[0].LastGate != gateForeignComposer {
		t.Fatalf("gate=%q want %q", pending[0].LastGate, gateForeignComposer)
	}
}

// The other half of the contract: once the draft is gone the same mail must
// land. A gate that never reopens is a silent drop with extra steps.
func TestSendDeliversOnceClaudeComposerClears(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	draft := claudeFrame(claudeDraftRow)
	idle := claudeFrame(claudeIdleRow)
	f := &fakeTMUX{captures: []string{draft, draft, idle, idle, idle}, cursor: claudeCursorHome, kind: "claude"}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	_ = q.Put(&Msg{ID: "cd2", Pane: "%1", From: "worker", To: "root", Text: "TOKEN-CLEARED", DeadlineUnix: 2000})

	for i := 0; i < 5 && f.injectCount() == 0; i++ {
		d.Tick()
	}
	if f.injectCount() != 1 {
		t.Fatalf("want 1 paste after the draft cleared, got %d", f.injectCount())
	}
	if f.lastInject() != "TOKEN-CLEARED" {
		t.Fatalf("payload=%q", f.lastInject())
	}
	if pending, _ := q.Pending(); len(pending) != 0 {
		t.Fatalf("still pending: %+v", pending)
	}
}

// muxa#147, dispatch path. The same gate already guarded first briefs — for
// Cursor panes only. A Claude pane with foreign input must reach the same
// "dispatch refused" outcome rather than clobbering the draft.
func TestDispatchRefusedForClaudeComposerDraft(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	draft := claudeFrame(claudeDraftRow)
	f := &fakeTMUX{captures: []string{draft}, cursor: claudeCursorHome, kind: "claude"}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	now := int64(1000)
	d.now = func() time.Time { return time.Unix(now, 0) }
	d.drew["%1"] = true
	_ = q.Put(&Msg{ID: "cd3", Pane: "%1", From: "root", To: "worker", Text: "BRIEF", Kind: kindDispatch,
		ParentPane: "%2", DeadlineUnix: 1005, EnqueuedUnix: 900})

	d.Tick()
	now = 1005
	d.Tick()
	if f.injectCount() != 0 {
		t.Fatalf("pasted a brief over a claude composer draft: %q", f.lastInject())
	}
	pending, _ := q.Pending()
	var notice *Msg
	for _, m := range pending {
		if m.From == "broker" {
			notice = m
		}
		if m.ID == "cd3" {
			t.Fatal("refused dispatch must not stay pending")
		}
	}
	if notice == nil {
		t.Fatal("want a dispatch-refused notice to the parent")
	}
	if !strings.Contains(notice.Text, "composer holds foreign input") {
		t.Fatalf("notice text: %s", notice.Text)
	}
}

// Do not regress worker delivery: an idle Claude worker must take its first
// brief on the first free frame pair, with no extra gate in the way.
func TestDispatchDeliversToIdleClaudeComposer(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	idle := claudeFrame(claudeIdleRow)
	f := &fakeTMUX{captures: []string{idle, idle, idle}, cursor: claudeCursorHome, kind: "claude"}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	d.drew["%1"] = true
	_ = q.Put(&Msg{ID: "cd4", Pane: "%1", From: "root", To: "worker", Text: "BRIEF-IDLE", Kind: kindDispatch,
		ParentPane: "%2", DeadlineUnix: 2000})

	for i := 0; i < 3 && f.injectCount() == 0; i++ {
		d.Tick()
	}
	if f.injectCount() != 1 {
		t.Fatalf("idle claude composer must take the brief, injects=%d", f.injectCount())
	}
}
