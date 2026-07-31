// lifecycle.go — TIN dedup/merge lifecycle (M2).
//
// Lifecycle statuses live OUTSIDE the Entity document (collection
// "lifecycle" keyed by tin_hash) so the existing Entity schema is
// untouched; an entity with no lifecycle record is implicitly "active".
//
// Merge moves registrations (any doc in "registrations" whose tin_hash
// matches) and KYB attributes onto the surviving TIN, marks the merged
// record status=merged_into, and writes an append-only audit trail
// (collection "tin_audit"). Unmerge restores exact pre-merge snapshots
// and is only allowed inside a configurable window.
//
// Filings rule hook: FilingAllowed is the documented gate other services
// (filings/assessment) call before accepting a new filing for a TIN —
// deceased, business_closed, deregistered and merged_into TINs reject
// new filings; suspended TINs still may file (suspension restricts
// clearance certificates, not the obligation to file).
package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/store"
)

// LifecycleStatus is the TIN lifecycle state machine label.
type LifecycleStatus string

const (
	StatusActive         LifecycleStatus = "active"
	StatusSuspended      LifecycleStatus = "suspended"
	StatusMergedInto     LifecycleStatus = "merged_into"
	StatusDeceased       LifecycleStatus = "deceased"
	StatusBusinessClosed LifecycleStatus = "business_closed"
	StatusDeregistered   LifecycleStatus = "deregistered"
)

// Sentinel errors mapped to HTTP codes by the transport layer.
var (
	ErrIllegalTransition = errors.New("illegal lifecycle transition")
	ErrNotFound          = errors.New("record not found")
	ErrWindowExpired     = errors.New("unmerge window expired")
	ErrTerminalMerge     = errors.New("surviving TIN is not in a mergeable status")
)

// transitions is the lifecycle transition table. merged_into -> active is
// NOT a public transition: it happens only through UnmergeTINs restore.
var transitions = map[LifecycleStatus][]LifecycleStatus{
	StatusActive:    {StatusSuspended, StatusMergedInto, StatusDeceased, StatusBusinessClosed, StatusDeregistered},
	StatusSuspended: {StatusActive, StatusMergedInto, StatusDeceased, StatusBusinessClosed, StatusDeregistered},
	// terminal states: deceased / business_closed / deregistered / merged_into
}

// CanTransition reports whether from->to is a legal public transition.
func CanTransition(from, to LifecycleStatus) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// ParseStatus validates a status label.
func ParseStatus(s string) (LifecycleStatus, error) {
	switch LifecycleStatus(strings.ToLower(strings.TrimSpace(s))) {
	case StatusActive, StatusSuspended, StatusMergedInto, StatusDeceased, StatusBusinessClosed, StatusDeregistered:
		return LifecycleStatus(strings.ToLower(strings.TrimSpace(s))), nil
	}
	return "", fmt.Errorf("unknown lifecycle status %q", s)
}

// FilingAllowed is the filings rule hook: deceased, business_closed,
// deregistered and merged_into TINs stop new filings. The returned string
// is the human-readable reason when blocked.
func FilingAllowed(s LifecycleStatus) (bool, string) {
	switch s {
	case StatusActive, StatusSuspended:
		return true, ""
	case StatusMergedInto:
		return false, "TIN was merged into a surviving TIN; file under the surviving TIN"
	case StatusDeceased:
		return false, "TIN belongs to a deceased taxpayer; estate filings only"
	case StatusBusinessClosed:
		return false, "business is closed; new filings blocked"
	case StatusDeregistered:
		return false, "TIN is deregistered; new filings blocked"
	}
	return false, "unknown lifecycle status"
}

// LifecycleEvent is one entry in the per-TIN audit history.
type LifecycleEvent struct {
	From          LifecycleStatus `json:"from"`
	To            LifecycleStatus `json:"to"`
	EffectiveDate string          `json:"effective_date"` // YYYY-MM-DD
	Reason        string          `json:"reason,omitempty"`
	Actor         string          `json:"actor,omitempty"`
	At            string          `json:"at"` // RFC3339 wall-clock of the write
}

