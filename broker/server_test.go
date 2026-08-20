package main

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestEnqueueAndStatus(t *testing.T) {
	dir := t.TempDir()
	q, err := OpenQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "b.sock")
	s := &Server{Sock: sock, Q: q, Deadline: time.Minute}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	resp := rpc(t, sock, Request{Op: "enqueue", Pane: "%9", From: "c", To: "p", Text: "hi", ID: "e1"})
	if !resp.OK || resp.ID != "e1" || resp.State != "queued" {
		t.Fatalf("enqueue: %+v", resp)
	}
	st := rpc(t, sock, Request{Op: "status"})
	if !st.OK || st.Queued != 1 {
		t.Fatalf("status: %+v", st)
	}
	ping := rpc(t, sock, Request{Op: "ping"})
	if !ping.OK || ping.State != "pong" {
		t.Fatalf("ping: %+v", ping)
	}
}

func rpc(t *testing.T, sock string, req Request) Response {
	t.Helper()
	var c net.Conn
	var err error
	for i := 0; i < 50; i++ {
		c, err = net.Dial("unix", sock)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(c)
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}
