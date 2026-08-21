package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestControlModeAttachSilenceAndResize(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	sock := fmt.Sprintf("mctl%d", os.Getpid())
	defer func() { _ = exec.Command("tmux", "-L", sock, "kill-server").Run() }()
	if err := exec.Command("tmux", "-L", sock, "new-session", "-d", "-s", "ctl", "-x", "80", "-y", "12", "-n", "p",
		"while true; do echo tick; sleep 0.25; done").Run(); err != nil {
		t.Fatal(err)
	}
	tm := &TMUX{Bin: "tmux", Socket: sock, Run: nil}
	tm.Run = tm.exec
	before, err := tm.fmt("%0", "#{window_width}x#{window_height}")
	if err != nil {
		t.Fatal(err)
	}

	h := NewControlHub(tm, 80*time.Millisecond)
	stop := make(chan struct{})
	defer close(stop)
	go h.Run(stop)

	deadline := time.Now().Add(3 * time.Second)
	for !h.Live() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !h.Live() {
		t.Fatal("control-mode never attached")
	}
	deadline = time.Now().Add(2 * time.Second)
	for !h.Drawing("%0") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !h.Drawing("%0") {
		t.Fatal("ticking pane never showed up in control-mode output")
	}
	deadline = time.Now().Add(2 * time.Second)
	for len(h.DrawingPanes()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	after, err := tm.fmt("%0", "#{window_width}x#{window_height}")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("ignore-size failed: before=%s after=%s", before, after)
	}
	if !strings.HasPrefix(before, "80x") {
		t.Fatalf("unexpected size %s", before)
	}
	panes := h.DrawingPanes()
	found := false
	for _, p := range panes {
		if p == "%0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("DrawingPanes=%v", panes)
	}
}

func TestControlModeReconnectAfterKill(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	sock := fmt.Sprintf("mctlR%d", os.Getpid())
	start := func() {
		if err := exec.Command("tmux", "-L", sock, "new-session", "-d", "-s", "ctl", "-x", "80", "-y", "8", "-n", "p",
			"while true; do echo ping; sleep 0.2; done").Run(); err != nil {
			t.Fatal(err)
		}
	}
	start()
	defer func() { _ = exec.Command("tmux", "-L", sock, "kill-server").Run() }()

	tm := &TMUX{Bin: "tmux", Socket: sock}
	tm.Run = tm.exec
	h := NewControlHub(tm, 80*time.Millisecond)
	stop := make(chan struct{})
	defer close(stop)
	go h.Run(stop)

	waitLive := func() bool {
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if h.Live() && h.Drawing("%0") {
				return true
			}
			time.Sleep(30 * time.Millisecond)
		}
		return false
	}
	if !waitLive() {
		t.Fatal("initial attach failed")
	}
	if err := exec.Command("tmux", "-L", sock, "kill-server").Run(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.Live() && time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
	}
	start()
	if !waitLive() {
		t.Fatal("did not reconnect after tmux server restart")
	}
}
