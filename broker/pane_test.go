package main

import (
	"testing"
)

func TestVisibleRunesSkipsEscapes(t *testing.T) {
	got := string(visibleRunes("\x1b]0;title\x07\x1b[2mab\x1b[0mc"))
	if got != "abc" {
		t.Fatalf("visible=%q want %q", got, "abc")
	}
}

func TestVisibleRunesTruecolorParamsNotVisible(t *testing.T) {
	got := string(visibleRunes("\x1b[38;2;38;38;38mhi\x1b[m there"))
	if got != "hi there" {
		t.Fatalf("visible=%q want %q", got, "hi there")
	}
}

func TestVisibleRunesLoneEscape(t *testing.T) {
	got := string(visibleRunes("\x1b=ok"))
	if got != "ok" {
		t.Fatalf("visible=%q want %q", got, "ok")
	}
}

func TestVisibleRunesUnterminatedOSC(t *testing.T) {
	// An OSC cut off at the end of a capture line must not leak its
	// payload into the visible text.
	got := string(visibleRunes("hi\x1b]0;half-a-title"))
	if got != "hi" {
		t.Fatalf("visible=%q want %q", got, "hi")
	}
}
