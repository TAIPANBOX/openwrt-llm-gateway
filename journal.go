package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one request as the journal keeps it: who, which model, what happened. It has no
// field that could hold a prompt, a completion, a key or a request path (invariant 5).
type Entry struct {
	Time    time.Time `json:"time"`
	Source  string    `json:"source"`
	Client  string    `json:"client,omitempty"`
	Route   string    `json:"route,omitempty"`
	Model   string    `json:"model,omitempty"`
	Backend string    `json:"backend,omitempty"`
	Status  int       `json:"status"`
	Reason  string    `json:"reason,omitempty"`
	Millis  int64     `json:"ms"`
	In      int       `json:"in,omitempty"`
	Out     int       `json:"out,omitempty"`
	Stream  bool      `json:"stream,omitempty"`
}

type Journal interface{ Record(Entry) }

type writerJournal struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewWriterJournal writes one JSON line per entry to w.
func NewWriterJournal(w io.Writer) Journal { return &writerJournal{enc: json.NewEncoder(w)} }

func (j *writerJournal) Record(e Entry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.enc.Encode(e)
}

type discardJournal struct{}

func (discardJournal) Record(Entry) {}

// fileJournal appends to a file and keeps one previous file when it passes max bytes. Its home is
// under /var/run, in RAM, so the router's flash takes none of these writes.
type fileJournal struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func NewFileJournal(path string, max int64) (Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	j := &fileJournal{path: path, max: max}
	return j, j.open()
}

func (j *fileJournal) open() error {
	f, err := os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	j.f, j.size = f, st.Size()
	return nil
}

func (j *fileJournal) Record(e Entry) {
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	line = append(line, '\n')
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return
	}
	if j.size+int64(len(line)) > j.max {
		j.f.Close()
		_ = os.Rename(j.path, j.path+".1")
		if j.open() != nil {
			j.f = nil
			return
		}
	}
	n, _ := j.f.Write(line)
	j.size += int64(n)
}
