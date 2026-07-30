// Package outbox implements the transactional outbox pattern (SPEC 1.1).
// In dev the canonical store is the service's embedded store and the outbox
// is a durable JSONL file; a relay goroutine drains it onto the bus.
// Services using Postgres implement Store with a real same-tx outbox table.
package outbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/bus"
	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

// Record is one outbox row.
type Record struct {
	Seq      int64             `json:"seq"`
	Topic    string            `json:"topic"`
	Envelope envelope.Envelope `json:"envelope"`
}

// Store abstracts the outbox table so services can back it with Postgres.
type Store interface {
	Append(topic string, env envelope.Envelope) error
	Pending(afterSeq int64, limit int) ([]Record, error)
}

// FileStore is a durable JSONL outbox (dev default).
type FileStore struct {
	mu   sync.Mutex
	dir  string
	seq  int64
	file *os.File
}

// NewFileStore opens (or creates) a JSONL outbox in dir.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "outbox.jsonl")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fs := &FileStore{dir: dir, file: f}
	// recover last seq
	if rf, err := os.Open(p); err == nil {
		sc := bufio.NewScanner(rf)
		sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
		for sc.Scan() {
			var r Record
			if json.Unmarshal(sc.Bytes(), &r) == nil && r.Seq > fs.seq {
				fs.seq = r.Seq
			}
		}
		rf.Close()
	}
	return fs, nil
}

// Append writes one outbox record (durable, fsync'd).
func (fs *FileStore) Append(topic string, env envelope.Envelope) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.seq++
	line, err := json.Marshal(Record{Seq: fs.seq, Topic: topic, Envelope: env})
	if err != nil {
		return err
	}
	if _, err := fs.file.Write(append(line, '\n')); err != nil {
		return err
	}
	return fs.file.Sync()
}

// Pending returns records with seq > afterSeq.
func (fs *FileStore) Pending(afterSeq int64, limit int) ([]Record, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	rf, err := os.Open(filepath.Join(fs.dir, "outbox.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rf.Close()
	var out []Record
	sc := bufio.NewScanner(rf)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Seq > afterSeq {
			out = append(out, r)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, sc.Err()
}

// Close flushes and closes the outbox file.
func (fs *FileStore) Close() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.file.Close()
}

// Relay drains an outbox Store onto a Bus, checkpointing progress.
type Relay struct {
	Store    Store
	Bus      bus.Bus
	Dir      string // checkpoint file location
	Interval time.Duration
}

// Run starts the relay loop until ctx is cancelled. Non-blocking errors are logged.
func (r *Relay) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		r.flushOnce(ctx)
		select {
		case <-ctx.Done():
			r.flushOnce(ctx) // final drain
			return
		case <-t.C:
		}
	}
}

func (r *Relay) checkpointPath() string { return filepath.Join(r.Dir, "outbox.relay.seq") }

func (r *Relay) loadCheckpoint() int64 {
	b, err := os.ReadFile(r.checkpointPath())
	if err != nil {
		return 0
	}
	var n int64
	fmt.Sscanf(string(b), "%d", &n)
	return n
}

func (r *Relay) saveCheckpoint(n int64) {
	tmp := r.checkpointPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d", n)), 0o644); err == nil {
		os.Rename(tmp, r.checkpointPath())
	}
}

func (r *Relay) flushOnce(ctx context.Context) {
	seq := r.loadCheckpoint()
	recs, err := r.Store.Pending(seq, 200)
	if err != nil {
		log.Printf("outbox relay: read pending: %v", err)
		return
	}
	for _, rec := range recs {
		if err := r.Bus.Publish(ctx, rec.Topic, rec.Envelope); err != nil {
			log.Printf("outbox relay: publish seq=%d topic=%s: %v", rec.Seq, rec.Topic, err)
			return // retry next tick
		}
		seq = rec.Seq
	}
	if len(recs) > 0 {
		r.saveCheckpoint(seq)
	}
}
