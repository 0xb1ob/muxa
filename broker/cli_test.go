package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJSONEscapesQuoteBackslashNewline(t *testing.T) {
	cwd := "proj/\"quote\"\\slash"
	payload := "line1\"quote\"\\\nline2"
	out := captureJSON(t, map[string]any{
		"cwd": cwd, "text": payload, "name": "alice",
	})
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

func TestWhoJSONShapeAndNulls(t *testing.T) {
	rows := []whoJSONRow{
		{Name: "alice", ID: "abc123", Parent: strPtr("bob"), Kind: "generic", State: "idle", Pane: "%1", Cwd: "/tmp/proj"},
		{Name: "bob", ID: "def456", Parent: nil, Kind: "generic", State: "busy", Pane: "%2", Cwd: "/tmp"},
		{Name: "zed", ID: "aaa111", Parent: strPtr("bob"), Kind: "cursor", State: "ghost", Pane: "%3", Cwd: "/gone"},
	}
	out := captureJSON(t, rows)
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(got) != 3 {
		t.Fatalf("rows=%d", len(got))
	}
	need := []string{"name", "id", "parent", "kind", "state", "pane", "session", "cwd"}
	for _, row := range got {
		if len(row) != len(need) {
			t.Fatalf("keys=%v", keysOf(row))
		}
		for _, k := range need {
			if _, ok := row[k]; !ok {
				t.Fatalf("missing %s", k)
			}
		}
		if _, ok := row["status"]; ok {
			t.Fatalf("status must not be present: %+v", row)
		}
	}
	if got[0]["parent"] != "bob" || got[0]["session"] != nil {
		t.Fatalf("alice: %+v", got[0])
	}
	if got[1]["parent"] != nil || got[1]["session"] != nil {
		t.Fatalf("bob: %+v", got[1])
	}
	if got[2]["state"] != "ghost" {
		t.Fatalf("zed state: %+v", got[2])
	}
	for _, row := range got {
		st, _ := row["state"].(string)
		if st != "idle" && st != "busy" && st != "ghost" {
			t.Fatalf("state must be idle|busy|ghost, not %q", st)
		}
		if row["session"] != nil {
			t.Fatalf("session must be null: %+v", row)
		}
	}
}

func TestWhoJSONEscapesCwd(t *testing.T) {
	cwd := `/tmp/acme-"quote"\slash`
	out := captureJSON(t, []whoJSONRow{
		{Name: "projagent", ID: "ididid", Kind: "generic", State: "idle", Pane: "%9", Cwd: cwd},
	})
	var rows []map[string]any
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if rows[0]["cwd"] != cwd {
		t.Fatalf("cwd got %q", rows[0]["cwd"])
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

func captureJSON(t *testing.T, v any) []byte {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = buf.ReadFrom(r)
		close(done)
	}()
	if rc := writeJSON(v); rc != 0 {
		t.Fatalf("writeJSON rc=%d", rc)
	}
	_ = w.Close()
	<-done
	os.Stdout = old
	return buf.Bytes()
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
