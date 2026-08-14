// Postgres adapter (HARDENING H3): pgx/v5 connection pool implementing the
// same DocStore interface as the embedded JSON store. Selected when
// DATABASE_URL is set; the embedded store remains the dev fallback. DDL is
// auto-migrated idempotently on open (CREATE TABLE IF NOT EXISTS), with
// documents stored as JSONB mirroring the SQLite/JSON document schemas.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	neturl "net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DocStore is the collection-oriented document store interface implemented
// by both the embedded Store (dev) and PgStore (prod).
type DocStore interface {
	Put(coll, id string, v any) error
	Get(coll, id string, v any) error
	GetRaw(coll, id string) (json.RawMessage, error)
	Delete(coll, id string) error
	List(coll string) ([]json.RawMessage, error)
	ListInto(coll string, slicePtr any) error
	Update(coll, id string, v any, fn func(current any) (any, error)) error
}

// PgDDL is the idempotent schema (mirrors the embedded document model).
const PgDDL = `
CREATE TABLE IF NOT EXISTS meridian_documents (
    collection TEXT NOT NULL,
    id         TEXT NOT NULL,
    doc        JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (collection, id)
);
CREATE TABLE IF NOT EXISTS meridian_append_log (
    seq         BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    doc         JSONB NOT NULL,
    appended_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS meridian_append_log_name_seq ON meridian_append_log (name, seq);
`

// PgStore is a Postgres-backed DocStore.
type PgStore struct {
	pool *pgxpool.Pool
}

// OpenPg connects to DATABASE_URL, auto-migrates the schema, and returns
// the store. The caller owns Close.
func OpenPg(ctx context.Context, databaseURL string) (*PgStore, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store pg: parse DATABASE_URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store pg: connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store pg: ping: %w", err)
	}
	if _, err := pool.Exec(ctx, PgDDL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store pg: migrate: %w", err)
	}
	// Transactional outbox table (audit I2) — idempotent.
	if _, err := pool.Exec(ctx, PgOutboxDDL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store pg: outbox migrate: %w", err)
	}
	return &PgStore{pool: pool}, nil
}

// Close releases the connection pool.
func (s *PgStore) Close() { s.pool.Close() }

// Put inserts or replaces a document (upsert).
func (s *PgStore) Put(coll, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
		`INSERT INTO meridian_documents (collection, id, doc, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (collection, id) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`,
		coll, id, b)
	return err
}

