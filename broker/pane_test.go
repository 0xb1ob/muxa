package main

import "testing"

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
