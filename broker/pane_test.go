package main

import (
	"testing"
)

func TestApplySGRTruecolorIsNotDim(t *testing.T) {
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
