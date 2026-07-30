// Package evidence implements the append-only hash-chained audit log,
// WORM evidence objects (sha256-addressed, immutable) and technical
// audit trail (TAT) assembly (SPEC 2).
package evidence

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/envelope"
	"github.com/munisp/meridian-core-platform/packages/events/store"
)

// AuditEvent is one append-only audit record. The hash chain makes
// tampering evident: hash = sha256(prev_hash || canonical(event)).
type AuditEvent struct {
	Seq             int64          `json:"seq"`
	ID              string         `json:"id"`
	Time            string         `json:"time"`
	Actor           string         `json:"actor"`
	Subject         string         `json:"subject"`
	Action          string         `json:"action"`
	Type            string         `json:"type"`
	TenantID        string         `json:"tenant_id,omitempty"`
	RulePackVersion string         `json:"rule_pack_version,omitempty"`
	Details         map[string]any `json:"details,omitempty"`
	PrevHash        string         `json:"prev_hash"`
	Hash            string         `json:"hash"`
}

func canonical(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// computeHash derives the chain hash of an event (over everything but Hash).
func computeHash(e AuditEvent) string {
	h := sha256.New()
	h.Write([]byte(e.PrevHash))
	// seq is assigned by the append log after hashing, so it is covered
	// implicitly by chain order (prev_hash links) rather than the hash itself.
	payload := map[string]any{
		"id": e.ID, "time": e.Time, "actor": e.Actor,
		"subject": e.Subject, "action": e.Action, "type": e.Type,
		"tenant_id": e.TenantID, "rule_pack_version": e.RulePackVersion,
		"details": e.Details,
	}
	h.Write(canonical(payload))
	return hex.EncodeToString(h.Sum(nil))
}

// AuditLog is the hash-chained append-only log.
type AuditLog struct {
	log  *store.AppendLog
	tail string // hash of last event ("" = genesis)
}

// OpenAuditLog opens the log and recovers the chain tail.
func OpenAuditLog(dir string) (*AuditLog, error) {
	l, err := store.OpenAppendLog(dir, "audit")
	if err != nil {
		return nil, err
	}
	al := &AuditLog{log: l}
	recs, err := l.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) > 0 {
		var last AuditEvent
		if json.Unmarshal(recs[len(recs)-1], &last) == nil {
			al.tail = last.Hash
		}
	}
	return al, nil
}

// Append adds an event to the chain.
func (al *AuditLog) Append(e AuditEvent) (AuditEvent, error) {
	if e.ID == "" {
		e.ID = envelope.NewULID()
	}
	if e.Time == "" {
		e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}
	e.PrevHash = al.tail
	e.Hash = computeHash(e)
	if _, err := al.log.Append(e); err != nil {
		return e, err
	}
	al.tail = e.Hash
	return e, nil
}

