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
	Kind         string `json:"kind,omitempty"`
	ParentPane   string `json:"parent_pane,omitempty"`
}

const kindDispatch = "dispatch"

func (m *Msg) isDispatch() bool {
	return m != nil && m.Kind == kindDispatch
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
	if err := mergeLegacyUnknown(dir, q.done); err != nil {
		return nil, err
	}
	return q, nil
}

// mergeLegacyUnknown moves leftover unknown/ JSON into done/ so a pre-#96
// queue cannot retry a paste the old broker had already accepted.
func mergeLegacyUnknown(dir, done string) error {
	legacy := filepath.Join(dir, "unknown")
	ents, err := os.ReadDir(legacy)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		src := filepath.Join(legacy, e.Name())
		dst := filepath.Join(done, e.Name())
		if _, err := os.Stat(dst); err == nil {
			if err := os.Remove(src); err != nil {
				return err
			}
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	}
	return os.RemoveAll(legacy)
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

const (
	pruneDoneMaxAge   = 24 * time.Hour
	pruneFailedMaxAge = 7 * 24 * time.Hour
)

// Prune deletes stale entries from done/ and failed/. Done entries older
// than 24h and failed older than 7d are removed. Call on startup (and
// periodically) while holding no other queue work; the mutex makes
// concurrent delivery safe.
func (q *Queue) Prune(now time.Time) (done, failed int, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	done, err = q.pruneDir(q.done, now, pruneDoneMaxAge)
	if err != nil {
		return
	}
	failed, err = q.pruneDir(q.failed, now, pruneFailedMaxAge)
	return
}

func (q *Queue) pruneDir(dir string, now time.Time, maxAge time.Duration) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	cutoff := now.Add(-maxAge)
	n := 0
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
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
