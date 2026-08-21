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

func TestQueueMergesLegacyUnknownIntoDone(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "unknown")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "u1.json"), []byte(`{"id":"u1"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	q, err := OpenQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "done", "u1.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy unknown/ still present: %v", err)
	}
	p, d, f, err := q.Counts()
	if err != nil || p != 0 || d != 1 || f != 0 {
		t.Fatalf("counts pending=%d done=%d failed=%d err=%v", p, d, f, err)
	}
}

func writeQueueJSON(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"id":"x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestQueuePrune(t *testing.T) {
	dir := t.TempDir()
	q, err := OpenQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	oldDone := now.Add(-25 * time.Hour)
	freshDone := now.Add(-1 * time.Hour)
	oldFailed := now.Add(-8 * 24 * time.Hour)
	freshFailed := now.Add(-1 * 24 * time.Hour)

	writeQueueJSON(t, filepath.Join(dir, "done", "stale.json"), oldDone)
	writeQueueJSON(t, filepath.Join(dir, "done", "fresh.json"), freshDone)
	writeQueueJSON(t, filepath.Join(dir, "failed", "stale.json"), oldFailed)
	writeQueueJSON(t, filepath.Join(dir, "failed", "fresh.json"), freshFailed)

	doneN, failedN, err := q.Prune(now)
	if err != nil {
		t.Fatal(err)
	}
	if doneN != 1 || failedN != 1 {
		t.Fatalf("prune counts done=%d failed=%d", doneN, failedN)
	}
	for _, keep := range []string{
		filepath.Join(dir, "done", "fresh.json"),
		filepath.Join(dir, "failed", "fresh.json"),
	} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("expected %s to survive prune: %v", keep, err)
		}
	}
	for _, gone := range []string{
		filepath.Join(dir, "done", "stale.json"),
		filepath.Join(dir, "failed", "stale.json"),
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("expected %s pruned, stat err=%v", gone, err)
		}
	}
	p, d, f, err := q.Counts()
	if err != nil || p != 0 || d != 1 || f != 1 {
		t.Fatalf("counts after prune pending=%d done=%d failed=%d err=%v", p, d, f, err)
	}
}
