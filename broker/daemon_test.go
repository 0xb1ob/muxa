package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDaemonizeGuards(t *testing.T) {
	// The re-executed child and an explicit foreground run must both stay
	// put; otherwise the binary forks forever.
	t.Setenv(daemonEnv, "1")
	if parent, err := daemonize(shortTempDir(t), "/nonexistent.sock", "/nonexistent.pid", filepath.Join(shortTempDir(t), "log")); parent || err != nil {
		t.Fatalf("child re-exec forked again: parent=%v err=%v", parent, err)
	}
	t.Setenv(daemonEnv, "0")
	t.Setenv("MUXA_BROKER_FOREGROUND", "1")
	if parent, err := daemonize(shortTempDir(t), "/nonexistent.sock", "/nonexistent.pid", filepath.Join(shortTempDir(t), "log")); parent || err != nil {
		t.Fatalf("MUXA_BROKER_FOREGROUND ignored: parent=%v err=%v", parent, err)
	}
}

func TestPingSocketOnDeadPath(t *testing.T) {
	if pingSocket(filepath.Join(shortTempDir(t), "nope.sock")) {
		t.Fatal("ping answered on a socket that does not exist")
	}
	if waitSocket(filepath.Join(shortTempDir(t), "nope.sock"), 100*time.Millisecond) {
		t.Fatal("waitSocket succeeded on a socket that does not exist")
	}
}

// shortTempDir returns a scratch dir short enough to hold a unix socket.
// t.TempDir() on darwin lives under /var/folders/… and blows past the
// 104-byte sun_path limit once the test name is in the path.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "muxa-bt-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// buildBroker compiles the broker into a temp dir with the same link mode the
// repo's shell suites use.
func buildBroker(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out := filepath.Join(t.TempDir(), "muxa-broker")
	args := []string{"build"}
	if runtime.GOOS == "darwin" {
		args = append(args, "-ldflags=-linkmode=external")
	}
	args = append(args, "-o", out, ".")
	cmd := exec.Command("go", args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build broker: %v\n%s", err, b)
	}
	if runtime.GOOS == "darwin" {
		// darwin 25+ SIGKILLs provenance-tagged binaries built under a
		// worktree; the shell suites sign the same way.
		_ = exec.Command("xattr", "-c", out).Run()
		_ = exec.Command("codesign", "-s", "-", "--force", "--timestamp=none", out).Run()
	}
	return out
}

// TestDaemonSurvivesCallerProcessGroupKill is the regression for muxa#41.
//
// `muxa send` starts the broker from whatever shell the calling agent is in.
// nohup + disown left the broker in that shell's process group, so the group
// teardown at the end of the caller's tool call killed it — before its first
// delivery, with the first brief still sitting in pending. The log showed
// nothing but repeated "listening" lines. The daemon must outlive a signal
// aimed at the whole starter group.
func TestDaemonSurvivesCallerProcessGroupKill(t *testing.T) {
	bin := buildBroker(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "broker.sock")
	pidPath := filepath.Join(dir, "broker.pid")

	// Start it the way muxa send does, but in a process group of its own so
	// the test can tear that group down without touching `go test`.
	starter := exec.Command(bin)
	starter.Env = append(os.Environ(),
		"MUXA_BROKER_DIR="+dir,
		"MUXA_BROKER_SOCK="+sock,
		"MUXA_BROKER_PID="+pidPath,
		"MUXA_BROKER_FOREGROUND=0",
		daemonEnv+"=0",
	)
	starter.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if b, err := starter.CombinedOutput(); err != nil {
		t.Fatalf("start broker: %v\n%s", err, b)
	}
	pgid := starter.Process.Pid

	if !waitSocket(sock, 5*time.Second) {
		t.Fatal("daemon never opened the socket")
	}
	pid := readPIDFile(t, pidPath)
	if pid == pgid {
		t.Fatal("pidfile holds the forking parent, not the daemon")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })

	// The whole point: its own process group, so no caller-scoped signal
	// reaches it. setsid makes the daemon a group leader, hence pgid == pid.
	dpgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("getpgid(%d): %v", pid, err)
	}
	if dpgid != pid {
		t.Fatalf("daemon pgid=%d pid=%d: not a group leader, so it did not setsid", dpgid, pid)
	}
	if dpgid == pgid {
		t.Fatalf("daemon stayed in the starter's process group %d", pgid)
	}
	if mine, _ := syscall.Getpgid(0); dpgid == mine {
		t.Fatalf("daemon stayed in the test's process group %d", mine)
	}

	// Signal the starter's entire process group, the way a tool-call teardown does.
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		t.Fatalf("kill process group %d: %v", pgid, err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("daemon %d died with the caller's process group: %v", pid, err)
	}
	if !pingSocket(sock) {
		t.Fatal("daemon stopped answering after the caller's group was killed")
	}

	// SIGHUP must not end it either.
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		t.Fatalf("sighup: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if !pingSocket(sock) {
		t.Fatal("daemon died on SIGHUP")
	}
}

