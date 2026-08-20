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
	sock := env("MUXA_BROKER_SOCK", filepath.Join(dir, "broker.sock"))
	pidPath := env("MUXA_BROKER_PID", filepath.Join(dir, "broker.pid"))
	deadline := durationSec("MUXA_BROKER_DEADLINE", 600)
	poll := durationMS("MUXA_BROKER_POLL_MS", 250)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatal(err)
	}
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
	log.Printf("listening %s pid=%d deadline=%s poll=%s", sock, os.Getpid(), deadline, poll)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	close(stop)
	_ = s.Close()
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
