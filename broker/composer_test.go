package main

import (
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
