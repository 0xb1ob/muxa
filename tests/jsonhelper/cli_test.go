package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestJSONGetArrayAndObject(t *testing.T) {
	arr := []byte(`[{"name":"alice","parent":"bob","session":null},{"name":"bob","to":"carol","pane":"%7"}]`)
	got := strings.TrimSpace(string(runCLIWithArgs(t, arr, "json-get", "alice", "parent")))
	if got != "bob" {
		t.Fatalf("alice.parent=%q", got)
	}
	if rc := runCLIExit(t, arr, "json-get", "alice", "session"); rc != 3 {
		t.Fatalf("alice.session null exit=%d", rc)
	}
	got = strings.TrimSpace(string(runCLIWithArgs(t, arr, "json-get", "bob", "pane")))
	if got != "%7" {
		t.Fatalf("bob.pane via to=%q", got)
	}
	obj := []byte(`{"id":"x","pane":"%3","from":"bob","to":"carol"}`)
	got = strings.TrimSpace(string(runCLIWithArgs(t, obj, "json-get", "pane")))
	if got != "%3" {
		t.Fatalf("object pane=%q", got)
	}
}

func TestJSONKeys(t *testing.T) {
	obj := []byte(`{"id":"x","pane":"%3","from":"bob","to":"carol"}`)
	if rc := runCLIExit(t, obj, "json-keys", "id", "pane", "from", "to"); rc != 0 {
		t.Fatalf("exact keys rc=%d", rc)
	}
	if rc := runCLIExit(t, obj, "json-keys", "id", "pane"); rc != 1 {
		t.Fatalf("subset of keys must fail exact match, rc=%d", rc)
	}
}

func runCLIWithArgs(t *testing.T, stdin []byte, args ...string) []byte {
	t.Helper()
	oldIn, oldOut := os.Stdin, os.Stdout
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin, os.Stdout = rIn, wOut
	if stdin != nil {
		go func() {
			_, _ = wIn.Write(stdin)
			_ = wIn.Close()
		}()
	} else {
		_ = wIn.Close()
	}
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = buf.ReadFrom(rOut)
		close(done)
	}()
	rc := runCLI(args)
	_ = wOut.Close()
	<-done
	os.Stdin, os.Stdout = oldIn, oldOut
	_ = rIn.Close()
	if rc != 0 {
		t.Fatalf("runCLI %v rc=%d out=%s", args, rc, buf.Bytes())
	}
	return buf.Bytes()
}

func runCLIExit(t *testing.T, stdin []byte, args ...string) int {
	t.Helper()
	oldIn, oldOut := os.Stdin, os.Stdout
	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin, os.Stdout = rIn, wOut
	if stdin != nil {
		go func() {
			_, _ = wIn.Write(stdin)
			_ = wIn.Close()
		}()
	} else {
		_ = wIn.Close()
	}
	done := make(chan struct{})
	go func() {
		_, _ = bytes.NewBuffer(nil).ReadFrom(rOut)
		close(done)
	}()
	rc := runCLI(args)
	_ = wOut.Close()
	<-done
	os.Stdin, os.Stdout = oldIn, oldOut
	_ = rIn.Close()
	return rc
}
