// Transactional outbox for Postgres (audit I2). The outbox row is written
// in the SAME transaction as the domain state change, so a crash can never
// lose the event after the state commit. A relay drains unpublished rows
// onto the bus with FOR UPDATE SKIP LOCKED so multiple relay replicas are
// safe.
//
// Usage (producer):
//
//	err := store.WithTx(ctx, pg, func(tx pgx.Tx) error {
//	    if err := writeDomainStateTx(ctx, tx, op); err != nil { return err }
//	    return store.AppendOutboxTx(ctx, tx, "nrs.onb.provisioned.v1", env)
//	})
//
// Relay:
//
//	go store.OutboxRelay(ctx, pg, bus, store.OutboxRelayConfig{})
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/munisp/meridian-core-platform/packages/events/bus"
	"github.com/munisp/meridian-core-platform/packages/events/envelope"
)

// PgOutboxDDL creates the outbox table idempotently. One table per service
// database; services share the name meridian_outbox.
const PgOutboxDDL = `
CREATE TABLE IF NOT EXISTS meridian_outbox (
    seq          BIGSERIAL PRIMARY KEY,
    topic        TEXT NOT NULL,
    envelope     JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS meridian_outbox_unpublished
    ON meridian_outbox (seq) WHERE published_at IS NULL;
`

// TxBeginner is satisfied by *pgxpool.Pool and pgx.Conn.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithTx runs fn inside a transaction, committing on success and rolling
// back on error. Domain writes and AppendOutboxTx calls inside fn commit
// or roll back atomically.
func WithTx(ctx context.Context, b TxBeginner, fn func(tx pgx.Tx) error) error {
	tx, err := b.Begin(ctx)
	if err != nil {
		return fmt.Errorf("outbox withtx: begin: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after Commit
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WithTx runs fn in a transaction on the store's pool.
func (s *PgStore) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return WithTx(ctx, s.pool, fn)
}

// EnsureOutbox creates the outbox table if it does not exist.
func (s *PgStore) EnsureOutbox(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, PgOutboxDDL)
	return err
}

// AppendOutboxTx inserts one outbox row inside tx. The tx MUST also carry
// the domain state change; committing tx publishes both atomically.
func AppendOutboxTx(ctx context.Context, tx pgx.Tx, topic string, env envelope.Envelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("outbox append: marshal envelope: %w", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO meridian_outbox (topic, envelope) VALUES ($1, $2)`, topic, raw)
	if err != nil {
		return fmt.Errorf("outbox append: %w", err)
	}
	return nil
}

// OutboxClaim is one claimed (locked) outbox row.
type OutboxClaim struct {
	Seq      int64
	Topic    string
	Envelope envelope.Envelope
}

// outboxQuerier abstracts the pool for testability.
type outboxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// claimOutboxSQL atomically claims up to $1 unpublished rows: the selected
// rows are locked FOR UPDATE SKIP LOCKED and stamped published_at in the
// same statement, so concurrent relays never double-publish.
const claimOutboxSQL = `
WITH picked AS (
    SELECT seq FROM meridian_outbox
    WHERE published_at IS NULL
    ORDER BY seq
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
UPDATE meridian_outbox o
SET published_at = now()
FROM picked
WHERE o.seq = picked.seq
RETURNING o.seq, o.topic, o.envelope;
`

// ClaimOutbox claims up to limit unpublished outbox rows (marking them
// published). Returns claims in seq order.
func ClaimOutbox(ctx context.Context, q outboxQuerier, limit int) ([]OutboxClaim, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := q.Query(ctx, claimOutboxSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox claim: %w", err)
	}
	defer rows.Close()
	var out []OutboxClaim
	for rows.Next() {
		var c OutboxClaim
		var raw []byte
		if err := rows.Scan(&c.Seq, &c.Topic, &raw); err != nil {
			return nil, fmt.Errorf("outbox claim scan: %w", err)
		}
		if err := json.Unmarshal(raw, &c.Envelope); err != nil {
			return nil, fmt.Errorf("outbox claim envelope (seq %d): %w", c.Seq, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ClaimOutbox claims unpublished rows on the store's pool.
func (s *PgStore) ClaimOutbox(ctx context.Context, limit int) ([]OutboxClaim, error) {
	return ClaimOutbox(ctx, s.pool, limit)
}

// OutboxRelayConfig tunes the relay loop.
type OutboxRelayConfig struct {
	Batch    int           // rows claimed per tick (default 100)
	Interval time.Duration // poll interval (default 500ms)
}

// OutboxRelay polls the outbox table and publishes claimed rows to b until
// ctx is cancelled. Rows are claimed (published_at set) before publish; a
// publish failure logs and un-claims the row so it is retried next tick.
func OutboxRelay(ctx context.Context, s *PgStore, b bus.Bus, cfg OutboxRelayConfig) {
	if cfg.Batch <= 0 {
		cfg.Batch = 100
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 500 * time.Millisecond
	}
	if err := s.EnsureOutbox(ctx); err != nil {
		log.Printf("outbox relay: ensure table: %v", err)
	}
	tick := time.NewTicker(cfg.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			claims, err := s.ClaimOutbox(ctx, cfg.Batch)
			if err != nil {
				log.Printf("outbox relay: claim: %v", err)
				continue
			}
			for _, c := range claims {
				if err := b.Publish(ctx, c.Topic, c.Envelope); err != nil {
					log.Printf("outbox relay: publish seq=%d topic=%s: %v (retrying)", c.Seq, c.Topic, err)
					s.unclaim(ctx, c.Seq)
				}
			}
		}
	}
}

// unclaim clears published_at so a failed publish is retried.
func (s *PgStore) unclaim(ctx context.Context, seq int64) {
	if _, err := s.pool.Exec(ctx,
		`UPDATE meridian_outbox SET published_at = NULL WHERE seq = $1`, seq); err != nil {
		log.Printf("outbox relay: unclaim seq=%d: %v", seq, err)
	}
}
