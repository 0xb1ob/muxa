package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLooksFree(t *testing.T) {
	cases := []struct {
		name    string
		capture string
		free    bool
	}{
		{"empty", "", true},
		{"blank lines", "\n\n  \n", true},
		{"dollar", "user@host ~ $", true},
		{"percent", "muxa %", true},
		{"hash root", "root@box:/#", true},
		{"gt", ">", true},
		{"gt spaces", ">  ", true},
		{"ready loop", "ready>", true},
		{"zsh with path", "mbaranovski@mac muxa %", true},
		{"ansi prompt", "\x1b[32mready>\x1b[0m", true},
		{"typed after percent", "muxa % hello", false},
		{"typed after gt", "> partial text", false},
		{"typed after ready", "ready> still typing", false},
		{"command output", "hello world", false},
		{"busy string", "esc to interrupt", false},
		{"ctrl-c", "ctrl+c to stop", false},
		{"spinner-ish", "⠋ running", false},
		{"multiline prompt last", "output\nmore\nready>", true},
		{"multiline typed last", "output\nready> abc", false},
		{"trailing empty after prompt", "ready>\n\n", true},
		{"cursor-ish composer", "  > ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LooksFree(tc.capture)
			if got != tc.free {
				t.Fatalf("LooksFree(%q)=%v want %v", tc.capture, got, tc.free)
			}
		})
	}
}

// TestLooksFreeComposerFixtures pins the composer path to real
// `capture-pane -e` output. Every "idle" fixture here used to read as busy,
// which is why a first brief to a freshly spawned agent pane sat in pending
// until the deadline instead of being pasted (muxa#41).
func TestLooksFreeComposerFixtures(t *testing.T) {
	cases := []struct {
		file string
		free bool
		why  string
	}{
		// Idle composers: placeholder only, rendered faint.
		{"cursor-agent-splash.ansi", true, "real Cursor Agent splash, empty composer"},
		{"cursor-idle.ansi", true, "faint 'Add a follow-up' placeholder"},
		{"cursor-revcursor-idle.ansi", true, "block cursor over the placeholder"},
		{"256color-idle.ansi", true, "faint placeholder under a 256-colour theme"},
		{"claude-idle.ansi", true, "bare faint > in the box"},
		{"pi-idle.ansi", true, "bare faint ❯ in the box"},
		// Default-foreground text in the box reads as typed even when it is a
		// hint: this fixture's "Image in clipboard · ctrl+v to paste" carries
		// the same attributes as text typed after a faint ❯. Conservative on
		// purpose — the cost is the deadline fallback, not lost input.
		{"claude-idle-233.ansi", false, "non-faint box text is indistinguishable from typing"},
		// Busy: live interrupt hint in the box, or a spinner beside it.
		{"claude-busy.ansi", false, "'esc to interrupt' inside the box"},
		{"cursor-busy-revcursor.ansi", false, "'ctrl+c to stop' inside the box"},
		{"cursor-busy-spinner.ansi", false, "spinner above the box"},
		{"pi-busy.ansi", false, "spinner above the box, composer looks idle"},
		// Typed: non-faint text between the borders must never be clobbered.
		{"cursor-typed.ansi", false, "human typed 'hello world'"},
		// No composer box → shell fallback.
		{"cursor-trust-dialog.ansi", false, "rounded modal, not a composer"},
		{"shell-prompt.ansi", true, "plain shell prompt"},
		{"vim.ansi", false, "vim insert mode"},
		{"garbage.ansi", false, "truncated escape, no prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "tests", "fixtures", "composer", tc.file))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			if got := LooksFree(string(b)); got != tc.free {
				t.Fatalf("LooksFree(%s)=%v want %v (%s)", tc.file, got, tc.free, tc.why)
			}
		})
	}
}

// TestLooksFreeComposerNeedsEscapes documents why Capture passes -e: with the
// attributes stripped, an idle composer is indistinguishable from typed text
// and the pane never looks free.
func TestLooksFreeComposerNeedsEscapes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "tests", "fixtures", "composer", "cursor-agent-splash.ansi"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !LooksFree(string(b)) {
		t.Fatal("with escapes: want free")
	}
	if LooksFree(stripANSI(string(b))) {
		t.Fatal("without escapes the placeholder is unreadable; capture-pane must use -e")
	}
}

