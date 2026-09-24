package main

import (
	"sync"

	"github.com/munisp/meridian-core-platform/packages/events/store"
	"github.com/munisp/meridian-core-platform/services/tin-graph/internal/graph"
)

// Perf fix: previously every request path (verifyTIN, resolve, findEntity,
// provision idempotence, graph builds) re-loaded the ENTIRE entities
// collection via store.ListInto — a triple JSON serialize plus O(N) scan
// per request (measured p50 13.6-24.4 ms @2k entities, scaling linearly).
// entityIndex keeps an in-memory copy of the collection (by-id and
// by-tin_hash maps + the id-sorted list ListInto returned) and is
// invalidated by entityStore on every write to "entities", so reads are
// served from memory and the index is rebuilt lazily after any mutation.

type entityIndex struct {
	mu     sync.RWMutex
	loaded bool
	list   []graph.Entity
	byID   map[string]int // entity id -> index into list
	byTIN  map[string]int // tin hash -> index into list
}

func (x *entityIndex) invalidate() {
	x.mu.Lock()
	x.loaded = false
	x.mu.Unlock()
}

// ensure (re)builds the index from the store if stale.
func (x *entityIndex) ensure(st store.DocStore) {
	x.mu.RLock()
	ok := x.loaded
	x.mu.RUnlock()
	if ok {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.loaded {
		return
	}
	var ents []graph.Entity
	if err := st.ListInto("entities", &ents); err != nil {
		ents = nil // same behaviour allEntities() had on store errors
	}
	byID := make(map[string]int, len(ents))
	byTIN := make(map[string]int, len(ents))
	for i, e := range ents {
		byID[e.ID] = i
		if e.TINHash != "" {
			if _, dup := byTIN[e.TINHash]; !dup { // first id-sorted match, like the old linear scan
				byTIN[e.TINHash] = i
			}
		}
	}
	x.list, x.byID, x.byTIN, x.loaded = ents, byID, byTIN, true
}

func (x *entityIndex) all(st store.DocStore) []graph.Entity {
	x.ensure(st)
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.list
}

func (x *entityIndex) findByID(st store.DocStore, id string) (graph.Entity, bool) {
	x.ensure(st)
	x.mu.RLock()
	defer x.mu.RUnlock()
	if i, ok := x.byID[id]; ok {
		return x.list[i], true
	}
	return graph.Entity{}, false
}

func (x *entityIndex) findByTINHash(st store.DocStore, hash string) (graph.Entity, bool) {
	x.ensure(st)
	x.mu.RLock()
	defer x.mu.RUnlock()
	if i, ok := x.byTIN[hash]; ok {
		return x.list[i], true
	}
	return graph.Entity{}, false
}

// entityStore wraps a DocStore and invalidates the entity index on any
// write to the "entities" collection (Put/Delete/Update),
// covering direct handler writes and internal/graph lifecycle mutations
// (merge/unmerge), which receive the wrapped store as a DocStore.
type entityStore struct {
	store.DocStore
	idx *entityIndex
}

func (s entityStore) Put(coll, id string, v any) error {
	err := s.DocStore.Put(coll, id, v)
	if coll == "entities" {
		s.idx.invalidate()
	}
	return err
}

func (s entityStore) Delete(coll, id string) error {
	err := s.DocStore.Delete(coll, id)
	if coll == "entities" {
		s.idx.invalidate()
	}
	return err
}

func (s entityStore) Update(coll, id string, v any, fn func(current any) (any, error)) error {
	err := s.DocStore.Update(coll, id, v, fn)
	if coll == "entities" {
		s.idx.invalidate()
	}
	return err
}
