-- Ingestion spine: bronze mirror + ML extract view (audit I6).
--
-- `bronze.txs` is the Postgres mirror of the bronze lakehouse transactions
-- (payment-like nrs.* topics). The lakehouse sink
-- (ml/pipelines/lakehouse_sink.py) is the source of truth in object
-- storage; this table exists for services/ML that read via DATABASE_URL.
-- Populate it by replaying the sink's bronze records (backfill mode) or by
-- running the sink with a Postgres mirror writer; it is deliberately
-- append-only and keyed by envelope id for idempotent dedup.
CREATE SCHEMA IF NOT EXISTS bronze;

CREATE TABLE IF NOT EXISTS bronze.txs (
    id                TEXT PRIMARY KEY,           -- envelope id (ULID)
    type              TEXT NOT NULL,              -- topic, e.g. nrs.psm.payments.v1
    source            TEXT NOT NULL,
    time              TIMESTAMPTZ NOT NULL,
    tenant_id         TEXT NOT NULL DEFAULT '',
    trace_id          TEXT NOT NULL DEFAULT '',
    rule_pack_version TEXT NOT NULL DEFAULT '',
    data              JSONB NOT NULL,
    ingested_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS bronze_txs_type_time ON bronze.txs (type, time);

-- The ML extract view consumed by ml/data/pipeline.py (EXTRACT_SQL reads
-- `ml_transactions_v` by default; override with ML_EXTRACT_TABLE).
CREATE OR REPLACE VIEW bronze.ml_transactions_v AS
SELECT id                                    AS tx_id,
       data->>'tin_hash'                     AS entity_id,
       data->>'counterparty_hash'            AS counterparty_id,
       COALESCE((data->>'occurred_at')::timestamptz, time) AS occurred_at,
       data->>'channel'                      AS channel,
       data->>'category'                     AS category,
       (data->>'amount_kobo')::bigint        AS amount_kobo,
       (data->>'vat_rate')::double precision AS vat_rate,
       0                                     AS label,
       ''                                    AS fraud_type
FROM bronze.txs
WHERE type IN ('nrs.psm.payments.v1', 'nrs.psm.remittance.v1',
               'nrs.pos.receipts.v1', 'nrs.pos.receipt.v1',
               'nrs.mbs.preclearance.v1');

-- Point DATABASE_URL-based readers at the schema-qualified name by default:
CREATE OR REPLACE VIEW public.ml_transactions_v AS
SELECT * FROM bronze.ml_transactions_v;