func TestComposerTypedOneCharUnderCursor(t *testing.T) {
	// A single typed char sitting under the block cursor has no faint run to
	// mark it as a placeholder, so it must count as typed.
	cap := "▄▄▄▄▄▄\n \x1b[0;7mX\x1b[0m     \n▀▀▀▀▀▀\n"
	if LooksFree(cap) {
		t.Fatal("one typed char under the cursor must not look free")
	}
}

// TestComposerIgnoresBusyScrollback isolates the rule the claude-idle-233
// fixture is built around: an interrupt hint only means "busy" when the
// composer itself is showing it. Above the box it is transcript text from a
// finished turn and must not stall delivery for the whole deadline.
func TestComposerIgnoresBusyScrollback(t *testing.T) {
	cap := "  tip: esc to interrupt only applies while a turn is running\n" +
		"  (older status) ctrl+c to stop was shown during the prior turn\n" +
		"▄▄▄▄▄▄\n \x1b[2m❯\x1b[0m     \n▀▀▀▀▀▀\n"
	if !LooksFree(cap) {
		t.Fatal("busy phrases above the box must not mark an idle composer busy")
	}
}

func TestComposerBusyPhraseInsideBox(t *testing.T) {
	cap := "▄▄▄▄▄▄\n \x1b[2m❯ esc to interrupt\x1b[0m \n▀▀▀▀▀▀\n"
	if LooksFree(cap) {
		t.Fatal("interrupt hint inside the box means a turn is running")
	}
}

func TestComposerEmptyInputRow(t *testing.T) {
	cap := "▄▄▄▄▄▄\n      \n▀▀▀▀▀▀\n"
	if !LooksFree(cap) {
		t.Fatal("blank composer row must look free")
	}
}

func TestComposerBoxNeedsBothBorders(t *testing.T) {
	// A lone bottom border is ordinary output, not a composer.
	if LooksFree("some output\nmore output\n▀▀▀▀▀▀\n") {
		t.Fatal("bottom border alone must not be read as a composer")
	}
}

func TestApplySGRTruecolorIsNotDim(t *testing.T) {
	// Composer borders are drawn with 38;2;r;g;b. Reading the "2" as SGR 2
	// would mark every border and every typed line faint, so a typed
	// composer would look free and get pasted over.
	dim, rev := applySGR("38;2;38;38;38", false, false)
	if dim || rev {
		t.Fatalf("truecolor fg set dim=%v rev=%v", dim, rev)
	}
	if dim, _ = applySGR("48;2;18;18;18", false, false); dim {
		t.Fatal("truecolor bg read as dim")
	}
	if dim, _ = applySGR("38;5;2", false, false); dim {
		t.Fatal("256-colour index read as dim")
	}
}

func TestApplySGRAttributes(t *testing.T) {
	if dim, _ := applySGR("2", false, false); !dim {
		t.Fatal("SGR 2 must set dim")
	}
	if _, rev := applySGR("7", false, false); !rev {
		t.Fatal("SGR 7 must set reverse")
	}
	if dim, rev := applySGR("0;2", true, true); !dim || rev {
		t.Fatalf("0;2 → dim=%v rev=%v want true,false", dim, rev)
	}
	if dim, rev := applySGR("0;7", true, true); dim || !rev {
		t.Fatalf("0;7 → dim=%v rev=%v want false,true", dim, rev)
	}
	if dim, rev := applySGR("", true, true); dim || rev {
		t.Fatal("bare ESC[m must reset")
	}
	if dim, _ := applySGR("22", true, false); dim {
		t.Fatal("SGR 22 must clear dim")
	}
	if _, rev := applySGR("27", false, true); rev {
		t.Fatal("SGR 27 must clear reverse")
	}
}

func TestAttrCellsSkipsOSC(t *testing.T) {
	cells := attrCells("\x1b]0;title\x07\x1b[2mab\x1b[0mc")
	var got string
	dim := map[rune]bool{}
	for _, c := range cells {
		got += string(c.r)
		dim[c.r] = c.dim
	}
	if got != "abc" {
		t.Fatalf("visible=%q want %q", got, "abc")
	}
	if !dim['a'] || !dim['b'] || dim['c'] {
		t.Fatalf("dim map wrong: %v", dim)
	}
}
