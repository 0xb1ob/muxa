package main

import (
	"strings"
	"testing"
	"time"
)

// Verbatim `capture-pane -e` rows from cursor-agent v2026.08.11, post-turn
// composer, trailing pad kept. The two rows differ only by the reverse-video
// caret parked on the "A" — the blink muxa#144 is about.
const (
	cursorFollowUpCaret = " \x1b[48;2;242;242;242m \x1b[2m→ \x1b[0;7m\x1b[48;2;242;242;242mA\x1b[0;2m\x1b[48;2;242;242;242mdd a follow-up\x1b[0m\x1b[48;2;242;242;242m    \x1b[49m"
	cursorFollowUpBare  = " \x1b[48;2;242;242;242m \x1b[2m→ Add a follow-up\x1b[0m\x1b[48;2;242;242;242m    \x1b[49m"
)

func cursorFrame(row string) string {
	return "  the day starts in green\n" +
		" \x1b[38;2;242;242;242m▄▄▄▄▄\x1b[39m\n" +
		row + "\n" +
		" \x1b[38;2;242;242;242m▀▀▀▀▀\x1b[39m\n" +
		"  \x1b[2mCursor Grok 4.6 High\x1b[0m\n" +
		"\n"
}

// muxa#144: the caret blinks between frames of a pane that is doing nothing.
// Comparing raw capture bytes called that animation, so quiescence reset on
// nearly every poll and mail sat at last_gate=two-signal for thousands of
// refusals.
func TestSameFrameCursorCaretBlink(t *testing.T) {
	caret := cursorFrame(cursorFollowUpCaret)
	bare := cursorFrame(cursorFollowUpBare)
	if caret == bare {
		t.Fatal("frames must differ in bytes, or this proves nothing")
	}
	if !sameFrame(caret, bare) {
		t.Fatal("caret blink is not animation (muxa#144)")
	}
	if !sameFrame(bare, caret) {
		t.Fatal("sameFrame must not depend on which frame carries the caret")
	}
	// The hardware cursor parks on the blank row below the box, as cursor-agent
	// leaves it (muxa#44), so the empty-at-cursor half passes either way.
	cy := 5
	if !TwoSignalFree(caret, bare, cy, 0) || !TwoSignalFree(bare, caret, cy, 0) {
		t.Fatal("a static cursor pane whose caret blinked is FREE (muxa#144)")
	}
}

// The blink exemption is only for the caret. Everything a busy agent actually
// animates still breaks quiescence.
func TestSameFrameRealAnimationStillWaits(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{
			"elapsed timer",
			cursorFrame(cursorFollowUpCaret) + " Generating… 11s\n",
			cursorFrame(cursorFollowUpBare) + " Generating… 12s\n",
		},
		{
			"spinner glyph",
			"  ⠋ thinking\n" + cursorFrame(cursorFollowUpCaret),
			"  ⠙ thinking\n" + cursorFrame(cursorFollowUpBare),
		},
		{
			"colour sweep, same text",
			"  \x1b[31mworking\x1b[0m\n" + cursorFrame(cursorFollowUpCaret),
			"  \x1b[32mworking\x1b[0m\n" + cursorFrame(cursorFollowUpCaret),
		},
		{
			"faint text turns normal weight",
			"  \x1b[2mdone\x1b[0m\n" + cursorFrame(cursorFollowUpBare),
			"  done\n" + cursorFrame(cursorFollowUpBare),
		},
		{
			"row order changes",
			"  first\n  second\n" + cursorFrame(cursorFollowUpCaret),
			"  second\n  first\n" + cursorFrame(cursorFollowUpCaret),
		},
	}
	for _, c := range cases {
		if sameFrame(c.a, c.b) {
			t.Fatalf("%s must break quiescence", c.name)
		}
		if TwoSignalFree(c.a, c.b, 5, 0) {
			t.Fatalf("%s must WAIT", c.name)
		}
	}
}

// A reverse run wider than one cell is content — a selection, a highlight —
// so it still breaks quiescence when it appears. Same line muxa#141 draws in
// the composer gate.
func TestSameFrameMultiCellReverseIsContent(t *testing.T) {
	plain := "\x1b[2m→ \x1b[0;2mPICKED\x1b[0m"
	selected := "\x1b[2m→ \x1b[0;7mPICKED\x1b[0;2m\x1b[0m"
	if sameFrame(plain, selected) {
		t.Fatal("a multi-cell reverse run is content, not a caret")
	}
	caret := "\x1b[2m→ \x1b[0;7mP\x1b[0;2mICKED\x1b[0m"
	if !sameFrame(plain, caret) {
		t.Fatal("one reverse cell is a caret")
	}
}

// Claude Code parks a reverse-video block past the last visible rune of an
// empty input row. tmux trims trailing default-attribute blanks but keeps the
// styled caret cell, so the frame with the caret is one cell longer — a length
// difference, not a content one.
func TestSameFrameTrailingCaretCell(t *testing.T) {
	bare := "› "
	caret := "› \x1b[7m \x1b[0m"
	if !sameFrame(bare, caret) {
		t.Fatal("a caret parked on trailing pad is not content (muxa#144)")
	}
	typed := "› \x1b[7mx\x1b[0m"
	if sameFrame(bare, typed) {
		t.Fatal("a visible rune under the caret is content")
	}
}

