package main

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LooksFree is the typed-in-box conjunct, not a free-detection model.
//
// Free-detection — whether a pane is idle / at a prompt — is the broker's
// job: pane_dead / pane_in_mode, control-mode %output silence, and the
// two-signal rule (quiescence AND empty at the hardware cursor). This
// function MUST NOT decide that a pane is at a prompt, and MUST NOT model
// status chrome (spinners, interrupt phrases, bottom-line prompt markers).
// The bottom line is user-configurable; no fixed chrome model can be
// correct.
//
// What it may be used for: refuse a paste when a composer box contains
// unsubmitted human input. That is the only thing in the system that can
// see a Cursor Agent half-typed prompt. Hardware cursor (#44), second-
// capture frame diff (#44), and control-mode silence (#46) are all blind
// to it: cursor-idle and cursor-typed fixtures are identical at cursor_x=0,
// both static (t1==t2), and typing emits %output that then goes quiet —
// the same silence as an empty composer.
//
// If there is no ▄/▀ box, the conjunct is vacuously true and free-detection
// decides. Capture must keep -e: without SGR, a faint placeholder is
// indistinguishable from typed text.
func LooksFree(capture string) bool {
	rows, top, bot, ok := findComposer(capture)
	if !ok {
		return true
	}
	return !typedInBox(rows, top, bot)
}

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

// Half-block rows every agent-CLI composer we have captured draws around its
// input line: U+2584 fills the lower half of the row above the input, U+2580
// the upper half of the row below it. Locating the box is not chrome
// modelling — it is how we read the input the hardware cursor does not see.
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
// between. Trailing rows below the box are ignored; they are not consulted
// for a prompt/busy verdict.
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

// typedInBox reports unsubmitted input inside a composer box.
//
// Placeholder text is rendered faint (SGR 2) by every composer we have
// captured, and the block cursor is reverse video (SGR 7) — those cells
// are not typed. Anything else between the borders is text a human typed
// and must not be clobbered.
//
// Reverse alone is not enough to call a box empty: one typed character
// with the cursor on it is all-reverse. An idle composer always renders
// *some* faint glyph, so a visible row with no faint run is typed.
//
// Spinners, interrupt phrases, and other status chrome are out of scope:
// they decide nothing here. Default-foreground hint text (Claude's
// "Image in clipboard · ctrl+v to paste") is indistinguishable from
// typing and is treated as typed. That is deliberate: the cost is a
// delayed paste; the other way overwrites a human's input.
func typedInBox(rows [][]cell, top, bot int) bool {
	faint, visible := false, false
	for i := top + 1; i < bot; i++ {
		for _, c := range rows[i] {
			if unicode.IsSpace(c.r) {
				continue
			}
			visible = true
			switch {
			case c.dim:
				faint = true
			case c.reverse: // block cursor
			default:
				return true
			}
		}
	}
	return visible && !faint
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
