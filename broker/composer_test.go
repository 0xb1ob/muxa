package main

import (
	"strings"
	"testing"
)

func TestComposerInputForeignIdlePlaceholder(t *testing.T) {
	idle := "log\n\x1b[38;2;38;38;38m▄▄▄▄▄\x1b[0m\n \x1b[2m→ Plan, search, build anything\x1b[0m\n\x1b[38;2;38;38;38m▀▀▀▀▀\x1b[0m\n footer"
	if composerInputForeign(idle) {
		t.Fatal("idle placeholder must not block paste")
	}
}

func TestComposerInputForeignTypedText(t *testing.T) {
	typed := "log\n▄▄▄▄▄\n HUMANTYPING\n▀▀▀▀▀\n footer"
	if !composerInputForeign(typed) {
		t.Fatal("typed composer text must block paste")
	}
}

func TestComposerInputForeignCollapsedPaste(t *testing.T) {
	collapsed := "ready>\n[Pasted text #1 +79 lines]"
	if !composerInputForeign(collapsed) {
		t.Fatal("unsubmitted collapsed paste must block paste")
	}
}

func TestComposerInputForeignBusyTurn(t *testing.T) {
	busy := "log\n▄▄▄▄▄\n → Add a follow-up   ctrl+c to stop\n▀▀▀▀▀\n footer"
	if !composerInputForeign(busy) {
		t.Fatal("busy composer row must block paste")
	}
}

// muxa#123: cursor-agent replaces the cold placeholder with "→ Add a follow-up"
// once a turn completes. Rows below are the real thing, captured from a live
// cursor-agent pane (v2026.08.11) under `capture-pane -e`, padded to the pane
// width the way tmux hands them to the broker.
func TestComposerInputForeignCursorFollowUpIdle(t *testing.T) {
	idle := "  the day starts in green\n" +
		" \x1b[38;2;38;38;38m▄▄▄▄▄\x1b[0m\n" +
		"\x1b[48;2;38;38;38m  \x1b[2m→ Add a follow-up                              \x1b[49m\n" +
		" \x1b[38;2;38;38;38m▀▀▀▀▀\x1b[0m\n" +
		"  Cursor Grok 4.6 High Fast · 7%\n"
	if composerInputForeign(idle) {
		t.Fatal("cursor post-turn idle placeholder must not block paste (muxa#123)")
	}
}

// The same words during a turn, with cursor's right-aligned stop hint on the
// same row. muxa#123's allowlist entry must not reach this one (muxa#44/#79).
func TestComposerInputForeignCursorFollowUpBusy(t *testing.T) {
	busy := "  count slowly from 1 to 40\n" +
		" \x1b[38;2;38;38;38m▄▄▄▄▄\x1b[0m\n" +
		"\x1b[48;2;38;38;38m  \x1b[2m→ Add a follow-up                ctrl+c to stop \x1b[49m\n" +
		" \x1b[38;2;38;38;38m▀▀▀▀▀\x1b[0m\n" +
		"  Cursor Grok 4.6 High Fast · 7%\n"
	if !composerInputForeign(busy) {
		t.Fatal("busy cursor composer must still block paste")
	}
}

// muxa#139: Cursor parent keeps the dim cold hint on screen while the
// operator types in normal weight after a reset sequence. stripANSI still
// contains "plan, search, build", which composerIdlePlaceholder alone
// treated as free.
func TestComposerInputForeignCursorParentTyping(t *testing.T) {
	typing := "  prior turn output\n" +
		" \x1b[38;2;38;38;38m▄▄▄▄▄\x1b[0m\n" +
		"\x1b[48;2;38;38;38m \x1b[2m→ Plan, search, build anything\x1b[0mHUMANTYPING\x1b[0m\n" +
		" \x1b[38;2;38;38;38m▀▀▀▀▀\x1b[0m\n" +
		"  Composer 2.5 Fast\n"
	if !composerInputForeign(typing) {
		t.Fatal("cursor parent mid-keystroke must block paste (muxa#139)")
	}
	if !composerRowHasNonFaintText(strings.Split(typing, "\n")[2]) {
		t.Fatal("typing row should expose non-faint operator text")
	}
}

func TestComposerRowHasNonFaintTextIdlePlaceholder(t *testing.T) {
	idle := "\x1b[48;2;38;38;38m  \x1b[2m→ Add a follow-up                              \x1b[49m"
	if composerRowHasNonFaintText(idle) {
		t.Fatal("dim idle placeholder must not count as typed")
	}
}

func TestComposerRowHasNonFaintTextTruecolorNotFaint(t *testing.T) {
	// 38;2 must not be read as SGR 2 faint.
	bg := "\x1b[38;2;38;38;38m\x1b[48;2;38;38;38m  \x1b[2m→ Plan, search, build anything\x1b[0m"
	if composerRowHasNonFaintText(bg) {
		t.Fatal("truecolor params must not trip faint detection")
	}
}

func TestComposerInputForeignCursorWrappedTyping(t *testing.T) {
	// Narrow panes wrap in-box typing; a single-line composerInputRow miss
	// dropped the gate entirely (muxa#139).
	wrapped := "\x1b[38;2;38;38;38m▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄\x1b[39m\n" +
		"\x1b[48;2;38;38;38m \x1b[2m→ Plan, search, build anything\x1b[0mHUMANTYPI\n" +
		"NG\n" +
		"\x1b[38;2;38;38;38m▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\x1b[39m\n"
	if !composerInputForeign(wrapped) {
		t.Fatal("wrapped cursor typing must block paste")
	}
}

func TestUnsubmittedPasteVisible(t *testing.T) {
	if !unsubmittedPasteVisible("[Pasted text +48 lines]") {
		t.Fatal("collapsed paste marker")
	}
	if unsubmittedPasteVisible("working...") {
		t.Fatal("plain working line is not collapsed paste")
	}
}

func TestAgentTurnStarted(t *testing.T) {
	busy := "▄▄▄▄▄\n → Add a follow-up   ctrl+c to stop\n▀▀▀▀▀"
	if !agentTurnStarted(busy) {
		t.Fatal("busy composer means turn started")
	}
	collapsed := "[Pasted text +48 lines]"
	if agentTurnStarted(collapsed) {
		t.Fatal("collapsed paste alone is not a started turn")
	}
}
