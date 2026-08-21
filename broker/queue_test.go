package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueueSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	q, err := OpenQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := &Msg{ID: "a1", Pane: "%1", From: "c", To: "p", Text: "hello", DeadlineUnix: time.Now().Unix() + 60}
	if err := q.Put(m); err != nil {
		t.Fatal(err)
	}
	q2, err := OpenQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := q2.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "hello" || got[0].ID != "a1" {
		t.Fatalf("pending after reopen: %+v", got)
	}
	if err := q2.MarkDone(got[0]); err != nil {
		t.Fatal(err)
	}
	got, err = q2.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("still pending: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "done", "a1.json")); err != nil {
		t.Fatal(err)
	}
}
