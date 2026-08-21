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
//  1. Quiescence — this capture equals the previous poll. A busy agent
//     animates (spinner, timer, token count); an idle one is static.
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
	if prev == "" || prev != cur {
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
	cells := attrCells(lines[cursorY])
	if cursorX > len(cells) {
		cursorX = len(cells)
	}
	var b strings.Builder
	for _, c := range cells[:cursorX] {
		b.WriteRune(c.r)
	}
	s := strings.TrimRightFunc(b.String(), unicode.IsSpace)
	if s == "" {
		return true
	}
	return promptAtEnd.MatchString(s)
}
