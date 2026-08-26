package main

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// sameFrame reports whether two `capture-pane -e` frames show the same
// picture, ignoring caret decoration.
//
// Byte equality was the whole quiescence test until muxa#144. Cursor-agent
// blinks a reverse-video caret inside its composer box, so two frames of one
// *static* pane differ by the SGR run around a single cell — the same one-cell
// reverse run the composer gate already exempts (muxa#141). Sampled at the
// broker's poll the blink phase-locks against it: 49 of 142 frames of one live
// worker carried the caret, consecutive pairs kept disagreeing, and mail sat
// at last_gate=two-signal with refusals in the thousands before a lucky
// matching pair let it through.
//
// Only the caret is normalised away. Text, faint, colours and every other
// attribute survive into the key, so a pane that animates anything else — a
// spinner glyph, an elapsed timer, a token count, a colour sweep — is still
// not quiescent and still WAITs.
func sameFrame(prev, cur string) bool {
	if prev == cur {
		return true
	}
	return frameKey(prev) == frameKey(cur)
}

// frameKey renders a frame into what a reader would see with the caret taken
// off: every cell's rune plus the style it was drawn with, trailing blanks
// trimmed. It is a comparison key, not a capture — nothing reconstructs a
// frame from it.
func frameKey(capture string) string {
	var b strings.Builder
	b.Grow(len(capture))
	var cells []cell
	for i, line := range strings.Split(capture, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		cells = renderCells(cells[:0], line)
		uncaret(cells)
		writeLineKey(&b, cells)
	}
	return b.String()
}

// cell is one rendered grid cell: the rune, the style it was drawn with, and
// the style in force just before reverse video turned on — which is what the
// same cell wears in the frame where the caret is not on it.
type cell struct {
	r      rune
	style  cellStyle
	preRev cellStyle
}

// renderCells decodes one capture-pane -e line into styled cells, appending
// to dst so the caller can reuse one scratch slice across lines. Style starts
// clean on every line: tmux writes each captured line's attributes from
// scratch, and per-row independence is what the composer gate already assumes
// (muxa#139).
func renderCells(dst []cell, line string) []cell {
	var style, preRev cellStyle
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			if n, ok := skipCSI(line[i:]); ok {
				if seq := line[i : i+n]; seq[len(seq)-1] == 'm' {
					was := style
					style = applyCellSGR(style, seq[2:len(seq)-1])
					if !was.reverse && style.reverse {
						preRev = was
						preRev.reverse = false
					}
				}
				i += n
				continue
			}
			if n, ok := skipOSC(line[i:]); ok {
				i += n
				continue
			}
			i += minInt(2, len(line)-i)
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		i += size
		dst = append(dst, cell{r: r, style: style, preRev: preRev})
	}
	return dst
}

// uncaret rewrites every one-cell reverse-video run to the style that cell
// wears with no caret on it. A longer reverse run is content — a selection, a
// highlight, a mouse marker — and keeps its style, so it still breaks
// quiescence when it appears or moves. Same one-cell rule as muxa#141.
func uncaret(cells []cell) {
	for i := 0; i < len(cells); {
		if !cells[i].style.reverse {
			i++
			continue
		}
		j := i + 1
		for j < len(cells) && cells[j].style.reverse {
			j++
		}
		if j-i == 1 {
			cells[i].style = cells[i].preRev
		}
		i = j
	}
}

// writeLineKey serialises one line's cells, emitting a style tag only where
// the style changes. Trailing blanks are dropped: a caret parked past the
// last visible rune keeps its cell out of tmux's trailing-space trim, so the
// frame carrying the caret would otherwise be one cell longer than the frame
// without it.
func writeLineKey(b *strings.Builder, cells []cell) {
	end := len(cells)
	for end > 0 && unicode.IsSpace(cells[end-1].r) {
		end--
	}
	var last cellStyle
	first := true
	for _, c := range cells[:end] {
		if first || c.style != last {
			writeStyle(b, c.style)
			last = c.style
			first = false
		}
		b.WriteRune(c.r)
	}
}

