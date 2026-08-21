package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("muxa-broker: ")

	dir := env("MUXA_BROKER_DIR", "")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "muxa-broker: MUXA_BROKER_DIR is required")
		os.Exit(2)
	}
	// Resolve before forking: the daemon runs with cwd=/ so it does not pin
	// the caller's worktree, which would make a relative path point somewhere
	// else in the child than in the parent.
	dir = abs(dir)
	sock := abs(env("MUXA_BROKER_SOCK", filepath.Join(dir, "broker.sock")))
	pidPath := abs(env("MUXA_BROKER_PID", filepath.Join(dir, "broker.pid")))
	logPath := abs(env("MUXA_BROKER_LOG", filepath.Join(dir, "broker.log")))
	deadline := durationSec("MUXA_BROKER_DEADLINE", 600)
	poll := durationMS("MUXA_BROKER_POLL_MS", 250)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatal(err)
	}
	// Detach into our own session before opening the queue, so a signal
	// aimed at the caller of `muxa send` can never reach the owner of it.
	parent, err := daemonize(dir, sock, pidPath, logPath)
	if err != nil {
		log.Fatal(err)
	}
	if parent {
		return
	}
	// The session leader has no controlling terminal to be hung up on, but
	// be explicit: a stray SIGHUP must not end the queue.
	signal.Ignore(syscall.SIGHUP)

	// Claim the queue before touching the socket or the pidfile, so a second
	// daemon cannot unlink a live owner's socket and start racing it over
	// pending/. Exit 0: the queue has an owner, which is all the caller wanted.
	owner, err := lockQueue(filepath.Join(dir, "owner.lock"))
	if err != nil {
		log.Printf("another broker already owns %s (%v); exiting", dir, err)
		return
	}
	defer owner.Close()

	q, err := OpenQueue(dir)
	if err != nil {
		log.Fatal(err)
	}
	if err := writePID(pidPath); err != nil {
		log.Fatal(err)
	}
	defer os.Remove(pidPath)

	s := &Server{Sock: sock, Q: q, Deadline: deadline}
	if err := s.Listen(); err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	d := NewDeliverer(q, NewTMUX(), poll)
	stop := make(chan struct{})
	go d.Loop(stop)
	go s.Serve()
	log.Printf("listening %s pid=%d pgid=%d deadline=%s poll=%s",
		sock, os.Getpid(), processGroup(), deadline, poll)
	// A restart re-adopts whatever the previous owner had not delivered;
	// say so, so "queued" with no "delivered" is never a silent hole.
	if n, _, _, err := q.Counts(); err == nil && n > 0 {
		log.Printf("re-adopted %d pending from %s", n, filepath.Join(dir, "pending"))
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	sig := <-ch
	close(stop)
	_ = s.Close()
	// Hand over anything the target panes will take right now rather than
	// leaving it for an unknown restart, then account for the rest. Pending
	// files are durable, so nothing is lost either way — but it must be in
	// the log, not inferred from an empty tail.
	d.Tick()
	if n, _, _, err := q.Counts(); err != nil {
		log.Printf("shutdown signal=%s: queue count failed: %v", sig, err)
	} else if n > 0 {
		log.Printf("shutdown signal=%s: %d pending left in %s (re-adopted on next start)",
			sig, n, filepath.Join(dir, "pending"))
	} else {
		log.Printf("shutdown signal=%s: queue drained", sig)
	}
}

// processGroup reports our process group, so the log proves the daemon
// detached from whichever shell ran `muxa send`: after setsid it leads its own
// group, so pgid == pid. syscall.Getsid is darwin-only, and the group is the
// thing a caller-scoped signal would target anyway.
func processGroup() int {
	pgid, err := syscall.Getpgid(0)
	if err != nil {
		return 0
	}
	return pgid
}

// abs resolves p against the current cwd, leaving it alone if that fails.
func abs(p string) string {
	if p == "" {
		return p
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func durationSec(k string, def int) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return time.Duration(def) * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return time.Duration(def) * time.Second
	}
	return time.Duration(n) * time.Second
}

func durationMS(k string, def int) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return time.Duration(def) * time.Millisecond
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return time.Duration(def) * time.Millisecond
	}
	return time.Duration(n) * time.Millisecond
}

func writePID(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.WriteString(f, strconv.Itoa(os.Getpid())+"\n")
	return err
}
