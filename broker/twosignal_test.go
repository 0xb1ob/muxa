package main

import (
	"strings"
	"testing"
)

func TestTwoSignalFreeBasics(t *testing.T) {
	if TwoSignalFree("", "ready>", 0, 6) {
		t.Fatal("no previous capture must WAIT")
	}
	if TwoSignalFree("a", "b", 0, 0) {
		t.Fatal("drawing pane must WAIT")
	}
	idle := "user@host % "
	if !TwoSignalFree(idle, idle, 0, 0) {
		t.Fatal("quiescent empty-at-cursor (cx=0) is FREE")
	}
	// Cursor after the prompt marker: left side ends with %.
	if !TwoSignalFree("ready>", "ready>", 0, len([]rune("ready>"))) {
		t.Fatal("quiescent prompt-at-cursor is FREE")
	}
}

func TestTwoSignalEmptyAtCursorTypedShell(t *testing.T) {
	line := "muxa % hello"
	// cursor after "hello" → text left of cursor is the whole line → typed.
	cells := attrCells(line)
	if emptyAtCursor(line, 0, len(cells)) {
		t.Fatal("typed text left of cursor must WAIT")
	}
	if !emptyAtCursor("muxa % ", 0, len(attrCells("muxa % "))) {
		t.Fatal("prompt only, cursor at end, must be empty")
	}
}

// TestCursorIdleVsTypedSignals is the muxa#44 hole: on Cursor Agent the
// hardware cursor does not move between an empty composer and a typed one,
// and both snapshots are internally static. The two specified signals
// therefore agree with each other and cannot protect a half-typed prompt.
func TestCursorIdleVsTypedSignals(t *testing.T) {
	idle := loadComposerFixture(t, "cursor-idle.ansi")
	typed := loadComposerFixture(t, "cursor-typed.ansi")
	if !idle.HasMeta || !typed.HasMeta || !idle.HasT2 || !typed.HasT2 {
		t.Fatal("need #43 sidecars")
	}

	idleQ := idle.Capture == idle.T2
	typedQ := typed.Capture == typed.T2
	idleEmpty := emptyAtCursor(idle.Capture, idle.CursorY, idle.CursorX)
	typedEmpty := emptyAtCursor(typed.Capture, typed.CursorY, typed.CursorX)
	idleTwo := TwoSignalFree(idle.Capture, idle.T2, idle.CursorY, idle.CursorX)
	typedTwo := TwoSignalFree(typed.Capture, typed.T2, typed.CursorY, typed.CursorX)

	t.Logf("idle  cy=%d cx=%d quiescent=%v emptyAtCursor=%v two=%v parser=%v",
		idle.CursorY, idle.CursorX, idleQ, idleEmpty, idleTwo, LooksFree(idle.Capture))
	t.Logf("typed cy=%d cx=%d quiescent=%v emptyAtCursor=%v two=%v parser=%v",
		typed.CursorY, typed.CursorX, typedQ, typedEmpty, typedTwo, LooksFree(typed.Capture))

	if !idleQ || !typedQ {
		t.Fatal("both fixtures must be internally quiescent (t1==t2)")
	}
	if !idleEmpty || !typedEmpty {
		t.Fatal("hardware cursor row is empty in both — that is the hole")
	}
	if idle.CursorX != typed.CursorX {
		t.Fatalf("cursor_x idle=%d typed=%d — typed input did not move the hardware cursor",
			idle.CursorX, typed.CursorX)
	}
	// cursor_y differs only because idle was captured after a turn (more
	// transcript); it is not a typed-vs-empty signal on one pane.
	if idleTwo != typedTwo {
		t.Fatal("two-signal separated idle from typed; update this test and the detector")
	}
	if !LooksFree(idle.Capture) || LooksFree(typed.Capture) {
		t.Fatal("parser still separates them; paste stays on LooksFree")
	}
	if strings.Contains(typed.Capture, "hello world") == false {
		t.Fatal("typed fixture lost the composer text")
	}
}

func TestTwoSignalCorpus(t *testing.T) {
	// Honest two-signal expect: FREE unless the live pair actually differs
	// (drawing). Do not mark cursor-typed WAIT — that would paper over the hole.
	wantTwo := map[string]bool{
		"cursor-agent-splash.ansi":   true,
		"cursor-idle.ansi":           true,
		"cursor-revcursor-idle.ansi": true,
		"256color-idle.ansi":         true,
		"claude-idle.ansi":           true,
		"pi-idle.ansi":               true,
		"claude-idle-233.ansi":       true, // parser WAIT; two-signal FREE (the ─/hint case)
		"claude-busy.ansi":           true, // origin=static: t2 is a copy, no animation
		"cursor-busy-revcursor.ansi": false,
		"cursor-busy-spinner.ansi":   false,
		"pi-busy.ansi":               true, // origin=static
		"cursor-typed.ansi":          true, // hole: paused typing looks idle
		"cursor-trust-dialog.ansi":   true, // parked cursor, static modal
		"shell-prompt.ansi":          true,
		"vim.ansi":                   true, // origin=static, cx=0
		"garbage.ansi":               true,
	}

	for _, tc := range composerFixtureCases() {
		fx := loadComposerFixture(t, tc.file)
		parser := LooksFree(fx.Capture)
		two := TwoSignalFree(fx.Capture, fx.T2, fx.CursorY, fx.CursorX)
		want, ok := wantTwo[tc.file]
		if !ok {
			t.Errorf("%s: missing two-signal expect", tc.file)
			continue
		}
		agree := "dis"
		if parser == two {
			agree = "yes"
		}
		t.Logf("%-32s parser=%v two=%v agree=%s origin=%s cy=%d cx=%d",
			tc.file, parser, two, agree, fx.Origin, fx.CursorY, fx.CursorX)
		if two != want {
			t.Errorf("%s: two-signal=%v want %v (%s)", tc.file, two, want, tc.why)
		}
		if parser != tc.free {
			t.Errorf("%s: LooksFree=%v want %v (parser assertions must stay honest)", tc.file, parser, tc.free)
		}
	}
}