// cellStyle is the full SGR state a cell is drawn with. The composer gate
// reads faint and reverse only (sgrState); quiescence keeps every attribute it
// can name, because dropping one would make a pane that animates in that
// attribute alone look static. fg/bg are the raw parameter text, so no colour
// space needs modelling — only compared, never rendered.
type cellStyle struct {
	bold      bool
	dim       bool
	italic    bool
	underline bool
	blink     bool
	reverse   bool
	hidden    bool
	strike    bool
	fg        string
	bg        string
}

const hexDigits = "0123456789abcdef"

// writeStyle appends a tag no capture can contain, so a style change can
// never be confused with pane text.
func writeStyle(b *strings.Builder, s cellStyle) {
	var bits uint16
	for i, on := range [...]bool{s.bold, s.dim, s.italic, s.underline, s.blink, s.reverse, s.hidden, s.strike} {
		if on {
			bits |= 1 << uint(i)
		}
	}
	b.WriteByte(0x00)
	b.WriteByte(hexDigits[(bits>>4)&0xf])
	b.WriteByte(hexDigits[bits&0xf])
	b.WriteByte(0x01)
	b.WriteString(s.fg)
	b.WriteByte(0x01)
	b.WriteString(s.bg)
	b.WriteByte(0x00)
}

// applyCellSGR folds one SGR parameter list into a cell style. Unknown codes
// are skipped rather than guessed at: a code this does not model cannot make
// two different frames compare equal, it just does not tell them apart.
func applyCellSGR(s cellStyle, params string) cellStyle {
	if params == "" {
		return cellStyle{}
	}
	for i := 0; i < len(params); {
		start := i
		tok, next := nextParam(params, i)
		i = next
		n, ok := sgrCode(tok)
		if !ok {
			continue
		}
		switch {
		case n == 0:
			s = cellStyle{}
		case n == 1:
			s.bold = true
		case n == 2:
			s.dim = true
		case n == 3:
			s.italic = true
		case n == 4:
			s.underline = true
		case n == 5, n == 6:
			s.blink = true
		case n == 7:
			s.reverse = true
		case n == 8:
			s.hidden = true
		case n == 9:
			s.strike = true
		case n == 21, n == 22:
			s.bold, s.dim = false, false
		case n == 23:
			s.italic = false
		case n == 24:
			s.underline = false
		case n == 25:
			s.blink = false
		case n == 27:
			s.reverse = false
		case n == 28:
			s.hidden = false
		case n == 29:
			s.strike = false
		case n == 38, n == 48:
			var color string
			color, i = extendedColor(params, start, i)
			if n == 38 {
				s.fg = color
			} else {
				s.bg = color
			}
		case n >= 30 && n <= 37, n >= 90 && n <= 97:
			s.fg = tok
		case n == 39:
			s.fg = ""
		case n >= 40 && n <= 47, n >= 100 && n <= 107:
			s.bg = tok
		case n == 49:
			s.bg = ""
		}
	}
	return s
}

// nextParam returns the parameter starting at i and the index just past its
// separator.
func nextParam(params string, i int) (tok string, next int) {
	start := i
	for i < len(params) && params[i] != ';' {
		i++
	}
	tok = params[start:i]
	if i < len(params) {
		i++
	}
	return tok, i
}

// sgrCode parses one SGR parameter. An empty parameter is 0 (ECMA-48); a
// colon-substyle parameter (`4:3`) is not a code this models.
func sgrCode(tok string) (int, bool) {
	if tok == "" {
		return 0, true
	}
	n := 0
	for i := 0; i < len(tok); i++ {
		if tok[i] < '0' || tok[i] > '9' {
			return 0, false
		}
		n = n*10 + int(tok[i]-'0')
		if n > 1<<16 {
			return 0, false
		}
	}
	return n, true
}

// extendedColor consumes a 38/48 sub-parameter run (`5;n` indexed, `2;r;g;b`
// truecolor) whose leading code started at from, and returns the raw
// parameter text plus the index to resume parsing at. Letting those
// sub-parameters fall through as top-level codes is what would read the 2 in
// `48;2;r;g;b` as faint — the shape muxa#139 already had to special-case.
func extendedColor(params string, from, i int) (string, int) {
	kind, j := nextParam(params, i)
	take := 0
	switch kind {
	case "5":
		take = 1
	case "2":
		take = 3
	}
	end := j
	for k := 0; k < take && j < len(params); k++ {
		_, j = nextParam(params, j)
		end = j
	}
	return strings.TrimSuffix(params[from:end], ";"), j
}
