package main

import (
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
	runes := visibleRunes(line)
	if emptyAtCursor(line, 0, len(runes)) {
		t.Fatal("typed text left of cursor must WAIT")
	}
	if !emptyAtCursor("muxa % ", 0, len(visibleRunes("muxa % "))) {
		t.Fatal("prompt only, cursor at end, must be empty")
	}
}

// TestCursorAgentTypingHole documents muxa#44/#79: Cursor Agent draws typed
// input inside the composer box and leaves the hardware cursor on the blank
// row below the splash footer. Two-signal cannot distinguish idle from
// half-typed; etiquette replaces the deleted typed-in-box parser.
func TestCursorAgentTypingHole(t *testing.T) {
	idle := "transcript\n▄▄▄▄\n \x1b[2m→ Plan\x1b[0m\n▀▀▀▀\n footer"
	typed := "transcript\n▄▄▄▄\n hello world\n▀▀▀▀\n footer"
	cy := 4
	if !TwoSignalFree(idle, idle, cy, 0) || !TwoSignalFree(typed, typed, cy, 0) {
		t.Fatal("both look free to two-signal — accepted hole (muxa#79)")
	}
	if !emptyAtCursor(idle, cy, 0) || !emptyAtCursor(typed, cy, 0) {
		t.Fatal("cursor row empty in both")
	}
}
