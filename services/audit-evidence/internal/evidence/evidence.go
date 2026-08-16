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
// A5 hardening: the chain is KEYED (HMAC-SHA256 when chainKey != nil) so an
// attacker with write access to the log file cannot recompute hashes, and
// the sequence number is covered by the hash itself (previously seq was
// assigned after hashing, leaving reorder/replay invisible to the hash).
func computeHash(e AuditEvent, chainKey []byte) string {
	payload := map[string]any{
		"seq": e.Seq,
		"id": e.ID, "time": e.Time, "actor": e.Actor,
		"subject": e.Subject, "action": e.Action, "type": e.Type,
		"tenant_id": e.TenantID, "rule_pack_version": e.RulePackVersion,
		"details": e.Details,
	}
	var sum []byte
	if len(chainKey) > 0 {
		mac := hmac.New(sha256.New, chainKey)
		mac.Write([]byte(e.PrevHash))
		mac.Write(canonical(payload))
		sum = mac.Sum(nil)
	} else {
		h := sha256.New()
		h.Write([]byte(e.PrevHash))
		h.Write(canonical(payload))
		sum = h.Sum(nil)
	}
	return hex.EncodeToString(sum)
}

// AuditLog is the hash-chained append-only log.
type AuditLog struct {
	log     *store.AppendLog
	tail    string // hash of last event ("" = genesis)
	key     []byte // chain HMAC key (nil = legacy unkeyed chain)
	nextSeq int64
}

// OpenAuditLog opens the log and recovers the chain tail. chainKey keys the
// hash chain (HMAC-SHA256); pass nil only for legacy dev chains.
func OpenAuditLog(dir string, chainKey []byte) (*AuditLog, error) {
	l, err := store.OpenAppendLog(dir, "audit")
	if err != nil {
		return nil, err
	}
	al := &AuditLog{log: l, key: chainKey, nextSeq: 1}
	recs, err := l.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) > 0 {
		var last AuditEvent
		if json.Unmarshal(recs[len(recs)-1], &last) == nil {
			al.tail = last.Hash
			if last.Seq >= al.nextSeq {
				al.nextSeq = last.Seq + 1
			}
		}
	}
	return al, nil
}

// Append adds an event to the chain. Seq is assigned BEFORE hashing so it
// is covered by the (keyed) chain hash.
func (al *AuditLog) Append(e AuditEvent) (AuditEvent, error) {
	if e.ID == "" {
		e.ID = envelope.NewULID()
	}
	if e.Time == "" {
		e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}
	e.Seq = al.nextSeq
	e.PrevHash = al.tail
	e.Hash = computeHash(e, al.key)
	seq, err := al.log.Append(e)
	if err != nil {
		return e, err
	}
	al.nextSeq = seq + 1
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
		if computeHash(e, al.key) != e.Hash {
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
// WormStore persists evidence objects content-addressed under dir (dev) or
// in a MinIO object-locked bucket (prod, see minio.go).
type WormStore struct {
	dir     string
	st      store.DocStore
	backend blobBackend
}

// NewWormStore opens the WORM store (local backend; see NewWormStoreFromEnv
// for the MINIO_* prod selection).
func NewWormStore(dir string, st store.DocStore) (*WormStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &WormStore{dir: dir, st: st, backend: localBackend{dir: dir}}, nil
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
	// Fast-path existence check; the authoritative insert below is atomic
	// (PutIfAbsent) so a concurrent Put of the same id can never overwrite.
	var existing Object
	if err := ws.st.Get("evidence", id, &existing); err == nil {
		return Object{}, errImmutable
	}
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	uri, err := ws.backend.Put(id+".bin", content, contentType, sha)
	if err != nil {
		return Object{}, err
	}
	obj := Object{
		ID:          id,
		SHA256:      sha,
		WormURI:     uri,
		ContentType: contentType,
		Size:        len(content),
		Metadata:    meta,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Immutable:   true,
	}
	if _, local := ws.backend.(localBackend); local {
		obj.StoragePath = ws.dir + "/" + id + ".bin"
	}
	// Insert-only persist (r9 pentest defect): the audit schema grants
	// svc_audit_evidence INSERT but not UPDATE, so the upsert in Put 500s
	// every evidence write in prod; WORM semantics are insert-only anyway.
	if ps, ok := ws.st.(interface {
		PutIfAbsent(coll, id string, v any) (bool, error)
	}); ok {
		inserted, err := ps.PutIfAbsent("evidence", id, obj)
		if err != nil {
			return Object{}, err
		}
		if !inserted {
			return Object{}, errImmutable
		}
		return obj, nil
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
	if _, local := ws.backend.(localBackend); local {
		obj.StoragePath = ws.dir + "/" + id + ".bin"
	}
	return obj, nil
}

// Content reads the object bytes.
func (ws *WormStore) Content(id string) ([]byte, error) {
	return ws.backend.Get(id + ".bin")
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
