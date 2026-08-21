package main

import (
	"regexp"
	"strconv"
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

// cell is one visible rune plus the SGR attributes active when it was written.
type cell struct {
	r       rune
	dim     bool
	reverse bool
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
// faint (SGR 2) request.
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
