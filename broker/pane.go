package main

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	csiRe       = regexp.MustCompile(`\x1b\[[0-9;?=]*[A-Za-z]`)
	oscRe       = regexp.MustCompile(`\x1b\].*?(?:\x07|\x1b\\)`)
	otherEscRe  = regexp.MustCompile(`\x1b.`)
	promptAtEnd = regexp.MustCompile(`[$%#>❯][ \t]*$`)
)

func stripANSI(s string) string {
	s = oscRe.ReplaceAllString(s, "")
	s = csiRe.ReplaceAllString(s, "")
	s = otherEscRe.ReplaceAllString(s, "")
	return s
}

// visibleRunes decodes one `capture-pane -e` line into the runes a reader
// would see, skipping CSI, OSC, and lone-ESC sequences. Byte-precise
// skipping (not regex substitution) so an unterminated sequence at the end
// of a line cannot leak parameter bytes into the cursor-column math in
// emptyAtCursor.
func visibleRunes(s string) []rune {
	var out []rune
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if n, ok := skipCSI(s[i:]); ok {
				i += n
				continue
			}
			if n, ok := skipOSC(s[i:]); ok {
				i += n
				continue
			}
			// Lone ESC + one byte (charset select, keypad mode, …).
			i += minInt(2, len(s)-i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		out = append(out, r)
		i += size
	}
	return out
}

// skipCSI consumes one ESC [ … <final> sequence at the head of s.
func skipCSI(s string) (int, bool) {
	if len(s) < 3 || s[1] != '[' {
		return 0, false
	}
	i := 2
	for i < len(s) && strings.IndexByte("0123456789;?=", s[i]) >= 0 {
		i++
	}
	if i >= len(s) {
		return 0, false
	}
	c := s[i]
	if !(c >= 'A' && c <= 'Z') && !(c >= 'a' && c <= 'z') {
		return 0, false
	}
	return i + 1, true
}

// skipOSC consumes one ESC ] … (BEL | ESC \) sequence at the head of s.
func skipOSC(s string) (int, bool) {
	if len(s) < 2 || s[1] != ']' {
		return 0, false
	}
	for i := 2; i < len(s); i++ {
		if s[i] == 0x07 {
			return i + 1, true
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
			return i + 2, true
		}
	}
	return len(s), true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
