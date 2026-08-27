package main

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// composerInputForeign reports whether an agent-CLI composer holds text the
// broker did not put there. Two-signal is blind to in-box typing (muxa#79);
// this is the dispatch safety gate for defect 3 in muxa#111.
func composerInputForeign(capture string) bool {
	if rows, ok := composerBoxRows(capture); ok {
		for _, row := range rows {
			if composerRowForeign(row) {
				return true
			}
		}
		return false
	}
	return strings.Contains(stripANSI(capture), "[Pasted text")
}

func composerRowForeign(row string) bool {
	// Cursor keeps the dim idle hint on screen and paints operator keystrokes
	// in normal weight after a reset sequence. stripANSI still contains the
	// placeholder substring, so text-only matching treats mid-typing as free
	// (muxa#139). SGR faint is the cursor idle/typing discriminator.
	if composerRowHasNonFaintText(row) {
		return true
	}
	plain := strings.TrimSpace(stripANSI(row))
	if plain == "" {
		return false
	}
	if strings.Contains(plain, "[Pasted text") {
		return true
	}
	return !composerIdlePlaceholder(plain)
}

// composerIdlePlaceholder is the faint empty-composer hint Cursor/Claude show
// when nothing has been typed. Busy-turn chrome is not a placeholder.
func composerIdlePlaceholder(s string) bool {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "plan, search, build") {
		return true
	}
	if strings.Contains(lower, "ask anything") || strings.Contains(lower, "type a message") {
		return true
	}
	if composerFollowUpIdle(s) {
		return true
	}
	trim := strings.TrimSpace(s)
	if trim == "" || trim == "→" {
		return true
	}
	if strings.HasPrefix(trim, "→") {
		r := []rune(trim)
		rest := strings.TrimSpace(string(r[1:]))
		if rest == "" {
			return true
		}
		for _, ch := range rest {
			if !unicode.IsSpace(ch) && ch != '→' {
				return false
			}
		}
		return true
	}
	return false
}

// composerFollowUpIdle reports whether row is cursor-agent's post-turn empty
// composer. Once a turn completes cursor swaps the cold hint for
// "→ Add a follow-up", which composerRowForeign read as typed text — so the
// broker refused every paste into an idle cursor pane, silently (muxa#123).
//
// Cursor paints the same words all through a turn, with a right-aligned
// "ctrl+c to stop" on the same row, so the match is exact once the arrow and
// tmux's trailing pad are trimmed: the busy row and anything a human typed
// keep their extra text and stay foreign (muxa#44/#79/#105).
func composerFollowUpIdle(s string) bool {
	trim := strings.TrimSpace(s)
	trim = strings.TrimSpace(strings.TrimPrefix(trim, "→"))
	return strings.EqualFold(trim, "Add a follow-up")
}

// composerRowHasNonFaintText reports whether a capture-pane -e composer row
// renders any visible rune without SGR faint (2). Cursor idle placeholders
// are entirely dim; operator typing is drawn after \x1b[0m/\x1b[22m.
//
// Cursor parks a reverse-video caret on the first character of the idle
// placeholder — a cold pane draws
// "\x1b[2m→ \x1b[0;7mP\x1b[0;2mlan, search, build anything" — and SGR 7
// arrives with a reset that clears faint. Counting that one cell as typed
// text refused every paste into a freshly spawned cursor worker, on every
// tick, forever (muxa#141). A caret covers a single cell, so a reverse run of
// one visible rune is decoration; a longer run is content the operator can
// see and must not be pasted over.
func composerRowHasNonFaintText(row string) bool {
	var sgr sgrState
	revRun := 0
	for i := 0; i < len(row); {
		if row[i] == 0x1b {
			if n, ok := skipCSI(row[i:]); ok {
				if seq := row[i : i+n]; seq[len(seq)-1] == 'm' {
					was := sgr.reverse
					sgr = applySGR(sgr, seq[2:len(seq)-1])
					if was && !sgr.reverse {
						revRun = 0
					}
				}
				i += n
				continue
			}
			if n, ok := skipOSC(row[i:]); ok {
				i += n
				continue
			}
			i += minInt(2, len(row)-i)
			continue
		}
		r, size := utf8.DecodeRuneInString(row[i:])
		i += size
		if unicode.IsSpace(r) {
			revRun = 0
			continue
		}
		if sgr.reverse {
			revRun++
			if revRun > 1 {
				return true
			}
			continue
		}
		revRun = 0
		if !sgr.dim {
			return true
		}
	}
	return false
}