// All returns every event in chain order.
func (al *AuditLog) All() ([]AuditEvent, error) {
	recs, err := al.log.ReadAll()
	if err != nil {
		return nil, err
	}
	out := make([]AuditEvent, 0, len(recs))
	for _, r := range recs {
		var e AuditEvent
		if json.Unmarshal(r, &e) == nil {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// VerifyChain re-computes the whole chain; returns the first broken seq (0 = ok).
func (al *AuditLog) VerifyChain() (int64, error) {
	events, err := al.All()
	if err != nil {
		return 0, err
	}
	prev := ""
	for _, e := range events {
		if e.PrevHash != prev {
			return e.Seq, nil
		}
		if computeHash(e) != e.Hash {
			return e.Seq, nil
		}
		prev = e.Hash
	}
	return 0, nil
}

// Query filters events by subject/type/time range.
func (al *AuditLog) Query(subject, typ, from, to string) ([]AuditEvent, error) {
	events, err := al.All()
	if err != nil {
		return nil, err
	}
	var out []AuditEvent
	for _, e := range events {
		if subject != "" && e.Subject != subject {
			continue
		}
		if typ != "" && e.Type != typ {
			continue
		}
		if from != "" && e.Time < from {
			continue
		}
		if to != "" && e.Time > to {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// --- WORM evidence objects ---

// Object is a write-once-read-many evidence object.
type Object struct {
	ID          string         `json:"id"`
	SHA256      string         `json:"sha256"`
	WormURI     string         `json:"worm_uri"`
	ContentType string         `json:"content_type"`
	Size        int            `json:"size"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   string         `json:"created_at"`
	Immutable   bool           `json:"immutable"`
	StoragePath string         `json:"-"`
}

// WormStore persists evidence objects content-addressed under dir.
type WormStore struct {
	dir string
	st  *store.Store
}

// NewWormStore opens the WORM store.
func NewWormStore(dir string, st *store.Store) (*WormStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &WormStore{dir: dir, st: st}, nil
}

var errImmutable = fmt.Errorf("worm object immutable: overwrite rejected")

// ErrImmutable is returned on overwrite attempts.
func ErrImmutable() error { return errImmutable }

// Put writes a new evidence object; existing ids are never overwritten.
func (ws *WormStore) Put(id string, content []byte, contentType string, meta map[string]any) (Object, error) {
	if id == "" {
		sum := sha256.Sum256(content)
		id = "ev-" + hex.EncodeToString(sum[:8]) + "-" + envelope.NewULID()[14:]
	}
	var existing Object
	if err := ws.st.Get("evidence", id, &existing); err == nil {
		return Object{}, errImmutable
	}
	sum := sha256.Sum256(content)
	path := ws.dir + "/" + id + ".bin"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o444); err != nil {
		return Object{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return Object{}, err
	}
	os.Chmod(path, 0o444) // read-only at the FS level too
	obj := Object{
		ID:          id,
		SHA256:      hex.EncodeToString(sum[:]),
		WormURI:     "worm://evidence/" + id,
		ContentType: contentType,
		Size:        len(content),
		Metadata:    meta,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Immutable:   true,
		StoragePath: path,
	}
	if err := ws.st.Put("evidence", id, obj); err != nil {
		return Object{}, err
	}
	return obj, nil
}

// Get returns object metadata.
func (ws *WormStore) Get(id string) (Object, error) {
	var obj Object
	if err := ws.st.Get("evidence", id, &obj); err != nil {
		return Object{}, err
	}
	obj.StoragePath = ws.dir + "/" + id + ".bin"
	return obj, nil
}

// Content reads the object bytes.
func (ws *WormStore) Content(id string) ([]byte, error) {
	return os.ReadFile(ws.dir + "/" + id + ".bin")
}

// Verify recomputes the sha256 of the stored content.
func (ws *WormStore) Verify(id string) (bool, string, error) {
	obj, err := ws.Get(id)
	if err != nil {
		return false, "", err
	}
	content, err := ws.Content(id)
	if err != nil {
		return false, "", err
	}
	sum := sha256.Sum256(content)
	got := hex.EncodeToString(sum[:])
	return got == obj.SHA256, got, nil
}

// --- TAT assembly ---

// TAT is a technical audit trail: who saw what, when, under which
// rule_pack_version (SPEC 2), chain-verified and HMAC-sealed.
type TAT struct {
	ID            string       `json:"id"`
	Subject       string       `json:"subject"`
	GeneratedAt   string       `json:"generated_at"`
	GeneratedBy   string       `json:"generated_by"`
	From          string       `json:"from,omitempty"`
	To            string       `json:"to,omitempty"`
	Events        []AuditEvent `json:"events"`
	EvidenceRefs  []string     `json:"evidence_refs"`
	ChainValid    bool         `json:"chain_valid"`
	RulePacksSeen []string     `json:"rule_packs_seen"`
	Seal          string       `json:"seal"` // HMAC-SHA256 over the assembly
}

// AssembleTAT builds and seals a TAT for a subject.
func AssembleTAT(al *AuditLog, ws *WormStore, subject, from, to, actor, sealKey string) (TAT, error) {
	events, err := al.Query(subject, "", from, to)
	if err != nil {
		return TAT{}, err
	}
	brokenSeq, err := al.VerifyChain()
	if err != nil {
		return TAT{}, err
	}
	packsSeen := map[string]bool{}
	evRefs := map[string]bool{}
	for _, e := range events {
		if e.RulePackVersion != "" {
			packsSeen[e.RulePackVersion] = true
		}
		if ev, ok := e.Details["evidence_id"].(string); ok && ev != "" {
			evRefs[ev] = true
		}
	}
	tat := TAT{
		ID:          "tat-" + envelope.NewULID(),
		Subject:     subject,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		GeneratedBy: actor,
		From:        from,
		To:          to,
		Events:      events,
		ChainValid:  brokenSeq == 0,
	}
	for p := range packsSeen {
		tat.RulePacksSeen = append(tat.RulePacksSeen, p)
	}
	sort.Strings(tat.RulePacksSeen)
	for r := range evRefs {
		tat.EvidenceRefs = append(tat.EvidenceRefs, r)
	}
	sort.Strings(tat.EvidenceRefs)
	// seal over everything but the seal itself
	sealed := tat
	sealed.Seal = ""
	mac := hmac.New(sha256.New, []byte(sealKey))
	mac.Write(canonical(sealed))
	tat.Seal = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return tat, nil
}
