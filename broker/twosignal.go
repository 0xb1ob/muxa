package main

import (
	"strings"
	"unicode"
)

// Snapshot is one tmux observation: the visible grid plus the hardware cursor.
type Snapshot struct {
	Capture string
	CursorY int
	CursorX int
}

// TwoSignalFree reports whether a pane looks free from what it *does*, not
// what its chrome looks like:
//
//  1. Quiescence — this capture shows the same picture as the previous poll.
//     A busy agent animates (spinner, timer, token count); an idle one is
//     static. Caret blink is not animation: a one-cell reverse-video run is
//     decoration, and comparing raw bytes made a blinking cursor-agent caret
//     reset quiescence on nearly every poll (muxa#144).
//  2. Empty at the hardware cursor — text left of cursor_x on cursor_y is
//     empty or ends at a prompt marker. That is where a shell's typed input
//     actually lands.
//
// Neither signal is enough alone: a busy Claude pane parks the cursor on an
// empty composer, so a drawing pane must WAIT even when the cursor row is
// blank.
//
// Known hole (muxa#44, accepted muxa#79): Cursor Agent draws typed input
// inside the composer box and leaves the hardware cursor on the blank row
// below the splash footer. cursor-idle and cursor-typed are both quiescent
// and both empty at that cursor, so this rule cannot tell them apart. Do
// not "fix" the hole by re-entering chrome modelling here; agents must not
// leave half-typed input in worker panes.
func TwoSignalFree(prev, cur string, cursorY, cursorX int) bool {
	if prev == "" || !sameFrame(prev, cur) {
		return false
	}
	return emptyAtCursor(cur, cursorY, cursorX)
}

func emptyAtCursor(capture string, cursorY, cursorX int) bool {
	if cursorY < 0 || cursorX < 0 {
		return false
	}
	lines := strings.Split(capture, "\n")
	if cursorY >= len(lines) {
		return false
	}
	runes := visibleRunes(lines[cursorY])
	if cursorX > len(runes) {
		cursorX = len(runes)
	}
	s := strings.TrimRightFunc(string(runes[:cursorX]), unicode.IsSpace)
	if s == "" {
		return true
	}
	return promptAtEnd.MatchString(s)
}
