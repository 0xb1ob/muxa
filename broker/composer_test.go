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

// muxa#141: a cold cursor-agent pane parks a reverse-video caret on the first
// character of its placeholder. SGR 7 arrives as "\x1b[0;7m", and the reset
// clears faint — so the muxa#139 non-faint scan read that one cell as typed
// text and refused every dispatch into a freshly spawned cursor worker,
// attempts=0, refusals in the thousands. Rows below are verbatim
// `capture-pane -e` output from cursor-agent v2026.08.11 with an empty
// composer, trailing pad trimmed.
func TestComposerInputForeignCursorColdCaretIdle(t *testing.T) {
	idle := "  Cursor Agent\n" +
		"  \x1b[2mTip: Use /plan to plan execution and reach the right outcome faster.\x1b[0m\n" +
		" \x1b[38;2;38;38;38m▄▄▄▄▄\x1b[39m\n" +
		" \x1b[48;2;38;38;38m \x1b[2m→ \x1b[0;7m\x1b[48;2;38;38;38mP\x1b[0;2m\x1b[48;2;38;38;38mlan, search, build anything\x1b[0m\x1b[48;2;38;38;38m    \x1b[49m\n" +
		" \x1b[38;2;38;38;38m▀▀▀▀▀\x1b[39m\n" +
		"  \x1b[2mComposer 2.5 Fast\x1b[0m\n"
	if composerRowHasNonFaintText(strings.Split(idle, "\n")[3]) {
		t.Fatal("a one-cell reverse caret is not typed text (muxa#141)")
	}
	if composerInputForeign(idle) {
		t.Fatal("cold cursor composer with a caret must not block paste (muxa#141)")
	}
}

// The same caret on the post-turn placeholder, verbatim from a live cursor
// worker pane. muxa#123 allowed these words; muxa#139 started refusing them
// again whenever the caret happened to sit on the "A".
func TestComposerInputForeignCursorFollowUpCaretIdle(t *testing.T) {
	idle := "  the day starts in green\n" +
		" \x1b[38;2;242;242;242m▄▄▄▄▄\x1b[39m\n" +
		" \x1b[48;2;242;242;242m \x1b[2m→ \x1b[0;7m\x1b[48;2;242;242;242mA\x1b[0;2m\x1b[48;2;242;242;242mdd a follow-up\x1b[0m\x1b[48;2;242;242;242m    \x1b[49m\n" +
		" \x1b[38;2;242;242;242m▀▀▀▀▀\x1b[39m\n" +
		"  \x1b[2mCursor Grok 4.6 High\x1b[0m\n"
	if composerInputForeign(idle) {
		t.Fatal("post-turn cursor placeholder with a caret must not block paste (muxa#141)")
	}
}

// The busy row keeps its stop hint, so the caret exemption must not free it.
func TestComposerInputForeignCursorCaretBusy(t *testing.T) {
	busy := " \x1b[38;2;242;242;242m▄▄▄▄▄\x1b[39m\n" +
		" \x1b[48;2;242;242;242m \x1b[2m→ \x1b[0;7m\x1b[48;2;242;242;242mA\x1b[0;2m\x1b[48;2;242;242;242mdd a follow-up      \x1b[2mctrl+c to stop\x1b[0m\x1b[48;2;242;242;242m \x1b[49m\n" +
		" \x1b[38;2;242;242;242m▀▀▀▀▀\x1b[39m\n"
	if !composerInputForeign(busy) {
		t.Fatal("busy cursor composer must still block paste")
	}
}

// Typing keeps the caret one cell wide, so the muxa#141 exemption cannot
// swallow the operator text the muxa#139 scan exists to see.
func TestComposerInputForeignCursorTypingWithCaret(t *testing.T) {
	typing := " \x1b[38;2;38;38;38m▄▄▄▄▄\x1b[39m\n" +
		" \x1b[48;2;38;38;38m \x1b[2m→ Plan, search, build anything\x1b[0mHUMANTYPING\x1b[0;7m \x1b[0m\n" +
		" \x1b[38;2;38;38;38m▀▀▀▀▀\x1b[39m\n"
	if !composerInputForeign(typing) {
		t.Fatal("operator text beside a caret must still block paste (muxa#139)")
	}
}

// A reverse run wider than one cell is content — a highlighted selection, a
// mouse-mode marker — not a caret, so it keeps blocking paste.
func TestComposerRowHasNonFaintTextReverseRunIsContent(t *testing.T) {
	caret := "\x1b[48;2;38;38;38m \x1b[2m→ \x1b[0;7mP\x1b[0;2mlan\x1b[0m"
	if composerRowHasNonFaintText(caret) {
		t.Fatal("one reverse cell is a caret")
	}
	selected := "\x1b[48;2;38;38;38m \x1b[2m→ \x1b[0;7mPICKED\x1b[0;2m\x1b[0m"
	if !composerRowHasNonFaintText(selected) {
		t.Fatal("a multi-cell reverse run is content, not a caret")
	}
}

// SGR 27 ends reverse without a full reset, so the cell after it is judged on
// faint alone.
func TestComposerRowHasNonFaintTextReverseOffRestoresScan(t *testing.T) {
	row := "\x1b[2m→ \x1b[7mP\x1b[27mlan\x1b[0m"
	if composerRowHasNonFaintText(row) {
		t.Fatal("reverse off must leave the surrounding faint text faint")
	}
	typed := "\x1b[2m→ \x1b[7mP\x1b[27;22mlan\x1b[0m"
	if !composerRowHasNonFaintText(typed) {
		t.Fatal("SGR 22 after the caret exposes non-faint text")
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
