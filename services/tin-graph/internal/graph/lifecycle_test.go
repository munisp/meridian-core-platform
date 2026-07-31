// lifecycle_test.go — M2: merge/unmerge lifecycle, transition table,
// filings rule hook, dedup review queue.
package graph

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/store"
)

const testNow = "2025-06-01T12:00:00Z"

func openStore(t *testing.T) store.DocStore {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func seedEntity(t *testing.T, st store.DocStore, e Entity) {
	t.Helper()
	if err := st.Put("entities", e.ID, e); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionTableLegal(t *testing.T) {
	st := openStore(t)
	rec, err := Transition(st, "h1", StatusSuspended, "2025-06-01", "audit hold", "officer1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusSuspended {
		t.Fatalf("got %s", rec.Status)
	}
	if len(rec.History) != 1 || rec.History[0].From != StatusActive {
		t.Fatalf("audit history wrong: %+v", rec.History)
	}
	// suspended -> active is legal
	if _, err := Transition(st, "h1", StatusActive, "2025-06-02", "cleared", "officer1", "2025-06-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionTableIllegal(t *testing.T) {
	st := openStore(t)
	if _, err := Transition(st, "h1", StatusDeceased, "2025-06-01", "", "o", testNow); err != nil {
		t.Fatal(err)
	}
	// deceased is terminal
	if _, err := Transition(st, "h1", StatusActive, "2025-06-02", "", "o", "2025-06-02T00:00:00Z"); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("want ErrIllegalTransition, got %v", err)
	}
	// active -> active not a transition
	st2 := openStore(t)
	if _, err := Transition(st2, "h2", StatusActive, "2025-06-01", "", "o", testNow); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("self-transition: want ErrIllegalTransition, got %v", err)
	}
	// merged_into has no public exit
	st3 := openStore(t)
	if _, err := Transition(st3, "h3", StatusMergedInto, "2025-06-01", "", "o", testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := Transition(st3, "h3", StatusSuspended, "2025-06-02", "", "o", "2025-06-02T00:00:00Z"); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("merged_into exit: want ErrIllegalTransition, got %v", err)
	}
}

func TestParseStatusRejectsUnknown(t *testing.T) {
	if _, err := ParseStatus("retired"); err == nil {
		t.Fatal("want error for unknown status")
	}
	if s, err := ParseStatus(" Business_Closed "); err != nil || s != StatusBusinessClosed {
		t.Fatalf("normalisation: %v %v", s, err)
	}
}

func TestFilingAllowedHook(t *testing.T) {
	for _, s := range []LifecycleStatus{StatusActive, StatusSuspended} {
		if ok, _ := FilingAllowed(s); !ok {
			t.Fatalf("%s should allow filings", s)
		}
	}
	for _, s := range []LifecycleStatus{StatusMergedInto, StatusDeceased, StatusBusinessClosed, StatusDeregistered} {
		ok, reason := FilingAllowed(s)
		if ok || reason == "" {
			t.Fatalf("%s should block filings with a reason", s)
		}
	}
}

// setupMerge seeds two entities and a registration on the merged one.
func setupMerge(t *testing.T) (store.DocStore, Entity, Entity) {
	t.Helper()
	st := openStore(t)
	surv := Entity{ID: "ent-s", TIN: "10000001-0001", TINHash: "hash-surv", Name: "Adaeze Okafor", EntityType: "individual"}
	merged := Entity{ID: "ent-m", TIN: "10000002-0002", TINHash: "hash-merged", Name: "Adaze Okafor", EntityType: "individual",
		Attrs: map[string]string{"dob": "1980-01-01"}, Directors: []Director{{PersonRef: PersonRef{Name: "A. Okafor"}}}}
	seedEntity(t, st, surv)
	seedEntity(t, st, merged)
	reg := map[string]any{"id": "reg-1", "tin_hash": "hash-merged", "tax_type": "PAYE"}
	if err := st.Put("registrations", "reg-1", reg); err != nil {
		t.Fatal(err)
	}
	return st, surv, merged
}

func TestMergeMovesRelationsAndMarksRecord(t *testing.T) {
	st, surv, _ := setupMerge(t)
	mr, err := MergeTINs(st, surv.TINHash, "hash-merged", "duplicate NIN match", "case-42", "officer1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	// registration moved
	var reg map[string]any
	if err := st.Get("registrations", "reg-1", &reg); err != nil {
		t.Fatal(err)
	}
	if reg["tin_hash"] != "hash-surv" {
		t.Fatalf("registration not moved: %v", reg["tin_hash"])
	}
	// lifecycle marked merged_into with pointer + audit
	lc, err := LoadLifecycle(st, "hash-merged")
	if err != nil {
		t.Fatal(err)
	}
	if lc.Status != StatusMergedInto || lc.MergedInto != "hash-surv" {
		t.Fatalf("merged lifecycle wrong: %+v", lc)
	}
	// KYB attrs moved to surviving
	var s Entity
	if err := st.Get("entities", surv.ID, &s); err != nil {
		t.Fatal(err)
	}
	if s.Attrs["dob"] != "1980-01-01" || len(s.Directors) != 1 {
		t.Fatalf("KYB attrs not moved: %+v", s)
	}
	if mr.PrevStatus != StatusActive || len(mr.MovedRegistration) != 1 {
		t.Fatalf("merge record wrong: %+v", mr)
	}
	// audit trail written
	var audit []json.RawMessage
	if err := st.ListInto("tin_audit", &audit); err != nil || len(audit) == 0 {
		t.Fatalf("audit trail missing: %v", err)
	}
	// merged TIN now blocks filings
	if ok, _ := FilingAllowed(lc.Status); ok {
		t.Fatal("merged TIN must block new filings")
	}
}

func TestMergeRequiresReasonEvidenceOfficer(t *testing.T) {
	st, surv, _ := setupMerge(t)
	if _, err := MergeTINs(st, surv.TINHash, "hash-merged", "", "case-42", "officer1", testNow); err == nil {
		t.Fatal("missing reason must fail")
	}
	if _, err := MergeTINs(st, surv.TINHash, "hash-merged", "dup", "", "officer1", testNow); err == nil {
		t.Fatal("missing evidence_ref must fail")
	}
}

func TestMergeRejectsTerminalSurviving(t *testing.T) {
	st, surv, _ := setupMerge(t)
	if _, err := Transition(st, surv.TINHash, StatusDeregistered, "2025-05-01", "", "o", testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeTINs(st, surv.TINHash, "hash-merged", "dup", "case-1", "officer1", testNow); !errors.Is(err, ErrTerminalMerge) {
		t.Fatalf("want ErrTerminalMerge, got %v", err)
	}
}

func TestMergeRejectsAlreadyMerged(t *testing.T) {
	st, surv, _ := setupMerge(t)
	if _, err := MergeTINs(st, surv.TINHash, "hash-merged", "dup", "c1", "officer1", testNow); err != nil {
		t.Fatal(err)
	}
	// merging the same record again: merged_into -> merged_into is illegal
	if _, err := MergeTINs(st, surv.TINHash, "hash-merged", "dup", "c2", "officer1", testNow); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("want ErrIllegalTransition, got %v", err)
	}
}

func TestUnmergeRestores(t *testing.T) {
	st, surv, merged := setupMerge(t)
	if _, err := MergeTINs(st, surv.TINHash, "hash-merged", "dup", "c1", "officer1", testNow); err != nil {
		t.Fatal(err)
	}
	mr, err := UnmergeTINs(st, "hash-merged", "officer2", "2025-06-02T12:00:00Z", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if mr.UndoneAt == "" || mr.UndoneBy != "officer2" {
		t.Fatalf("merge record not closed: %+v", mr)
	}
	// entities restored byte-for-byte
	var s, m Entity
	if err := st.Get("entities", surv.ID, &s); err != nil {
		t.Fatal(err)
	}
	if err := st.Get("entities", merged.ID, &m); err != nil {
		t.Fatal(err)
	}
	if len(s.Directors) != 0 || s.Attrs != nil {
		t.Fatalf("surviving entity not restored: %+v", s)
	}
	if m.Attrs["dob"] != "1980-01-01" {
		t.Fatalf("merged entity not restored: %+v", m)
	}
	// registration moved back
	var reg map[string]any
	if err := st.Get("registrations", "reg-1", &reg); err != nil {
		t.Fatal(err)
	}
	if reg["tin_hash"] != "hash-merged" {
		t.Fatalf("registration not restored: %v", reg["tin_hash"])
	}
	// lifecycle back to active (prev status)
	lc, err := LoadLifecycle(st, "hash-merged")
	if err != nil {
		t.Fatal(err)
	}
	if lc.Status != StatusActive || lc.MergedInto != "" {
		t.Fatalf("lifecycle not restored: %+v", lc)
	}
}

func TestUnmergeWindowExpired(t *testing.T) {
	st, surv, _ := setupMerge(t)
	if _, err := MergeTINs(st, surv.TINHash, "hash-merged", "dup", "c1", "officer1", testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := UnmergeTINs(st, "hash-merged", "officer2", "2025-06-10T12:00:00Z", 72*time.Hour); !errors.Is(err, ErrWindowExpired) {
		t.Fatalf("want ErrWindowExpired, got %v", err)
	}
}

func TestUnmergeWithoutOpenMerge(t *testing.T) {
	st := openStore(t)
	if _, err := UnmergeTINs(st, "hash-x", "officer2", testNow, 72*time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDedupCandidatesFound(t *testing.T) {
	a := Entity{ID: "e1", TINHash: "ta", Name: "Adaeze Okafor", Phone: "+2348012345678", Attrs: map[string]string{"dob": "1980-01-01"}}
	b := Entity{ID: "e2", TINHash: "tb", Name: "Adaeze Okafor", Phone: "08012345678", Attrs: map[string]string{"dob": "1980-01-01"}}
	c := Entity{ID: "e3", TINHash: "tc", Name: "Musa Danjuma", Phone: "09099998888", Attrs: map[string]string{"dob": "1975-05-05"}}
	cands := FindDedupCandidates([]Entity{a, b, c}, nil, DefaultDedupThreshold)
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d: %+v", len(cands), cands)
	}
	c0 := cands[0]
	if c0.Status != "pending_review" {
		t.Fatalf("candidate must be pending_review (never auto-merge), got %s", c0.Status)
	}
	if c0.FieldScores["dob"] != 1 || c0.FieldScores["contact"] != 1 || c0.FieldScores["name"] != 1 {
		t.Fatalf("field scores wrong: %+v", c0.FieldScores)
	}
}

func TestDedupSkipsMergedEntities(t *testing.T) {
	st, surv, merged := setupMerge(t)
	if _, err := MergeTINs(st, surv.TINHash, merged.TINHash, "dup", "c1", "officer1", testNow); err != nil {
		t.Fatal(err)
	}
	set := MergedIntoSet(st)
	if !set["hash-merged"] || set["hash-surv"] {
		t.Fatalf("merged set wrong: %v", set)
	}
	cands := FindDedupCandidates([]Entity{surv, merged}, set, 0.0)
	if len(cands) != 0 {
		t.Fatalf("merged entities must be excluded from dedup scan: %+v", cands)
	}
}

func TestDedupBelowThreshold(t *testing.T) {
	a := Entity{ID: "e1", TINHash: "ta", Name: "Adaeze Okafor", Attrs: map[string]string{"dob": "1980-01-01"}}
	b := Entity{ID: "e2", TINHash: "tb", Name: "Yetunde Balogun", Attrs: map[string]string{"dob": "1990-12-31"}}
	if cands := FindDedupCandidates([]Entity{a, b}, nil, DefaultDedupThreshold); len(cands) != 0 {
		t.Fatalf("distinct taxpayers must not be queued: %+v", cands)
	}
}
