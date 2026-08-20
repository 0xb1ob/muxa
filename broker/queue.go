package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Msg is one queued pane paste. Files in pending/ survive a broker restart.
type Msg struct {
	ID           string `json:"id"`
	Pane         string `json:"pane"`
	From         string `json:"from"`
	To           string `json:"to"`
	Text         string `json:"text"`
	EnqueuedUnix int64  `json:"enqueued_unix"`
	DeadlineUnix int64  `json:"deadline_unix"`
	Attempts     int    `json:"attempts"`
}

type Queue struct {
	mu      sync.Mutex
	dir     string
	pending string
	done    string
	failed  string
}

func OpenQueue(dir string) (*Queue, error) {
	q := &Queue{
		dir:     dir,
		pending: filepath.Join(dir, "pending"),
		done:    filepath.Join(dir, "done"),
		failed:  filepath.Join(dir, "failed"),
	}
	for _, d := range []string{q.pending, q.done, q.failed} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	return q, nil
}

func (q *Queue) Put(m *Msg) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if m.ID == "" {
		m.ID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	}
	if m.EnqueuedUnix == 0 {
		m.EnqueuedUnix = time.Now().Unix()
	}
	return q.write(q.pending, m)
}

func (q *Queue) Pending() ([]*Msg, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ents, err := os.ReadDir(q.pending)
	if err != nil {
		return nil, err
	}
	var out []*Msg
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(q.pending, e.Name()))
		if err != nil {
			continue
		}
		var m Msg
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		out = append(out, &m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EnqueuedUnix == out[j].EnqueuedUnix {
			return out[i].ID < out[j].ID
		}
		return out[i].EnqueuedUnix < out[j].EnqueuedUnix
	})
	return out, nil
}

func (q *Queue) Save(m *Msg) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.write(q.pending, m)
}

func (q *Queue) MarkDone(m *Msg) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.move(m, q.done)
}

func (q *Queue) MarkFailed(m *Msg) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.move(m, q.failed)
}

func (q *Queue) Counts() (pending, done, failed int, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	pending, err = countJSON(q.pending)
	if err != nil {
		return
	}
	done, err = countJSON(q.done)
	if err != nil {
		return
	}
	failed, err = countJSON(q.failed)
	return
}

func (q *Queue) write(dir string, m *Msg) error {
	path := filepath.Join(dir, m.ID+".json")
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(tmp)
	if err := enc.Encode(m); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (q *Queue) move(m *Msg, dest string) error {
	if err := q.write(dest, m); err != nil {
		return err
	}
	return os.Remove(filepath.Join(q.pending, m.ID+".json"))
}

func countJSON(dir string) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	return n, nil
}