// sgrState is the subset of SGR the composer gate reads: faint separates a
// dim placeholder from typed text, and reverse marks the terminal caret.
type sgrState struct {
	dim     bool
	reverse bool
}

func applySGR(sgr sgrState, params string) sgrState {
	if params == "" {
		return sgrState{}
	}
	for i := 0; i < len(params); {
		j := i
		for j < len(params) && params[j] >= '0' && params[j] <= '9' {
			j++
		}
		if j == i {
			i++
			continue
		}
		n, _ := strconv.Atoi(params[i:j])
		i = j
		if i < len(params) && params[i] == ';' {
			i++
		}
		switch n {
		case 0:
			sgr = sgrState{}
		case 2:
			sgr.dim = true
		case 7:
			sgr.reverse = true
		case 22:
			sgr.dim = false
		case 27:
			sgr.reverse = false
		case 38, 48:
			if i < len(params) && params[i] == '2' {
				for skip := 0; skip < 3 && i < len(params); skip++ {
					for i < len(params) && params[i] != ';' {
						i++
					}
					if i < len(params) {
						i++
					}
				}
			} else if i < len(params) && params[i] == '5' {
				for i < len(params) && params[i] != ';' {
					i++
				}
				if i < len(params) {
					i++
				}
			}
		}
	}
	return sgr
}

func composerInputRow(capture string) (string, bool) {
	rows, ok := composerBoxRows(capture)
	if !ok || len(rows) == 0 {
		return "", false
	}
	return rows[0], true
}

// composerBoxRows returns every content line between a half-block top border
// and its matching bottom border. Cursor wraps long in-box typing across
// multiple rows; a single-line read misses the gate (muxa#139).
func composerBoxRows(capture string) ([]string, bool) {
	lines := strings.Split(capture, "\n")
	for i := 0; i < len(lines); i++ {
		if !isComposerBorderTop(lines[i]) {
			continue
		}
		var rows []string
		for j := i + 1; j < len(lines); j++ {
			if isComposerBorderBottom(lines[j]) {
				if len(rows) == 0 {
					return nil, false
				}
				return rows, true
			}
			rows = append(rows, lines[j])
		}
	}
	return nil, false
}

func isComposerBorderTop(line string) bool {
	vis := strings.TrimSpace(string(visibleRunes(line)))
	if vis == "" {
		return false
	}
	hasTop := false
	for _, r := range vis {
		switch r {
		case '▄':
			hasTop = true
		case ' ', '\t':
		default:
			return false
		}
	}
	return hasTop
}

func isComposerBorderBottom(line string) bool {
	vis := strings.TrimSpace(string(visibleRunes(line)))
	if vis == "" {
		return false
	}
	hasBot := false
	for _, r := range vis {
		switch r {
		case '▀':
			hasBot = true
		case ' ', '\t':
		default:
			return false
		}
	}
	return hasBot
}

// pasteInComposer reports a collapsed paste placeholder sitting in the
// *composer box*. unsubmittedPasteVisible matches the whole capture, which is
// right for the one-beat confirm but wrong for a watch that outlives the
// turn start: a submitted brief keeps its "[Pasted text …]" line on screen as
// the user turn in the transcript, and judging that as unsubmitted would
// reintroduce muxa#142 from the other end. Panes with no box fall back to the
// whole capture, which is what muxa#111 shipped.
func pasteInComposer(capture string) bool {
	rows, ok := composerBoxRows(capture)
	if !ok {
		return unsubmittedPasteVisible(capture)
	}
	for _, row := range rows {
		if strings.Contains(stripANSI(row), "[Pasted text") {
			return true
		}
	}
	return false
}

func unsubmittedPasteVisible(capture string) bool {
	return strings.Contains(stripANSI(capture), "[Pasted text")
}

func agentTurnStarted(capture string) bool {
	plain := stripANSI(capture)
	if strings.Contains(plain, "ctrl+c to stop") || strings.Contains(plain, "ctrl+c to interrupt") {
		return true
	}
	if row, ok := composerInputRow(capture); ok {
		plainRow := strings.TrimSpace(stripANSI(row))
		if strings.Contains(plainRow, "Add a follow-up") {
			return true
		}
	}
	return false
}
