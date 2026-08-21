package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// composerFixture is one tests/fixtures/composer/*.ansi plus optional
// #43 signals: tmux cursor in .meta, and a second capture in .t2.ansi.
// Missing sidecar files are skipped so the muxa#41 LooksFree assertions
// still run on the .ansi alone.
type composerFixture struct {
	File    string
	Capture string
	CursorY int
	CursorX int
	HasMeta bool
	T2      string
	HasT2   bool
	Origin  string
}

func composerFixtureDir() string {
	return filepath.Join("..", "tests", "fixtures", "composer")
}

func loadComposerFixture(t *testing.T, file string) composerFixture {
	t.Helper()
	dir := composerFixtureDir()
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	out := composerFixture{File: file, Capture: string(b)}
	base := strings.TrimSuffix(file, ".ansi")
	if meta, err := os.ReadFile(filepath.Join(dir, base+".meta")); err == nil {
		out.HasMeta = true
		for _, line := range strings.Split(string(meta), "\n") {
			key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			switch key {
			case "cursor_y":
				n, err := strconv.Atoi(val)
				if err != nil {
					t.Fatalf("%s.meta cursor_y=%q: %v", base, val, err)
				}
				out.CursorY = n
			case "cursor_x":
				n, err := strconv.Atoi(val)
				if err != nil {
					t.Fatalf("%s.meta cursor_x=%q: %v", base, val, err)
				}
				out.CursorX = n
			case "origin":
				out.Origin = val
			}
		}
	}
	if t2, err := os.ReadFile(filepath.Join(dir, base+".t2.ansi")); err == nil {
		out.HasT2 = true
		out.T2 = string(t2)
	}
	return out
}

func composerFixtureCases() []struct {
	file string
	free bool
	why  string
} {
	return []struct {
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
		// purpose — the cost is a delayed paste, not lost input.
		{"claude-idle-233.ansi", false, "non-faint box text is indistinguishable from typing"},
		// Busy chrome is not a parser signal. LooksFree must not wait on
		// spinners or interrupt phrases; free-detection is the broker's.
		{"claude-busy.ansi", true, "faint 'esc to interrupt' is chrome, not typed"},
		{"cursor-busy-revcursor.ansi", true, "faint 'ctrl+c to stop' is chrome, not typed"},
		{"cursor-busy-spinner.ansi", true, "spinner above the box is chrome, not typed"},
		{"pi-busy.ansi", true, "spinner above the box, composer looks idle"},
		// Typed: non-faint text between the borders must never be clobbered.
		{"cursor-typed.ansi", false, "human typed 'hello world'"},
		// No composer box → conjunct is vacuously true (free-detection decides).
		{"cursor-trust-dialog.ansi", true, "rounded modal, not a composer"},
		{"shell-prompt.ansi", true, "plain shell prompt, no box"},
		{"vim.ansi", true, "vim insert mode, no box"},
		{"garbage.ansi", true, "truncated escape, no box"},
	}
}

func TestLooksFree(t *testing.T) {
	cases := []struct {
		name    string
		capture string
		free    bool
	}{
		{"empty", "", true},
		{"blank lines", "\n\n  \n", true},
		{"shell prompt is vacuously free", "user@host ~ $", true},
		{"typed shell is vacuously free", "muxa % hello", true},
		{"command output is vacuously free", "hello world", true},
		{"busy string is vacuously free", "esc to interrupt", true},
		{"idle box", "▄▄▄▄▄▄\n \x1b[2m❯\x1b[0m     \n▀▀▀▀▀▀\n", true},
		{"typed in box", "▄▄▄▄▄▄\n hello\n▀▀▀▀▀▀\n", false},
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

// TestLooksFreeComposerFixtures pins the typed-in-box conjunct to real
// `capture-pane -e` output. Busy chrome (spinners, interrupt phrases) must
// not flip the verdict: that is free-detection, not this remnant.
func TestLooksFreeComposerFixtures(t *testing.T) {
	for _, tc := range composerFixtureCases() {
		t.Run(tc.file, func(t *testing.T) {
			fx := loadComposerFixture(t, tc.file)
			if got := LooksFree(fx.Capture); got != tc.free {
				t.Fatalf("LooksFree(%s)=%v want %v (%s)", tc.file, got, tc.free, tc.why)
			}
		})
	}
}

// TestComposerFixtureSignals asserts the shipped corpus has #43 sidecars.
// loadComposerFixture still skips missing files, so the muxa#41 LooksFree
// assertions keep running on the .ansi alone.
func TestComposerFixtureSignals(t *testing.T) {
	for _, tc := range composerFixtureCases() {
		t.Run(tc.file, func(t *testing.T) {
			fx := loadComposerFixture(t, tc.file)
			if !fx.HasMeta {
				t.Fatal("missing .meta (cursor_y/cursor_x)")
			}
			if fx.CursorY < 0 || fx.CursorX < 0 {
				t.Fatalf("cursor_y=%d cursor_x=%d", fx.CursorY, fx.CursorX)
			}
			if !fx.HasT2 {
				t.Fatal("missing .t2.ansi (second capture)")
			}
			if fx.T2 == "" {
				t.Fatal("t2.ansi present but empty")
			}
		})
	}
}

// TestLooksFreeComposerNeedsEscapes documents why Capture passes -e: with the
// attributes stripped, an idle composer is indistinguishable from typed text
// and the pane never looks free.
func TestLooksFreeComposerNeedsEscapes(t *testing.T) {
	fx := loadComposerFixture(t, "cursor-agent-splash.ansi")
	if !LooksFree(fx.Capture) {
		t.Fatal("with escapes: want free")
	}
	if LooksFree(stripANSI(fx.Capture)) {
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
		t.Fatal("busy phrases above the box must not mark an idle composer typed")
	}
}

func TestComposerBusyChromeIsNotAPromptModel(t *testing.T) {
	// Faint interrupt hints are chrome. LooksFree must not use them to
	// decide a pane is at a prompt; that is the broker's job.
	cap := "▄▄▄▄▄▄\n \x1b[2m❯ esc to interrupt\x1b[0m \n▀▀▀▀▀▀\n"
	if !LooksFree(cap) {
		t.Fatal("busy phrases inside the box must not mark typed-in-box")
	}
}

func TestComposerEmptyInputRow(t *testing.T) {
	cap := "▄▄▄▄▄▄\n      \n▀▀▀▀▀▀\n"
	if !LooksFree(cap) {
		t.Fatal("blank composer row must look free")
	}
}

func TestComposerBoxNeedsBothBorders(t *testing.T) {
	// A lone bottom border is ordinary output, not a composer. Without a
	// box the conjunct is vacuously true — check findComposer, not LooksFree.
	if _, _, _, ok := findComposer("some output\nmore output\n▀▀▀▀▀▀\n"); ok {
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
