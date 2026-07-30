// Package store is the embedded durable document store used as the dev
// fallback for Postgres (SPEC 1.3: "embedded stores so services run
// standalone in dev"). One JSON file per collection, atomic writes,
// plus an append-only log for audit-style data. A Postgres adapter can
// implement the same methods when DATABASE_URL is set.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ErrNotFound is returned by Get for missing ids.
var ErrNotFound = errors.New("not found")

// Store is a collection-oriented embedded JSON document store.
type Store struct {
	mu   sync.RWMutex
	dir  string
	cols map[string]map[string]json.RawMessage
}

// Open creates/loads a store rooted at dir ("" means in-memory only).
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, cols: map[string]map[string]json.RawMessage{}}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		coll := e.Name()[:len(e.Name())-5]
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		m := map[string]json.RawMessage{}
		if len(b) > 0 {
			if err := json.Unmarshal(b, &m); err != nil {
				return nil, fmt.Errorf("load collection %s: %w", coll, err)
			}
		}
		s.cols[coll] = m
	}
	return s, nil
}

func (s *Store) coll(name string) map[string]json.RawMessage {
	c, ok := s.cols[name]
	if !ok {
		c = map[string]json.RawMessage{}
		s.cols[name] = c
	}
	return c
}

func (s *Store) persistLocked(name string) error {
	if s.dir == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.cols[name], "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, name+".json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, name+".json"))
}

// Put inserts or replaces a document.
func (s *Store) Put(coll, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coll(coll)[id] = b
	return s.persistLocked(coll)
}

// Get loads a document by id.
func (s *Store) Get(coll, id string, v any) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, ok := s.cols[coll][id]
	if !ok {
		return ErrNotFound
	}
	return json.Unmarshal(raw, v)
}

// GetRaw returns the raw JSON of a document.
func (s *Store) GetRaw(coll, id string) (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, ok := s.cols[coll][id]
	if !ok {
		return nil, ErrNotFound
	}
	return append(json.RawMessage(nil), raw...), nil
}

// Delete removes a document.
func (s *Store) Delete(coll, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cols[coll][id]; !ok {
		return ErrNotFound
	}
	delete(s.cols[coll], id)
	return s.persistLocked(coll)
}

// List returns all documents in a collection, ordered by id.
func (s *Store) List(coll string) ([]json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := s.cols[coll]
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		out = append(out, append(json.RawMessage(nil), m[id]...))
	}
	return out, nil
}

// ListInto unmarshals all documents of a collection into a slice pointer.
func (s *Store) ListInto(coll string, slicePtr any) error {
	raws, err := s.List(coll)
	if err != nil {
		return err
	}
	b, err := json.Marshal(raws)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, slicePtr)
}

// Update applies fn to the document with compare-and-swap semantics.
func (s *Store) Update(coll, id string, v any, fn func(current any) (any, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cur any
	if raw, ok := s.cols[coll][id]; ok {
		if err := json.Unmarshal(raw, &cur); err != nil {
			return err
		}
	}
	next, err := fn(cur)
	if err != nil {
		return err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	s.coll(coll)[id] = b
	if v != nil {
		_ = json.Unmarshal(b, v)
	}
	return s.persistLocked(coll)
}

// AppendLog is an append-only JSONL log with monotonically increasing seq
// (used by audit-evidence for the hash-chained event log).
type AppendLog struct {
	mu   sync.Mutex
	path string
	seq  int64
}

// OpenAppendLog opens or creates the JSONL file.
func OpenAppendLog(dir, name string) (*AppendLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &AppendLog{path: filepath.Join(dir, name+".jsonl")}
	// recover seq from tail
	if b, err := os.ReadFile(l.path); err == nil {
		var rec struct {
			Seq int64 `json:"seq"`
		}
		start := 0
		for i, c := range b {
			if c == '\n' {
				if i > start {
					if json.Unmarshal(b[start:i], &rec) == nil && rec.Seq > l.seq {
						l.seq = rec.Seq
					}
				}
				start = i + 1
			}
		}
	}
	return l, nil
}

// Append writes a record with the next sequence number and fsyncs.
func (l *AppendLog) Append(v any) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	// merge seq into the record
	b, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return 0, err
	}
	seqB, _ := json.Marshal(l.seq)
	m["seq"] = seqB
	line, err := json.Marshal(m)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return 0, err
	}
	return l.seq, f.Sync()
}

// ReadAll streams all records (each with a "seq" field).
func (l *AppendLog) ReadAll() ([]json.RawMessage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []json.RawMessage
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, append(json.RawMessage(nil), b[start:i]...))
			}
			start = i + 1
		}
	}
	return out, nil
}