// LifecycleRecord is the lifecycle document (collection "lifecycle",
// id = tin_hash). History is append-only.
type LifecycleRecord struct {
	TINHash       string           `json:"tin_hash"`
	Status        LifecycleStatus  `json:"status"`
	EffectiveDate string           `json:"effective_date,omitempty"`
	MergedInto    string           `json:"merged_into,omitempty"` // surviving tin_hash when merged
	UpdatedAt     string           `json:"updated_at"`
	History       []LifecycleEvent `json:"history,omitempty"`
}

// MergeRecord is the reversible merge receipt (collection "merges",
// id = merge ULID). Snapshots let UnmergeTINs restore byte-for-byte.
type MergeRecord struct {
	ID                string          `json:"id"`
	SurvivingTINHash  string          `json:"surviving_tin_hash"`
	MergedTINHash     string          `json:"merged_tin_hash"`
	Reason            string          `json:"reason"`
	EvidenceRef       string          `json:"evidence_ref"`
	Officer           string          `json:"officer"`
	MergedAt          string          `json:"merged_at"`
	UndoneAt          string          `json:"undone_at,omitempty"`
	UndoneBy          string          `json:"undone_by,omitempty"`
	MovedRegistration []string        `json:"moved_registrations,omitempty"` // doc ids re-pointed to surviving
	SurvivingSnapshot json.RawMessage `json:"surviving_snapshot"`            // pre-merge entity doc
	MergedSnapshot    json.RawMessage `json:"merged_snapshot"`               // pre-merge entity doc
	PrevStatus        LifecycleStatus `json:"prev_status"`                   // merged entity status before merge
}

// DedupCandidate is a suspected duplicate pair for the review queue.
// Detection NEVER auto-merges: candidates land in "dedup_queue" with
// status pending_review for an officer to act on via /v1/tins/merge.
type DedupCandidate struct {
	ID          string             `json:"id"` // dedup-<tinA>-<tinB> (sorted, content-addressed)
	EntityA     string             `json:"entity_a"`
	EntityB     string             `json:"entity_b"`
	TINHashA    string             `json:"tin_hash_a"`
	TINHashB    string             `json:"tin_hash_b"`
	Score       float64            `json:"score"`
	FieldScores map[string]float64 `json:"field_scores"`
	Status      string             `json:"status"` // pending_review|dismissed|merged
	CreatedAt   string             `json:"created_at"`
}

// --- lifecycle record helpers ---

// LoadLifecycle returns the record for tinHash, defaulting to active.
func LoadLifecycle(st store.DocStore, tinHash string) (*LifecycleRecord, error) {
	var rec LifecycleRecord
	if err := st.Get("lifecycle", tinHash, &rec); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &LifecycleRecord{TINHash: tinHash, Status: StatusActive}, nil
		}
		return nil, err
	}
	return &rec, nil
}

// Transition applies a validated status change (409 semantics: returns
// ErrIllegalTransition) and appends to the audit history.
func Transition(st store.DocStore, tinHash string, to LifecycleStatus, effectiveDate, reason, actor, at string) (*LifecycleRecord, error) {
	rec, err := LoadLifecycle(st, tinHash)
	if err != nil {
		return nil, err
	}
	if !CanTransition(rec.Status, to) {
		return nil, fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, rec.Status, to)
	}
	return applyTransition(st, rec, to, effectiveDate, reason, actor, at)
}