// Truecolor parameters must not be read as top-level SGR codes: 48;2;r;g;b
// carries a literal 2, and taking it as faint would let a faint/normal
// difference elsewhere on the row compare equal.
func TestSameFrameTruecolorSubparamsNotAttributes(t *testing.T) {
	colored := "\x1b[48;2;38;38;38mtext\x1b[49m"
	faint := "\x1b[48;2;38;38;38m\x1b[2mtext\x1b[0m"
	if sameFrame(colored, faint) {
		t.Fatal("48;2 must not set faint")
	}
	other := "\x1b[48;2;242;242;242mtext\x1b[49m"
	if sameFrame(colored, other) {
		t.Fatal("a background change is a real change")
	}
}

func TestSameFrameIdenticalAndEmpty(t *testing.T) {
	f := cursorFrame(cursorFollowUpCaret)
	if !sameFrame(f, f) {
		t.Fatal("a frame equals itself")
	}
	if !sameFrame("", "") {
		t.Fatal("empty frames are equal")
	}
	if sameFrame("", f) {
		t.Fatal("empty is not the same picture as a drawn pane")
	}
	// TwoSignalFree still refuses the first observation whatever the key says.
	if TwoSignalFree("", "", 0, 0) {
		t.Fatal("no previous capture must WAIT")
	}
}

// Accepted, in the same spirit as muxa#44's typing hole: a caret that moves
// with no other change reads as quiescent. Cursor motion without text change
// is not output, and the composer gate (muxa#139/#141) is what stands between
// a paste and half-typed input.
func TestSameFrameCaretMoveIsAcceptedAsQuiescent(t *testing.T) {
	at0 := "\x1b[2m\x1b[0;7mP\x1b[0;2mlan ahead\x1b[0m"
	at5 := "\x1b[2mPlan \x1b[0;7ma\x1b[0;2mhead\x1b[0m"
	if !sameFrame(at0, at5) {
		t.Fatal("a moved caret is still decoration (accepted, muxa#144)")
	}
	if strings.Contains(frameKey(at0), "\x1b") {
		t.Fatal("the key must not carry escape bytes it did not interpret")
	}
}

// SGR 27 ends reverse without a reset, so the caret run is still one cell.
func TestSameFrameReverseOffWithoutReset(t *testing.T) {
	bare := "\x1b[2m→ Plan\x1b[0m"
	caret := "\x1b[2m→ \x1b[7mP\x1b[27mlan\x1b[0m"
	if !sameFrame(bare, caret) {
		t.Fatal("SGR 27 must restore the pre-caret style")
	}
	// SGR 22 after the caret is a real weight change, not decoration.
	weight := "\x1b[2m→ \x1b[7mP\x1b[27;22mlan\x1b[0m"
	if sameFrame(bare, weight) {
		t.Fatal("non-faint text after the caret is a real difference")
	}
}

// muxa#144 end to end through the paste gate: a live cursor worker whose only
// change between polls is the caret. lively-hawk %62 sat here at
// refusals=8476, last_gate=two-signal before a lucky matching pair let the
// mail through. Every capture below differs from the one before it, so on
// main this loop never finds a quiescent pair and never pastes.
func TestDeliverCaretBlinkDoesNotHoldMail(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	caret := cursorFrame(cursorFollowUpCaret)
	bare := cursorFrame(cursorFollowUpBare)
	f := &fakeTMUX{
		captures:   []string{caret, bare, caret, bare, caret, bare, caret, bare},
		parkCursor: "5 0", // cursor-agent parks below the box (muxa#44)
	}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	// A frame pair already exists: this pane has been polled before, so the
	// first-observation shortcut in canPaste cannot carry the test.
	d.mu.Lock()
	d.prev["%1"] = bare
	d.mu.Unlock()
	_ = q.Put(&Msg{ID: "cb1", Pane: "%1", From: "parent", To: "worker", Text: "TOKEN-BLINK", DeadlineUnix: 2000})

	for i := 0; i < 4; i++ {
		d.Tick()
	}
	if n := f.injectCount(); n != 1 {
		held := ""
		for _, h := range d.HeldEntries() {
			held += h.Reason
		}
		t.Fatalf("injects=%d want 1 (last_gate=%q): a blinking caret must not hold mail (muxa#144)", n, held)
	}
}

// The control: an animating pane still keeps its mail queued. Caret blink is
// exempt; a spinner, a timer or a token count is not.
func TestDeliverBusyAnimationStillHoldsMail(t *testing.T) {
	dir := t.TempDir()
	q, _ := OpenQueue(dir)
	busy := func(n, row string) string { return cursorFrame(row) + " Generating… " + n + "s\n" }
	f := &fakeTMUX{
		captures: []string{
			busy("11", cursorFollowUpCaret),
			busy("12", cursorFollowUpBare),
			busy("13", cursorFollowUpCaret),
			busy("14", cursorFollowUpBare),
			busy("15", cursorFollowUpCaret),
		},
		parkCursor: "5 0",
	}
	d := NewDeliverer(q, testTMUX(f), time.Millisecond)
	d.now = func() time.Time { return time.Unix(1000, 0) }
	d.mu.Lock()
	d.prev["%1"] = busy("10", cursorFollowUpBare)
	d.mu.Unlock()
	_ = q.Put(&Msg{ID: "cb2", Pane: "%1", From: "parent", To: "worker", Text: "TOKEN-BUSY", DeadlineUnix: 2000})

	for i := 0; i < 4; i++ {
		d.Tick()
	}
	if n := f.injectCount(); n != 0 {
		t.Fatalf("injects=%d want 0: an animating pane keeps its mail (SPEC)", n)
	}
	if g := mustMsg(t, q, "cb2").LastGate; g != gateTwoSignal {
		t.Fatalf("last_gate=%q want %q", g, gateTwoSignal)
	}
}
