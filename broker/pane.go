package main

import (
	"regexp"
	"strings"
	"unicode"
)

// LooksFree reports whether capture-pane output looks like a pane that
// can accept a paste+Enter without interrupting typing or a busy TUI.
//
// Heuristic (tmux-only; no CLI composer JSON, no hook state):
//
//  1. Strip ANSI/OSC sequences.
//  2. Take the last non-empty line, trim trailing space.
//  3. Empty line → free (blank pane / empty input).
//  4. Line has a prompt marker ($ % # > ❯) followed by whitespace and
//     then non-space → not free (someone is typing after the prompt).
//  5. Line ends with a prompt marker (optional trailing space) → free.
//  6. Anything else → not free (command output, spinner, busy TUI).
//
// Retry until deadline is the reliability layer. This is intentionally
// coarse and CLI-agnostic — do not special-case Claude/Cursor/Pi layouts.
func LooksFree(capture string) bool {
	plain := stripANSI(capture)
	line := lastNonEmptyLine(plain)
	line = strings.TrimRightFunc(line, unicode.IsSpace)
	if line == "" {
		return true
	}
	if typedAfterPrompt.MatchString(line) {
		return false
	}
	return promptAtEnd.MatchString(line)
}

var (
	csiRe            = regexp.MustCompile(`\x1b\[[0-9;?=]*[A-Za-z]`)
	oscRe            = regexp.MustCompile(`\x1b\].*?(?:\x07|\x1b\\)`)
	otherEscRe       = regexp.MustCompile(`\x1b.`)
	typedAfterPrompt = regexp.MustCompile(`[$%#>❯][ \t]+\S`)
	promptAtEnd      = regexp.MustCompile(`[$%#>❯][ \t]*$`)
)

func stripANSI(s string) string {
	s = oscRe.ReplaceAllString(s, "")
	s = csiRe.ReplaceAllString(s, "")
	s = otherEscRe.ReplaceAllString(s, "")
	return s
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}