func applyTransition(st store.DocStore, rec *LifecycleRecord, to LifecycleStatus, effectiveDate, reason, actor, at string) (*LifecycleRecord, error) {
	if effectiveDate == "" {
		effectiveDate = at[:10]
	}
	rec.History = append(rec.History, LifecycleEvent{
		From: rec.Status, To: to, EffectiveDate: effectiveDate, Reason: reason, Actor: actor, At: at,
	})
	rec.Status = to
	rec.EffectiveDate = effectiveDate
	rec.UpdatedAt = at
	if to != StatusMergedInto {
		rec.MergedInto = ""
	}
	if err := st.Put("lifecycle", rec.TINHash, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func appendAudit(st store.DocStore, kind string, v any) error {
	var events []json.RawMessage
	_ = st.ListInto("tin_audit", &events) // collection may not exist yet
	return st.Put("tin_audit", fmt.Sprintf("%s-%s", kind, NowRFC3339Nano()), v)
}

// NowRFC3339Nano gives a sortable unique-ish timestamp for audit ids.
func NowRFC3339Nano() string {
	return time.Now().UTC().Format("20060102T150405.000000000Z")
}

// --- merge / unmerge ---

func findEntityByTINHash(st store.DocStore, tinHash string) (Entity, error) {
	var ents []Entity
	if err := st.ListInto("entities", &ents); err != nil {
		return Entity{}, err
	}
	for _, e := range ents {
		if e.TINHash == tinHash {
			return e, nil
		}
	}
	return Entity{}, fmt.Errorf("%w: entity with tin_hash %s", ErrNotFound, tinHash)
}

// MergeTINs merges mergedHash into survivingHash: registrations are
// re-pointed, KYB attributes the surviving entity lacks are moved over,
// the merged record is marked merged_into, and an audit trail + reversible
// MergeRecord are written. Officer identity and evidence are mandatory.
func MergeTINs(st store.DocStore, survivingHash, mergedHash, reason, evidenceRef, officer, at string) (*MergeRecord, error) {
	if survivingHash == mergedHash {
		return nil, fmt.Errorf("surviving_tin and merged_tin must differ")
	}
	if reason == "" || evidenceRef == "" || officer == "" {
		return nil, fmt.Errorf("reason, evidence_ref and officer are required (audit trail)")
	}
	surv, err := findEntityByTINHash(st, survivingHash)
	if err != nil {
		return nil, err
	}
	merged, err := findEntityByTINHash(st, mergedHash)
	if err != nil {
		return nil, err
	}
	survLC, err := LoadLifecycle(st, survivingHash)
	if err != nil {
		return nil, err
	}
	if ok, _ := FilingAllowed(survLC.Status); !ok || survLC.Status == StatusSuspended {
		return nil, fmt.Errorf("%w: surviving status is %s", ErrTerminalMerge, survLC.Status)
	}
	mergedLC, err := LoadLifecycle(st, mergedHash)
	if err != nil {
		return nil, err
	}
	if !CanTransition(mergedLC.Status, StatusMergedInto) {
		return nil, fmt.Errorf("%w: %s -> merged_into", ErrIllegalTransition, mergedLC.Status)
	}
	survSnap, _ := json.Marshal(surv)
	mergedSnap, _ := json.Marshal(merged)

	// move registrations: any doc in "registrations" tagged with the merged hash
	var moved []string
	raws, err := st.List("registrations")
	if err == nil {
		for _, raw := range raws {
			var doc map[string]any
			if json.Unmarshal(raw, &doc) != nil {
				continue
			}
			if doc["tin_hash"] != mergedHash {
				continue
			}
			id, _ := doc["id"].(string)
			if id == "" {
				continue
			}
			doc["tin_hash"] = survivingHash
			doc["merged_from_tin_hash"] = mergedHash
			if err := st.Put("registrations", id, doc); err != nil {
				return nil, fmt.Errorf("move registration %s: %w", id, err)
			}
			moved = append(moved, id)
		}
	}
	sort.Strings(moved)

	// move KYB attributes the surviving entity lacks
	if len(surv.Directors) == 0 {
		surv.Directors = merged.Directors
	}
	if len(surv.Shareholders) == 0 {
		surv.Shareholders = merged.Shareholders
	}
	if len(surv.UBOs) == 0 {
		surv.UBOs = merged.UBOs
	}
	if surv.Attrs == nil && merged.Attrs != nil {
		surv.Attrs = map[string]string{}
	}
	for k, v := range merged.Attrs {
		if _, ok := surv.Attrs[k]; !ok {
			surv.Attrs[k] = v
		}
	}
	if err := st.Put("entities", surv.ID, surv); err != nil {
		return nil, err
	}

	mr := &MergeRecord{
		ID:                "merge-" + envelope.NewULID(),
		SurvivingTINHash:  survivingHash,
		MergedTINHash:     mergedHash,
		Reason:            reason,
		EvidenceRef:       evidenceRef,
		Officer:           officer,
		MergedAt:          at,
		MovedRegistration: moved,
		SurvivingSnapshot: survSnap,
		MergedSnapshot:    mergedSnap,
		PrevStatus:        mergedLC.Status,
	}
	if err := st.Put("merges", mr.ID, mr); err != nil {
		return nil, err
	}
	if _, err := applyTransition(st, mergedLC, StatusMergedInto, at[:10], reason, officer, at); err != nil {
		return nil, err
	}
	mergedLC, _ = LoadLifecycle(st, mergedHash)
	mergedLC.MergedInto = survivingHash
	if err := st.Put("lifecycle", mergedHash, mergedLC); err != nil {
		return nil, err
	}
	_ = appendAudit(st, "merge", mr)
	return mr, nil
}

// UnmergeTINs reverses the latest open merge for mergedHash inside the
// window: entity snapshots are restored byte-for-byte, registrations move
// back, and the merged record returns to its pre-merge status.
func UnmergeTINs(st store.DocStore, mergedHash, officer, at string, window time.Duration) (*MergeRecord, error) {
	var merges []MergeRecord
	if err := st.ListInto("merges", &merges); err != nil {
		return nil, err
	}
	var open *MergeRecord
	for i := range merges {
		m := &merges[i]
		if m.MergedTINHash == mergedHash && m.UndoneAt == "" {
			if open == nil || m.MergedAt > open.MergedAt {
				open = m
			}
		}
	}
	if open == nil {
		return nil, fmt.Errorf("%w: no open merge for tin_hash %s", ErrNotFound, mergedHash)
	}
	mergedAt, err := time.Parse(time.RFC3339, open.MergedAt)
	if err != nil {
		return nil, fmt.Errorf("corrupt merge record timestamp: %w", err)
	}
	now, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return nil, fmt.Errorf("bad at timestamp: %w", err)
	}
	if now.Sub(mergedAt) > window {
		return nil, fmt.Errorf("%w: merged at %s, window %s", ErrWindowExpired, open.MergedAt, window)
	}
	// restore entity snapshots
	var surv, merged Entity
	if err := json.Unmarshal(open.SurvivingSnapshot, &surv); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(open.MergedSnapshot, &merged); err != nil {
		return nil, err
	}
	if err := st.Put("entities", surv.ID, surv); err != nil {
		return nil, err
	}
	if err := st.Put("entities", merged.ID, merged); err != nil {
		return nil, err
	}
	// move registrations back
	for _, id := range open.MovedRegistration {
		var raw json.RawMessage
		if err := st.Get("registrations", id, &raw); err != nil {
			continue // registration may have been deleted meanwhile; audit keeps the trail
		}
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		doc["tin_hash"] = mergedHash
		delete(doc, "merged_from_tin_hash")
		if err := st.Put("registrations", id, doc); err != nil {
			return nil, err
		}
	}
	// restore lifecycle of the merged TIN (force: merged_into has no public exits)
	lc, err := LoadLifecycle(st, mergedHash)
	if err != nil {
		return nil, err
	}
	lc.History = append(lc.History, LifecycleEvent{
		From: StatusMergedInto, To: open.PrevStatus, EffectiveDate: at[:10],
		Reason: "unmerge: " + open.ID, Actor: officer, At: at,
	})
	lc.Status = open.PrevStatus
	lc.EffectiveDate = at[:10]
	lc.MergedInto = ""
	lc.UpdatedAt = at
	if err := st.Put("lifecycle", mergedHash, lc); err != nil {
		return nil, err
	}
	open.UndoneAt = at
	open.UndoneBy = officer
	if err := st.Put("merges", open.ID, open); err != nil {
		return nil, err
	}
	_ = appendAudit(st, "unmerge", open)
	return open, nil
}

// --- dedup candidate detection (review queue, never auto-merges) ---

// dedupWeights: fuzzy name + DOB + contact (phone/email) matching.
var dedupWeights = map[string]float64{"name": 0.40, "dob": 0.30, "contact": 0.30}

// DefaultDedupThreshold is the minimum score to enter the review queue.
const DefaultDedupThreshold = 0.70

// ScoreDedupPair scores a suspected-duplicate pair on fuzzy name, exact
// DOB (attrs["dob"]) and shared contact channel.
func ScoreDedupPair(a, b Entity) (float64, map[string]float64) {
	fs := map[string]float64{}
	if a.Name != "" && b.Name != "" {
		fs["name"] = stringSimilarity(a.Name, b.Name)
	}
	dobA, dobB := a.Attrs["dob"], b.Attrs["dob"]
	if dobA != "" && dobB != "" {
		if dobA == dobB {
			fs["dob"] = 1
		} else {
			fs["dob"] = 0
		}
	}
	contact := 0.0
	if a.Phone != "" && b.Phone != "" && normPhone(a.Phone) == normPhone(b.Phone) {
		contact = 1
	}
	if a.Email != "" && b.Email != "" && strings.EqualFold(normStr(a.Email), normStr(b.Email)) {
		contact = 1
	}
	if a.Phone != "" && b.Phone != "" || a.Email != "" && b.Email != "" {
		fs["contact"] = contact
	}
	var score, wsum float64
	for f, w := range dedupWeights {
		if s, ok := fs[f]; ok {
			score += w * s
			wsum += w
		}
	}
	if wsum > 0 {
		score /= wsum
	}
	return score, fs
}

// FindDedupCandidates produces the review queue: all pairs scoring at or
// above threshold, sorted by score desc. Entities already merged_into are
// excluded. Detection NEVER auto-merges.
func FindDedupCandidates(entities []Entity, mergedInto map[string]bool, threshold float64) []DedupCandidate {
	var out []DedupCandidate
	for i := 0; i < len(entities); i++ {
		if mergedInto[entities[i].TINHash] {
			continue
		}
		for j := i + 1; j < len(entities); j++ {
			if mergedInto[entities[j].TINHash] {
				continue
			}
			score, fs := ScoreDedupPair(entities[i], entities[j])
			if score < threshold {
				continue
			}
			out = append(out, DedupCandidate{
				ID:          "dedup-" + shortHash(entities[i].TINHash) + "-" + shortHash(entities[j].TINHash),
				EntityA:     entities[i].ID,
				EntityB:     entities[j].ID,
				TINHashA:    entities[i].TINHash,
				TINHashB:    entities[j].TINHash,
				Score:       score,
				FieldScores: fs,
				Status:      "pending_review",
				CreatedAt:   NowRFC3339(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// shortHash bounds-checks the prefix used in candidate ids.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// MergedIntoSet returns the set of tin_hashes currently in merged_into.
func MergedIntoSet(st store.DocStore) map[string]bool {
	var recs []LifecycleRecord
	if err := st.ListInto("lifecycle", &recs); err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, r := range recs {
		if r.Status == StatusMergedInto {
			out[r.TINHash] = true
		}
	}
	return out
}
