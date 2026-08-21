package main

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LooksFree reports whether capture-pane output looks like a pane that
// can accept a paste+Enter without interrupting typing or a busy TUI.
//
// Two shapes exist and only the first one has a prompt on the last line:
//
// Shell-ish pane — the last non-empty line *is* the input line:
//
//  1. Strip ANSI/OSC.
//  2. Take the last non-empty line, trim trailing space.
//  3. Empty line → free (blank pane / empty input).
//  4. A prompt marker ($ % # > ❯) followed by whitespace and then non-space
//     → not free (someone is typing after the prompt).
//  5. Line ends with a prompt marker (optional trailing space) → free.
//  6. Anything else → not free (command output, spinner, busy TUI).
//
// Agent-CLI pane — the input line sits inside a half-block composer box and
// the last lines are chrome (status row, cwd, model name), so rule 6 above
// would reject an idle CLI forever and every first brief would wait out
// MUXA_BROKER_DEADLINE. When the capture contains a composer box, decide on
// the box instead: see composerFree.
//
// Both paths stay CLI-agnostic — the composer path keys off terminal
// attributes (SGR 2 faint, SGR 7 reverse) and half-block box chrome, not off
// any Claude/Cursor/Pi string. Retry until deadline remains the reliability
// layer.
func LooksFree(capture string) bool {
	if rows, top, bot, ok := findComposer(capture); ok {
		return composerFree(rows, top, bot)
	}
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

// Half-block rows every agent-CLI composer we have captured draws around its
// input line: U+2584 fills the lower half of the row above the input, U+2580
// the upper half of the row below it.
const (
	borderTop    = '▄' // ▄
	borderBottom = '▀' // ▀
)

// cell is one visible rune plus the SGR attributes active when it was written.
type cell struct {
	r       rune
	dim     bool
	reverse bool
}

// findComposer locates the innermost composer box: the last bottom-edge row
// and the top-edge row directly above it, with at least one input row in
// between. Trailing chrome below the box (status row, cwd) is ignored.
func findComposer(capture string) (rows [][]cell, top, bot int, ok bool) {
	for _, line := range strings.Split(capture, "\n") {
		rows = append(rows, attrCells(line))
	}
	bot = -1
	for i := len(rows) - 1; i >= 0; i-- {
		if isBorder(rows[i], borderBottom) {
			bot = i
			break
		}
	}
	if bot < 2 {
		return nil, 0, 0, false
	}
	for i := bot - 1; i >= 0; i-- {
		if isBorder(rows[i], borderBottom) {
			return nil, 0, 0, false
		}
		if isBorder(rows[i], borderTop) {
			return rows, i, bot, true
		}
	}
	return nil, 0, 0, false
}

// isBorder reports whether a row is nothing but r and whitespace.
func isBorder(row []cell, r rune) bool {
	seen := false
	for _, c := range row {
		switch {
		case c.r == r:
			seen = true
		case unicode.IsSpace(c.r):
		default:
			return false
		}
	}
	return seen
}

// composerFree decides an agent-CLI composer box.
//
// Placeholder text ("Add a follow-up", "Plan, search, build anything", a bare
// ❯) is rendered faint by every composer we have captured, and the block
// cursor is reverse video — so faint and reverse cells are chrome. Anything
// else between the borders is text a human typed and must not be clobbered.
//
// A live status line inside the box ("esc to interrupt", "ctrl+c to stop")
// means a turn is running. Those phrases are only trusted inside the box:
// above it they are ordinary transcript text from an earlier turn. A spinner
// glyph is the reverse — it is only ever live, so it counts from the row
// directly above the box as well.
//
// Reverse alone is not enough to call a box empty: one typed character with
// the cursor on it is all-reverse. An idle composer always renders *some*
// faint glyph — the prompt marker or the placeholder — so free requires
// either a faint run or a genuinely blank row.
//
// The deliberate limit: default-foreground text in the box is read as typed
// even when it is really a hint (Claude's "Image in clipboard · ctrl+v to
// paste" is byte-identical to text typed after a faint ❯). Erring that way
// costs the deadline fallback; erring the other way overwrites a human's
// input. Recognising the hint would mean parsing one CLI's chrome.
func composerFree(rows [][]cell, top, bot int) bool {
	var typed, status strings.Builder
	spinner, faint, visible := false, false, false
	for i := top + 1; i < bot; i++ {
		for _, c := range rows[i] {
			status.WriteRune(c.r)
			if isSpinner(c.r) {
				spinner = true
			}
			if unicode.IsSpace(c.r) {
				continue
			}
			visible = true
			switch {
			case c.dim:
				faint = true
			case c.reverse: // block cursor
			default:
				typed.WriteRune(c.r)
			}
		}
		status.WriteRune('\n')
	}
	if spinner || hasBusyPhrase(status.String()) {
		return false
	}
	if top > 0 && hasSpinner(rows[top-1]) {
		return false
	}
	if typed.Len() > 0 {
		return false
	}
	return faint || !visible
}

// busyPhrases are interrupt hints a composer only shows mid-turn.
var busyPhrases = []string{
	"esc to interrupt",
	"esc to stop",
	"ctrl+c to stop",
	"ctrl-c to stop",
	"ctrl+c to interrupt",
}

func hasBusyPhrase(s string) bool {
	s = strings.ToLower(s)
	for _, p := range busyPhrases {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func hasSpinner(row []cell) bool {
	for _, c := range row {
		if isSpinner(c.r) {
			return true
		}
	}
	return false
}

// isSpinner reports whether r is a braille cell other than the blank. Every
// CLI in scope animates its "working" indicator out of the braille block, and
// braille never shows up in composer chrome or in prose typed at a prompt.
func isSpinner(r rune) bool {
	return r > '⠀' && r <= '⣿'
}

// attrCells decodes one `capture-pane -e` line into visible cells carrying
// the SGR attributes that were active for each rune.
func attrCells(s string) []cell {
	var out []cell
	dim, rev := false, false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if n, params, final, ok := parseCSI(s[i:]); ok {
				if final == 'm' {
					dim, rev = applySGR(params, dim, rev)
				}
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
		out = append(out, cell{r: r, dim: dim, reverse: rev})
		i += size
	}
	return out
}

// parseCSI consumes one ESC [ … <final> sequence at the head of s.
func parseCSI(s string) (n int, params string, final byte, ok bool) {
	if len(s) < 3 || s[1] != '[' {
		return 0, "", 0, false
	}
	i := 2
	for i < len(s) && strings.IndexByte("0123456789;?=", s[i]) >= 0 {
		i++
	}
	if i >= len(s) {
		return 0, "", 0, false
	}
	c := s[i]
	if !(c >= 'A' && c <= 'Z') && !(c >= 'a' && c <= 'z') {
		return 0, "", 0, false
	}
	return i + 1, s[2:i], c, true
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

// applySGR folds one SGR parameter list into the faint/reverse state.
func applySGR(params string, dim, rev bool) (bool, bool) {
	if params == "" {
		return false, false // ESC[m is a full reset
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		f := strings.TrimLeft(fields[i], "?=")
		n := 0
		if f != "" {
			v, err := strconv.Atoi(f)
			if err != nil {
				continue
			}
			n = v
		}
		switch n {
		case 0:
			dim, rev = false, false
		case 2:
			dim = true
		case 7:
			rev = true
		case 22:
			dim = false
		case 27:
			rev = false
		case 38, 48, 58:
			i += extColorSkip(fields, i)
		}
	}
	return dim, rev
}

// extColorSkip returns how many following parameters a 38/48/58 colour
// selector swallows, so that truecolor "38;2;r;g;b" is never misread as a
// faint (SGR 2) request — composer borders are drawn exactly that way.
func extColorSkip(fields []string, i int) int {
	if i+1 >= len(fields) {
		return 0
	}
	switch fields[i+1] {
	case "2": // r;g;b
		return 4
	case "5": // palette index
		return 2
	case "3", "4", "6": // CMY / CMYK
		return 5
	}
	return 1
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
