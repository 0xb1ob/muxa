package main

import (
	"strings"
	"testing"
)

func TestKindFromArgv(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/usr/bin/claude --model haiku", "claude"},
		{"claude", "claude"},
		{"/opt/cursor-agent/node --foo", "cursor"},
		{"/home/x/.npm/claude-projects/cursor-agent/node", "cursor"},
		{"/usr/bin/agent --trust", "cursor"},
		{"agent", "cursor"},
		{"omp", "pi"},
		{"/usr/bin/pi", "pi"},
		{"docker compose up", "generic"},
		{"/usr/bin/component-tool", "generic"},
		{"zsh", "generic"},
		{"sleep 3600", "generic"},
	}
	for _, c := range cases {
		if got := kindFromArgv(c.in); got != c.want {
			t.Errorf("kindFromArgv(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestShlexQuote(t *testing.T) {
	if shlexQuote("") != "''" {
		t.Fatal(shlexQuote(""))
	}
	if shlexQuote("foo") != "foo" {
		t.Fatal(shlexQuote("foo"))
	}
	got := shlexQuote("a'b")
	if got != `'a'\''b'` {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeName(t *testing.T) {
	if sanitizeName("swift-oak") != "swift-oak" {
		t.Fatal(sanitizeName("swift-oak"))
	}
	if sanitizeName("  Bad Name! ") != "Bad-Name" {
		t.Fatal(sanitizeName("  Bad Name! "))
	}
}

func TestPaneWhoState(t *testing.T) {
	if paneWhoState("cursor", "/no/such", "zsh", "%1", nil) != "ghost" {
		t.Fatal("missing cwd should be ghost")
	}
	if paneWhoState("cursor", ".", "zsh", "%1", nil) != "ghost" {
		t.Fatal("cursor at shell should be ghost")
	}
	if paneWhoState("generic", ".", "zsh", "%1", nil) != "idle" {
		t.Fatal("generic at shell should be idle")
	}
	if paneWhoState("generic", ".", "sleep", "%1", []string{"%1"}) != "busy" {
		t.Fatal("drawing pane should be busy")
	}
}

func TestParseRosterLines(t *testing.T) {
	out := strings.Join([]string{
		"%1||bob||abc||||generic||muxa:0.0||/tmp||sleep",
		"%2||alice||def||bob||generic||muxa:0.1||/tmp/acme-\"quote\"\\slash||cat",
		"%3||||||||",
		"",
	}, "\n")
	rows := parseRosterLines(out)
	if len(rows) != 2 {
		t.Fatalf("len=%d want 2", len(rows))
	}
	if rows[0].Name != "bob" || rows[0].Parent != "" || rows[0].Kind != "generic" {
		t.Fatalf("bob: %+v", rows[0])
	}
	if rows[1].Name != "alice" || rows[1].Parent != "bob" || rows[1].Cwd != `/tmp/acme-"quote"\slash` {
		t.Fatalf("alice: %+v", rows[1])
	}
	if _, ok := findPaneByName(rows, "bob"); !ok {
		t.Fatal("bob missing from roster")
	}
}

func TestCanSend(t *testing.T) {
	rows := []rosterEntry{
		{Pane: "%1", Name: "bob", Parent: ""},
		{Pane: "%2", Name: "alice", Parent: "bob"},
		{Pane: "%3", Name: "carol", Parent: "bob"},
		{Pane: "%4", Name: "eve", Parent: ""},
	}
	if !canSend(rows, "bob", "alice") || !canSend(rows, "alice", "bob") {
		t.Fatal("parent↔child")
	}
	if canSend(rows, "alice", "carol") {
		t.Fatal("siblings")
	}
	if canSend(rows, "bob", "eve") {
		t.Fatal("roots")
	}
}

func TestFormatOne(t *testing.T) {
	s := formatOne("bob", "", "hi")
	if !strings.Contains(s, "[muxa] from=bob") || !strings.Contains(s, `muxa send bob`) {
		t.Fatal(s)
	}
	s = formatOne("bob", "no-reply", "hi")
	if !strings.Contains(s, "Do not reply.") {
		t.Fatal(s)
	}
}

func TestLastNContentLines(t *testing.T) {
	in := "a\nb\n\nc\n\n\n"
	got := lastNContentLines(in, 2)
	if got != "\nc\n" {
		t.Fatalf("got %q", got)
	}
}
