package main

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// composerInputForeign reports whether an agent-CLI composer holds text the
// broker did not put there. Two-signal is blind to in-box typing (muxa#79);
// this is the safety gate for defect 3 in muxa#111, and since muxa#147 it is
// the only thing standing between a half-typed operator draft and a paste on
// a Claude Code pane.
//
// Two composer shapes are recognised, because the CLIs draw different boxes:
// Cursor Agent's half-block box (▄ … ▀) and Claude Code's rule box (── … ──).
// Until muxa#147 only the half-block shape was, so every Claude pane — the
// operator's own root pane included — fell through to the "[Pasted text"
// substring, which sees a collapsed paste placeholder and nothing else. A
// draft typed by hand was invisible to this gate.
func composerInputForeign(capture string) bool {
	if rows, ok := composerBoxRows(capture); ok {
		for _, row := range rows {
			if composerRowForeign(row) {
				return true
			}
		}
		return false
	}
	if rows, ok := ruleComposerRows(capture); ok {
		// A collapsed paste placeholder is checked whole-capture as well:
		// the rule box is bounded by what Claude drew this frame, and an
		// unsubmitted brief that scrolled out of it is still unsubmitted.
		if unsubmittedPasteVisible(capture) {
			return true
		}
		for _, row := range rows {
			if ruleComposerRowForeign(row) {
				return true
			}
		}
		return false
	}
	return strings.Contains(stripANSI(capture), "[Pasted text")
}

// ruleComposerPrompt is the marker Claude Code paints at the head of its
// composer's first row. It is what separates the composer's rule pair from
// any other pair of horizontal rules an agent may have printed.
const ruleComposerPrompt = "\u276f"

// ruleComposerMaxRows bounds how tall a rule box may be and still be read as
// a composer. Claude grows the box with the draft, but a pair of rules with
// a whole screen between them is page chrome, not an input field.
const ruleComposerMaxRows = 16

// ruleComposerMinWidth is the shortest run of ─ taken for a composer rule.
// Claude draws the rule across the full pane; a short one is table art.
const ruleComposerMinWidth = 8

// ruleComposerRows returns the content rows of a Claude Code composer: the
// lines between the last pair of full-width ─ rules, when the first of them
// opens with the ❯ prompt marker.
//
// Searching from the bottom is deliberate. The composer is the last thing
// Claude draws above its cwd/model footer, and agent output above it may
// contain rules of its own; the footer rows contain none.
func ruleComposerRows(capture string) ([]string, bool) {
	lines := strings.Split(capture, "\n")
	bot := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isComposerRule(lines[i]) {
			bot = i
			break
		}
	}
	if bot <= 0 {
		return nil, false
	}
	top := -1
	for i := bot - 1; i >= 0 && bot-i <= ruleComposerMaxRows+1; i-- {
		if isComposerRule(lines[i]) {
			top = i
			break
		}
	}
	if top < 0 || bot-top < 2 {
		return nil, false
	}
	rows := lines[top+1 : bot]
	if !strings.HasPrefix(strings.TrimSpace(stripANSI(rows[0])), ruleComposerPrompt) {
		return nil, false
	}
	return rows, true
}

// isComposerRule reports whether a line is one of the horizontal rules that
// bound Claude Code's composer: only ─ and padding, and long enough not to be
// a fragment of table or markdown art.
func isComposerRule(line string) bool {
	vis := strings.TrimSpace(string(visibleRunes(line)))
	n := 0
	for _, r := range vis {
		switch r {
		case '\u2500':
			n++
		case ' ', '\t':
		default:
			return false
		}
	}
	return n >= ruleComposerMinWidth
}

// ruleComposerRowForeign reports whether one row of a Claude Code composer
// holds text the broker did not put there.
//
// The faint discriminator composerRowForeign uses is a Cursor fact and does
// not transfer: Claude dims its empty prompt with a 256-colour code (38;5;246)
// and draws typed text in the default foreground (39), so both rows are
// "non-faint" and reading them that way would refuse every paste into every
// Claude pane. What separates them is content: with the ❯ stripped, an empty
// composer has none.
func ruleComposerRowForeign(row string) bool {
	plain := strings.TrimSpace(stripANSI(row))
	plain = strings.TrimSpace(strings.TrimPrefix(plain, ruleComposerPrompt))
	if plain == "" {
		return false
	}
	if strings.Contains(plain, "[Pasted text") {
		return true
	}
	return !composerIdlePlaceholder(plain)
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