// TestDaemonLogsStrandedPendingOnShutdown proves a deliberate stop accounts
// for what it could not hand over, instead of leaving mail in pending with
// nothing in the log to say so.
func TestDaemonLogsStrandedPendingOnShutdown(t *testing.T) {
	bin := buildBroker(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "broker.sock")
	pidPath := filepath.Join(dir, "broker.pid")
	logPath := filepath.Join(dir, "broker.log")

	// Point the deliverer at a pane no tmux knows, so nothing can be
	// delivered and the message has to stay pending.
	q, err := OpenQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Put(&Msg{
		ID: "stranded-1", Pane: "%99999", From: "a", To: "b", Text: "HELD",
		DeadlineUnix: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	starter := exec.Command(bin)
	starter.Env = append(os.Environ(),
		"MUXA_BROKER_DIR="+dir,
		"MUXA_BROKER_SOCK="+sock,
		"MUXA_BROKER_PID="+pidPath,
		"MUXA_BROKER_LOG="+logPath,
		"MUXA_TMUX_BIN="+filepath.Join(dir, "no-such-tmux"),
		"MUXA_BROKER_FOREGROUND=0",
		daemonEnv+"=0",
	)
	starter.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if b, err := starter.CombinedOutput(); err != nil {
		t.Fatalf("start broker: %v\n%s", err, b)
	}
	if !waitSocket(sock, 5*time.Second) {
		t.Fatal("daemon never opened the socket")
	}
	pid := readPIDFile(t, pidPath)

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("sigterm: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var logs string
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(logPath)
		logs = string(b)
		if strings.Contains(logs, "shutdown signal=") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(logs, "1 pending left in") {
		t.Fatalf("shutdown did not account for stranded mail:\n%s", logs)
	}
	if !strings.Contains(logs, "re-adopted on next start") {
		t.Fatalf("shutdown did not say the queue survives:\n%s", logs)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "stranded-1.json")); err != nil {
		t.Fatalf("pending file was dropped on shutdown: %v", err)
	}

	// A restart must visibly take the queue over again.
	starter2 := exec.Command(bin)
	starter2.Env = starter.Env
	starter2.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if b, err := starter2.CombinedOutput(); err != nil {
		t.Fatalf("restart broker: %v\n%s", err, b)
	}
	if !waitSocket(sock, 5*time.Second) {
		t.Fatal("restarted daemon never opened the socket")
	}
	pid2 := readPIDFile(t, pidPath)
	t.Cleanup(func() { _ = syscall.Kill(pid2, syscall.SIGTERM) })
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(logPath)
		logs = string(b)
		if strings.Contains(logs, "re-adopted 1 pending") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("restart did not re-adopt the pending queue:\n%s", logs)
}

func TestDaemonPrunesStaleQueueOnStartup(t *testing.T) {
	bin := buildBroker(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "broker.sock")
	pidPath := filepath.Join(dir, "broker.pid")
	logPath := filepath.Join(dir, "broker.log")

	q, err := OpenQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	writeQueueJSON(t, filepath.Join(dir, "done", "stale.json"), now.Add(-25*time.Hour))
	writeQueueJSON(t, filepath.Join(dir, "failed", "stale.json"), now.Add(-8*24*time.Hour))
	_ = q

	starter := exec.Command(bin)
	starter.Env = append(os.Environ(),
		"MUXA_BROKER_DIR="+dir,
		"MUXA_BROKER_SOCK="+sock,
		"MUXA_BROKER_PID="+pidPath,
		"MUXA_BROKER_LOG="+logPath,
		"MUXA_TMUX_BIN="+filepath.Join(dir, "no-such-tmux"),
		"MUXA_BROKER_FOREGROUND=0",
		daemonEnv+"=0",
	)
	starter.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if b, err := starter.CombinedOutput(); err != nil {
		t.Fatalf("start broker: %v\n%s", err, b)
	}
	if !waitSocket(sock, 5*time.Second) {
		t.Fatal("daemon never opened the socket")
	}
	pid := readPIDFile(t, pidPath)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })

	deadline := time.Now().Add(5 * time.Second)
	var logs string
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(logPath)
		logs = string(b)
		if strings.Contains(logs, "pruned 1 done, 1 failed") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(logs, "pruned 1 done, 1 failed") {
		t.Fatalf("startup did not log prune pass:\n%s", logs)
	}
	if _, err := os.Stat(filepath.Join(dir, "done", "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("stale done entry was not pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "failed", "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("stale failed entry was not pruned: %v", err)
	}
}

// TestDaemonRefusesSecondOwner is the daemon-side half of the single-owner
// invariant. bin/muxa's start lock is the first line of defence, but it can be
// bypassed — two starters racing a bind, a lock reaped as stale, or someone
// running the binary by hand. A second daemon must not unlink the live owner's
// socket, rebind it, and start racing over pending/.
func TestDaemonRefusesSecondOwner(t *testing.T) {
	bin := buildBroker(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "broker.sock")
	pidPath := filepath.Join(dir, "broker.pid")
	logPath := filepath.Join(dir, "broker.log")

	start := func() ([]byte, error) {
		c := exec.Command(bin)
		c.Env = append(os.Environ(),
			"MUXA_BROKER_DIR="+dir,
			"MUXA_BROKER_SOCK="+sock,
			"MUXA_BROKER_PID="+pidPath,
			"MUXA_BROKER_LOG="+logPath,
			"MUXA_BROKER_FOREGROUND=0",
			daemonEnv+"=0",
		)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return c.CombinedOutput()
	}

	if b, err := start(); err != nil {
		t.Fatalf("first start: %v\n%s", err, b)
	}
	if !waitSocket(sock, 5*time.Second) {
		t.Fatal("first daemon never opened the socket")
	}
	first := readPIDFile(t, pidPath)
	t.Cleanup(func() { _ = syscall.Kill(first, syscall.SIGTERM) })

	// No shell lock anywhere in this path: the binary itself must refuse.
	if b, err := start(); err != nil {
		t.Fatalf("second start should exit 0 once the queue has an owner: %v\n%s", err, b)
	}
	deadline := time.Now().Add(5 * time.Second)
	var logs string
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(logPath)
		logs = string(b)
		if strings.Contains(logs, "already owns") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(logs, "already owns") {
		t.Fatalf("second daemon did not refuse the claimed queue:\n%s", logs)
	}
	if n := strings.Count(logs, "listening"); n != 1 {
		t.Fatalf("want exactly 1 daemon bound, got %d:\n%s", n, logs)
	}
	if got := readPIDFile(t, pidPath); got != first {
		t.Fatalf("pidfile moved from the live owner %d to %d", first, got)
	}
	if err := syscall.Kill(first, 0); err != nil {
		t.Fatalf("first owner %d died: %v", first, err)
	}
	if !pingSocket(sock) {
		t.Fatal("first owner stopped answering after a second start")
	}

	// Once the owner is gone the lock must be free again, or every later
	// start would refuse and sends would fail closed forever.
	_ = syscall.Kill(first, syscall.SIGTERM)
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(first, 0); err != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if b, err := start(); err != nil {
		t.Fatalf("restart after the owner exited: %v\n%s", err, b)
	}
	if !waitSocket(sock, 5*time.Second) {
		b, _ := os.ReadFile(logPath)
		t.Fatalf("no daemon took the freed queue:\n%s", b)
	}
	third := readPIDFile(t, pidPath)
	t.Cleanup(func() { _ = syscall.Kill(third, syscall.SIGTERM) })
	if third == first {
		t.Fatal("pidfile still points at the dead owner")
	}
}

// TestDaemonResolvesRelativeDir covers a relative MUXA_BROKER_DIR. The daemon
// runs with cwd=/ so it does not pin the caller's worktree; if it re-resolved
// the path itself it would try to open /<dir> and die on a read-only root,
// while the parent sat waiting on a socket under the caller's cwd.
func TestDaemonResolvesRelativeDir(t *testing.T) {
	bin := buildBroker(t)
	base := shortTempDir(t)
	if err := os.Mkdir(filepath.Join(base, "reldir"), 0o700); err != nil {
		t.Fatal(err)
	}
	starter := exec.Command(bin)
	starter.Dir = base
	starter.Env = append(withoutEnv(os.Environ(),
		"MUXA_BROKER_DIR", "MUXA_BROKER_SOCK", "MUXA_BROKER_PID", "MUXA_BROKER_LOG",
		"MUXA_BROKER_FOREGROUND", daemonEnv),
		"MUXA_BROKER_DIR=reldir")
	starter.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := starter.CombinedOutput()
	if err != nil {
		t.Fatalf("relative dir start failed: %v\n%s", err, out)
	}
	sock := filepath.Join(base, "reldir", "broker.sock")
	if !waitSocket(sock, 5*time.Second) {
		b, _ := os.ReadFile(filepath.Join(base, "reldir", "broker.log"))
		t.Fatalf("daemon did not open %s\nlog:\n%s", sock, b)
	}
	pid := readPIDFile(t, filepath.Join(base, "reldir", "broker.pid"))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", path)
	return 0
}
