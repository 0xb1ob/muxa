package main

import (
	"strings"
	"unicode"
)

// composerInputForeign reports whether an agent-CLI composer holds text the
// broker did not put there. Two-signal is blind to in-box typing (muxa#79);
// this is the dispatch safety gate for defect 3 in muxa#111.
func composerInputForeign(capture string) bool {
	if row, ok := composerInputRow(capture); ok {
		return composerRowForeign(row)
	}
	return strings.Contains(stripANSI(capture), "[Pasted text")
}

func composerRowForeign(row string) bool {
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

func composerInputRow(capture string) (string, bool) {
	lines := strings.Split(capture, "\n")
	for i := 0; i+2 < len(lines); i++ {
		if !isComposerBorderTop(lines[i]) || !isComposerBorderBottom(lines[i+2]) {
			continue
		}
		return lines[i+1], true
	}
	return "", false
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
