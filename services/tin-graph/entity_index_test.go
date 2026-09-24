package main

import (
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/store"
	"github.com/munisp/meridian-core-platform/services/tin-graph/internal/graph"
)

func newIndexedServer(t *testing.T) (*server, store.DocStore) {
	t.Helper()
	raw, err := store.Open("") // in-memory
	if err != nil {
		t.Fatal(err)
	}
	idx := &entityIndex{}
	return &server{st: entityStore{DocStore: raw, idx: idx}, rawSt: raw, idx: idx}, raw
}

// The index must serve reads and invalidate on every entity write (Put via
// the wrapped store, as handlers and lifecycle.go do).
func TestEntityIndexServesAndInvalidates(t *testing.T) {
	s, _ := newIndexedServer(t)
	e := graph.Entity{ID: "ent-1", TIN: "12345678-0001", TINHash: "hash-1", EntityType: "individual", Name: "Ada"}
	if err := s.st.Put("entities", e.ID, e); err != nil {
		t.Fatal(err)
	}
	got, ok := s.entityByTINHash("hash-1")
	if !ok || got.ID != "ent-1" {
		t.Fatalf("by tin hash: %v %+v", ok, got)
	}
	got, ok = s.findEntity("ent-1")
	if !ok || got.TINHash != "hash-1" {
		t.Fatalf("by id: %v %+v", ok, got)
	}
	if _, ok := s.entityByTINHash("nope"); ok {
		t.Fatal("phantom entity found")
	}
	// Mutate via the wrapped store: the index must observe the change.
	e.Name = "Ada Lovelace"
	if err := s.st.Put("entities", e.ID, e); err != nil {
		t.Fatal(err)
	}
	got, ok = s.findEntity("ent-1")
	if !ok || got.Name != "Ada Lovelace" {
		t.Fatalf("stale index after Put: %v %+v", ok, got)
	}
	// allEntities still returns the full id-sorted collection.
	e2 := graph.Entity{ID: "ent-0", TINHash: "hash-0", EntityType: "company"}
	if err := s.st.Put("entities", e2.ID, e2); err != nil {
		t.Fatal(err)
	}
	all := s.allEntities()
	if len(all) != 2 || all[0].ID != "ent-0" || all[1].ID != "ent-1" {
		t.Fatalf("allEntities order/content: %+v", all)
	}
}

// Writes through the wrapped store must invalidate so a provision followed
// immediately by a verify sees the new entity (no stale-cache window).
func TestEntityIndexNoStaleWindow(t *testing.T) {
	s, _ := newIndexedServer(t)
	_ = s.allEntities() // populate cache while empty
	e := graph.Entity{ID: "ent-9", TINHash: "hash-9"}
	if err := s.st.Put("entities", e.ID, e); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.entityByTINHash("hash-9"); !ok {
		t.Fatal("entity invisible right after write (stale cache)")
	}
}