// Get loads a document by id.
func (s *PgStore) Get(coll, id string, v any) error {
	raw, err := s.GetRaw(coll, id)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// GetRaw returns the raw JSON of a document.
func (s *PgStore) GetRaw(coll, id string) (json.RawMessage, error) {
	var raw []byte
	err := s.pool.QueryRow(context.Background(),
		`SELECT doc FROM meridian_documents WHERE collection = $1 AND id = $2`, coll, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// Delete removes a document.
func (s *PgStore) Delete(coll, id string) error {
	tag, err := s.pool.Exec(context.Background(),
		`DELETE FROM meridian_documents WHERE collection = $1 AND id = $2`, coll, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns all documents in a collection, ordered by id.
func (s *PgStore) List(coll string) ([]json.RawMessage, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT doc FROM meridian_documents WHERE collection = $1 ORDER BY id`, coll)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, rows.Err()
}

// ListInto unmarshals all documents of a collection into a slice pointer.
func (s *PgStore) ListInto(coll string, slicePtr any) error {
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

// Update applies fn with compare-and-swap semantics inside a transaction.
func (s *PgStore) Update(coll, id string, v any, fn func(current any) (any, error)) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cur any
	var raw []byte
	err = tx.QueryRow(ctx,
		`SELECT doc FROM meridian_documents WHERE collection = $1 AND id = $2 FOR UPDATE`,
		coll, id).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if raw != nil {
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
	if _, err := tx.Exec(ctx,
		`INSERT INTO meridian_documents (collection, id, doc, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (collection, id) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`,
		coll, id, b); err != nil {
		return err
	}
	if v != nil {
		_ = json.Unmarshal(b, v)
	}
	return tx.Commit(ctx)
}

// ResolveDatabaseURL applies assurance DB privilege separation (w2 §6A #1):
// when DB_USER is set, the per-service least-privilege credentials
// (DB_USER/DB_PASSWORD) replace any credentials embedded in DATABASE_URL.
// Compat path: if DB_USER is unset the shared DATABASE_URL user is used —
// with a loud startup warning outside prod, and a startup refusal when
// PROFILE=prod (privilege separation is mandatory in production).
func ResolveDatabaseURL() string {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return ""
	}
	user := os.Getenv("DB_USER")
	if user == "" {
		if os.Getenv("PROFILE") == "prod" {
			log.Fatal("profile=prod FATAL: DB_USER (per-service least-privilege role, see " +
				"infra/postgres/migrations/0003_roles.sql) is required; refusing to start on the shared database user")
		}
		log.Printf("profile=dev component=store WARNING: DB_USER unset — using the SHARED database user from DATABASE_URL; " +
			"privilege separation is NOT enforced (set DB_USER/DB_PASSWORD per service)")
		return url
	}
	u, err := neturl.Parse(url)
	if err != nil {
		log.Printf("component=store WARNING: unparseable DATABASE_URL (%v); using as-is", err)
		return url
	}
	u.User = neturl.UserPassword(user, os.Getenv("DB_PASSWORD"))
	return u.String()
}

// OpenFromEnv selects the store per HARDENING H1, failing closed:
//
//   - DATABASE_URL set -> Postgres is REQUIRED; a connection failure is a
//     startup error, never a silent fallback to the embedded store (an
//     explicitly-configured Postgres that is unreachable must not strand
//     consent/audit writes in ephemeral per-pod files).
//   - DATABASE_URL unset -> the embedded JSON store is allowed only outside
//     prod; PROFILE=prod refuses to boot without DATABASE_URL.
func OpenFromEnv(dir string) (DocStore, error) {
	if url := ResolveDatabaseURL(); url != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		pg, err := OpenPg(ctx, url)
		if err != nil {
			return nil, fmt.Errorf("DATABASE_URL is set but Postgres is unreachable (%v); refusing embedded fallback", err)
		}
		log.Printf("profile=prod component=store postgres")
		return pg, nil
	}
	if os.Getenv("PROFILE") == "prod" {
		return nil, fmt.Errorf("PROFILE=prod requires DATABASE_URL; refusing the embedded store")
	}
	log.Printf("profile=dev component=store embedded dir=%q", dir)
	return Open(dir)
}

// PgAppendLog is the Postgres-backed append-only log (meridian_append_log).
type PgAppendLog struct {
	pool *pgxpool.Pool
	name string
}

// OpenPgAppendLog opens the named append log on the store's pool.
func (s *PgStore) OpenPgAppendLog(name string) *PgAppendLog {
	return &PgAppendLog{pool: s.pool, name: name}
}

// Append writes a record with the next sequence number.
func (l *PgAppendLog) Append(v any) (int64, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return 0, err
	}
	var seq int64
	err = l.pool.QueryRow(context.Background(),
		`INSERT INTO meridian_append_log (name, doc) VALUES ($1, $2) RETURNING seq`,
		l.name, b).Scan(&seq)
	if err != nil {
		return 0, err
	}
	seqB, _ := json.Marshal(seq)
	m["seq"] = seqB
	if _, err := l.pool.Exec(context.Background(),
		`UPDATE meridian_append_log SET doc = $1 WHERE name = $2 AND seq = $3`,
		mustJSON(m), l.name, seq); err != nil {
		return 0, err
	}
	return seq, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// ReadAll streams all records in seq order (each with a "seq" field).
func (l *PgAppendLog) ReadAll() ([]json.RawMessage, error) {
	rows, err := l.pool.Query(context.Background(),
		`SELECT doc FROM meridian_append_log WHERE name = $1 ORDER BY seq`, l.name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, rows.Err()
}
