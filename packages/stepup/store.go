package stepup

import (
	"errors"
	"sync"
	"time"
)

// Record is a per-actor step-up enrollment.
type Record struct {
	Actor          string    `json:"actor"`
	SealedSecret   string    `json:"sealed_secret"`   // AES-GCM sealed base32 secret
	RecoveryHashes []string  `json:"recovery_hashes"` // SHA-256 hex, single-use
	Enabled        bool      `json:"enabled"`         // false until confirm
	CreatedAt      time.Time `json:"created_at"`
	ConfirmedAt    time.Time `json:"confirmed_at,omitempty"`
}

var (
	// ErrNotEnrolled: no record for the actor.
	ErrNotEnrolled = errors.New("stepup: not enrolled")
	// ErrReplay: ticket jti already consumed.
	ErrReplay = errors.New("stepup: ticket replay")
)

// Store persists enrollments and consumed ticket IDs. Implementations must
// be safe for concurrent use.
type Store interface {
	Get(actor string) (*Record, error)
	Put(rec *Record) error
	Delete(actor string) error
	// ConsumeTicket atomically marks jti consumed until exp; returns
	// ErrReplay if already consumed and not yet expired.
	ConsumeTicket(jti string, exp time.Time) error
}

// MemStore is an in-process Store (per-service; durable deployments should
// back it with the service DB).
type MemStore struct {
	mu      sync.Mutex
	recs    map[string]*Record
	tickets map[string]time.Time // jti -> exp
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{recs: map[string]*Record{}, tickets: map[string]time.Time{}}
}

// Get returns the actor's record or ErrNotEnrolled.
func (m *MemStore) Get(actor string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[actor]
	if !ok {
		return nil, ErrNotEnrolled
	}
	cp := *r
	cp.RecoveryHashes = append([]string(nil), r.RecoveryHashes...)
	return &cp, nil
}

// Put upserts a record.
func (m *MemStore) Put(rec *Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *rec
	cp.RecoveryHashes = append([]string(nil), rec.RecoveryHashes...)
	m.recs[rec.Actor] = &cp
	return nil
}

// Delete removes the actor's record (disable path).
func (m *MemStore) Delete(actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.recs, actor)
	return nil
}

// ConsumeTicket implements Store.
func (m *MemStore) ConsumeTicket(jti string, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	// opportunistic sweep of expired jtis
	for k, e := range m.tickets {
		if now.After(e) {
			delete(m.tickets, k)
		}
	}
	if e, ok := m.tickets[jti]; ok && now.Before(e) {
		return ErrReplay
	}
	m.tickets[jti] = exp
	return nil
}
