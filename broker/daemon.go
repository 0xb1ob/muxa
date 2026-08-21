package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// daemonEnv marks the re-executed child so it never forks a second time.
const daemonEnv = "MUXA_BROKER_DAEMONIZED"

// daemonize re-executes this binary in a fresh session and returns true in
// the parent, which must then exit without touching the queue.
//
// Why the binary has to do this itself: `muxa send` starts the broker from
// whatever shell the calling agent happens to be in. `nohup … & disown` only
// hides the job from that shell's job table — the child keeps the *caller's*
// process group, so one group-directed signal when the caller's tool call
// ends (agent CLI teardown, tmux run-shell, hook wrapper, shell exit with job
// control) takes the broker down with it, mid-queue and before its first
// delivery. macOS ships no setsid(1), so the shell cannot break the group on
// its own. setsid(2) here gives the daemon its own session and process group,
// which no caller-scoped signal can reach.
//
// MUXA_BROKER_FOREGROUND=1 keeps the process in place for tests and for
// running under a supervisor.
func daemonize(dir, sock, pidPath, logPath string) (parent bool, err error) {
	if os.Getenv(daemonEnv) == "1" || os.Getenv("MUXA_BROKER_FOREGROUND") == "1" {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	defer logf.Close()
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer devnull.Close()

	// Replace, never append: with a duplicate key the child's os.Getenv
	// returns the *first* entry, so an inherited MUXA_BROKER_DAEMONIZED=0
	// would win and the child would fork again, forever.
	//
	// The already-resolved paths are handed down too. The child runs with
	// cwd=/, so if it re-resolved a relative MUXA_BROKER_DIR it would land
	// somewhere else than the parent — and than the socket the parent waits on.
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(withoutEnv(os.Environ(),
		daemonEnv,
		"MUXA_BROKER_FOREGROUND",
		"MUXA_BROKER_DIR",
		"MUXA_BROKER_SOCK",
		"MUXA_BROKER_PID",
		"MUXA_BROKER_LOG",
	),
		daemonEnv+"=1",
		"MUXA_BROKER_DIR="+dir,
		"MUXA_BROKER_SOCK="+sock,
		"MUXA_BROKER_PID="+pidPath,
		"MUXA_BROKER_LOG="+logPath,
	)
	// Do not pin the caller's cwd; a worktree must stay removable.
	cmd.Dir = "/"
	cmd.Stdin = devnull
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	// Block until the child is actually answering, so `muxa broker start`
	// and the auto-start in `muxa send` are synchronous: a zero exit here
	// means the queue has an owner.
	if !waitSocket(sock, 5*time.Second) {
		return true, fmt.Errorf("daemon pid=%d did not open %s", cmd.Process.Pid, sock)
	}
	fmt.Printf("muxa-broker: daemon pid=%d sock=%s\n", cmd.Process.Pid, sock)
	return true, nil
}

// lockQueue takes an exclusive non-blocking flock on path and returns the open
// file, which the caller must keep for the process lifetime.
//
// One queue gets one owner. Concurrent starters — from ensure_broker, a manual
// run, or different shells — each fork a daemon; one wins this flock, the rest
// exit 0. Without it, a second daemon unlinks the live socket, rebinds it, and
// races the first over pending/ — pastes go missing or land twice, and the log
// shows two "listening" lines and no explanation. The kernel releases an flock
// when the fd closes, so this also clears on SIGKILL, where no defer runs.
//
// The lock lives on the inode, so nothing may unlink this file while a broker
// is running — a replacement would be a fresh inode and lock cleanly.
func lockQueue(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// withoutEnv drops every KEY=… entry for the named keys.
func withoutEnv(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		drop := false
		for _, k := range keys {
			if strings.HasPrefix(kv, k+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// waitSocket reports whether the daemon answered a ping before d elapsed.
func waitSocket(sock string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if pingSocket(sock) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func pingSocket(sock string) bool {
	c, err := net.DialTimeout("unix", sock, 250*time.Millisecond)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := c.Write([]byte(`{"op":"ping"}` + "\n")); err != nil {
		return false
	}
	buf := make([]byte, 256)
	n, err := c.Read(buf)
	return err == nil && n > 0
}
