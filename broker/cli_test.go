package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONObjectEscapesQuoteBackslashNewline(t *testing.T) {
	cwd := "proj/\"quote\"\\slash"
	payload := "line1\"quote\"\\\nline2"
	out := runCLIWithArgs(t, nil, "json-object",
		"cwd", cwd,
		"text", payload,
		"name", "alice",
	)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if obj["cwd"] != cwd {
		t.Fatalf("cwd: got %q want %q", obj["cwd"], cwd)
	}
	if obj["text"] != payload {
		t.Fatalf("text: got %q want %q", obj["text"], payload)
	}
	if !bytes.Contains(out, []byte(`\"quote\"`)) {
		t.Fatalf("expected escaped quotes in JSON: %s", out)
	}
	if !bytes.Contains(out, []byte(`\\`)) {
		t.Fatalf("expected escaped backslash in JSON: %s", out)
	}
	if !bytes.Contains(out, []byte(`\n`)) {
		t.Fatalf("expected escaped newline in JSON: %s", out)
	}
}

func TestJSONObjectNullKeys(t *testing.T) {
	out := runCLIWithArgs(t, nil, "json-object",
		"--null", "parent", "--null", "session",
		"name", "bob", "parent", "ignored", "state", "idle",
	)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["name"] != "bob" || obj["state"] != "idle" {
		t.Fatalf("%s", out)
	}
	if obj["parent"] != nil || obj["session"] != nil {
		t.Fatalf("parent/session want null: %s", out)
	}
}

func TestWhoJSONShapeAndNulls(t *testing.T) {
	in := "%1||alice||abc123||bob||generic||muxa:1.0||/tmp/proj||sleep||live||idle\n" +
		"%2||bob||def456||||generic||muxa:0.0||/tmp||sleep||live||idle\n"
	out := runCLIWithArgs(t, []byte(in), "who-json")
	var rows []map[string]any
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	need := []string{"name", "id", "parent", "kind", "state", "pane", "session", "cwd", "status"}
	for _, row := range rows {
		if len(row) != len(need) {
			t.Fatalf("keys=%v", keysOf(row))
		}
		for _, k := range need {
			if _, ok := row[k]; !ok {
				t.Fatalf("missing %s", k)
			}
		}
	}
	if rows[0]["parent"] != "bob" || rows[0]["session"] != nil {
		t.Fatalf("alice: %+v", rows[0])
	}
	if rows[1]["parent"] != nil || rows[1]["session"] != nil {
		t.Fatalf("bob: %+v", rows[1])
	}
	for _, row := range rows {
		st, _ := row["state"].(string)
		if st != "idle" && st != "busy" {
			t.Fatalf("state must be idle|busy, not %q (blocked is gone)", st)
		}
		status, _ := row["status"].(string)
		if status != "live" && status != "drawing" && status != "ghost" {
			t.Fatalf("status must be live|drawing|ghost, not %q", status)
		}
		if row["session"] != nil {
			t.Fatalf("session must be null: %+v", row)
		}
	}
}

func TestWhoJSONEscapesCwd(t *testing.T) {
	cwd := `/tmp/acme-"quote"\slash`
	in := "%9||projagent||ididid||||generic||s:1.0||" + cwd + "||sleep||live||idle\n"
	out := runCLIWithArgs(t, []byte(in), "who-json")
	var rows []map[string]any
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if rows[0]["cwd"] != cwd {
		t.Fatalf("cwd got %q", rows[0]["cwd"])
	}
}

func TestJSONArrayEmpty(t *testing.T) {
	out := runCLIWithArgs(t, nil, "json-array")
	if strings.TrimSpace(string(out)) != "[]" {
		t.Fatalf("got %q", out)
	}
}

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

func TestClientPingEnqueue(t *testing.T) {
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

	t.Setenv("MUXA_BROKER_SOCK", sock)
	if rc := cliPing(); rc != 0 {
		t.Fatalf("ping rc=%d", rc)
	}
	old := os.Stdin
	defer func() { os.Stdin = old }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	go func() {
		_, _ = w.Write([]byte("hello \"quote\"\\\nand more"))
		_ = w.Close()
	}()
	rc := cliEnqueue([]string{"--id", "e1", "--pane", "%9", "--from", "c", "--to", "p"})
	if rc != 0 {
		t.Fatalf("enqueue rc=%d", rc)
	}
	st := rpc(t, sock, Request{Op: "status"})
	if !st.OK || st.Queued != 1 {
		t.Fatalf("status after enqueue: %+v", st)
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
		_, _ = ioDiscardCopy(rOut)
		close(done)
	}()
	rc := runCLI(args)
	_ = wOut.Close()
	<-done
	os.Stdin, os.Stdout = oldIn, oldOut
	_ = rIn.Close()
	return rc
}

func ioDiscardCopy(r *os.File) (int64, error) {
	return bytes.NewBuffer(nil).ReadFrom(r)
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
