# Ingestion & Lakehouse Architecture

One spine: **envelope → bus → transactional outbox → sink → bronze/silver/gold → Trino / ML / feature-store**.

```
 producer svc                Kafka/Redpanda            lakehouse                 consumers
 ┌────────────┐  outbox      ┌──────────┐  sink   ┌──────────────┐
 │ domain tx  │─────────────▶│ nrs.*    │────────▶│ bronze (raw) │──┐
 │ + outbox   │  relay       │ topics   │ validate│ silver (conf)│  ├─▶ Trino (iceberg REST catalog)
 │   row      │ SKIP LOCKED  │ + .dlq   │ dedup   │ gold (pseud.)│  ├─▶ ML extract (ml_transactions_v)
 └────────────┘              └──────────┘         └──────────────┘  └─▶ feature-store materialise
```

## 1. Envelope (single canonical spec)

`packages/events/envelope` (Go) ≡ `meridian_events.envelope` (Python).
Fields: `id` (ULID), `type` (`nrs.*.v1`), `source`, `time` (RFC3339),
`tenant_id`, `trace_id`, `rule_pack_version`, `data`.

## 2. Schema registry

`packages/events/schemareg` (Go + embedded dev store) ≡
`meridian_events.schemareg` (Python). The dev store lives in
`packages/events/schemareg/schemas/*.json` with the **topic catalog** in
`schemareg/topics.json` (name, owner, schema, PII class, retention).

Publish-time validation: `bus.SetPublishValidator(reg.ValidateEnvelope)`
(Go). `PROFILE=prod` rejects unregistered/invalid; dev warns.

## 3. Transactional outbox (per-service)

Postgres services: `meridian_outbox` table (auto-created by
`store.OpenPg`; DDL = `store.PgOutboxDDL`). Write domain state and the
outbox row in **one** transaction (`store.WithTx` + `store.AppendOutboxTx`);
drain with `store.OutboxRelay` (`FOR UPDATE SKIP LOCKED`). Pattern +
reference services documented in `packages/events/README.md`
(search-indexer = consumer side, feature-store = producer side).

## 4. Bronze sink (`ml/pipelines/lakehouse_sink.py`)

Subscribes to every catalogued `nrs.*` topic (schemareg list;
`ML_SINK_TOPICS` overrides), upgrades legacy raw maps (consumer shim),
validates envelopes, dedups on envelope id (persistent
`seen-ids.jsonl`), writes `bronze/<dataset>/dt=<date>/` where
`dataset = topic family` (`nrs.psm.payments.v1 → psm_payments`).

- Prod: `KAFKA_BROKERS` set → Kafka consumer group `lakehouse-bronze-sink`.
- Backfill: `python -m ml.pipelines.lakehouse_sink --backfill <outbox-dir>...`
  replays service `outbox.jsonl` files through the same pipeline.

## 5. Lakehouse (one interface, two backends)

Interface contract shared by core (`ml/data/lakehouse.py`) and gov
(`services/analytics/app/lakehouse.py`): `write / read / sql / datasets /
dataset_path` over `bronze|silver|gold` zones.

- **Prod**: `ICEBERG_REST_URI` set → real Apache Iceberg via the REST
  catalog on MinIO (compose ships `iceberg-rest` + `trino`;
  `pyiceberg` optional, import-guarded). Tables `<zone>.<dataset>`;
  one snapshot per write. SQL via Trino.
- **Dev**: hive-partitioned parquet + `catalog.json` manifest (tables,
  schema versions, snapshots) — Trino/Spark-attachable, evolution explicit.

## 6. ML extract & scoring

- Extract: `ml/data/pipeline.py` reads the view **`ml_transactions_v`**
  (DDL: `infra/postgres/init/002-bronze-views.sql`) over the bronze txs
  mirror — never the phantom `transactions` table. Override with
  `ML_EXTRACT_TABLE`.
- Online scoring: `ml/pipelines/kafka_consumer.py` consumes the real
  `nrs.*` payment topics (`ML_LEGACY_TOPICS=1` re-adds the pre-audit
  `txs.events,filings.events` aliases), maps topic family → feature
  channel, unwraps envelopes, and emits scores as canonical
  **`nrs.ml.scored.v1`** envelopes (which themselves land in bronze via
  the sink).
- Feature store: push materialisation stays; receipts are emitted via the
  same-tx outbox as `nrs.feature.materialised.v1` (feeds lineage; bronze
  tables are the batch source going forward).

## 7. Topic table (ownership)

Canonical, machine-readable catalog: `packages/events/schemareg/topics.json`
(~50 topics; owner, schema, PII class, retention). Highlights:

| Topic | Owner | Schema |
|---|---|---|
| nrs.psm.payments.v1 | inclusion/presumptive | dedicated |
| nrs.pos.receipt(s).v1 | compliance/pos-vat | dedicated |
| nrs.mbs.preclearance.v1 | compliance/einvoicing | dedicated |
| nrs.onb.ussd.v1 | inclusion/ussd-gateway | dedicated (legacy-shimmed) |
| nrs.onb.provisioned.v1 | inclusion/onboarding | dedicated |
| nrs.ledger.transfers.v1 | core/ledger | dedicated |
| nrs.wht.deduction.v1 | compliance/wht | dedicated |
| nrs.cases.feed.v1 | core/audit-evidence | dedicated |
| nrs.rulepacks.published.v1 | core/rp-registry | dedicated |
| nrs.ml.scored.v1 | core/ml | dedicated |
| nrs.feature.materialised.v1 | core/feature-store | dedicated |
| nrs.revenue.settled.v1 | core/settlement | dedicated |
| remaining ~38 nrs.* | per topics.json | `nrs.generic.v1` (permissive) until dedicated schemas land |

## 8. Per-service envelope-conformance checklist

For every producing service, verify (CI-able):

1. **Envelope**: publishes via `envelope.New` / `new_envelope` — no raw
   maps (known violation: inclusion `ussd-gateway` → consumer shim covers
   it; fix upstream pending).
2. **Registered**: every published topic exists in
   `schemareg/topics.json` with a schema; payload validates
   (`reg.ValidateData`).
3. **Validated at publish**: `bus.SetPublishValidator` installed (Go) /
   validate before `bus.publish` (Python); `PROFILE=prod` rejects.
4. **Outbox**: domain write + outbox append in one tx
   (`store.WithTx`/`AppendOutboxTx` or `DuckDBOutbox`); relay running.
   No fire-and-forget `bus.Publish` after commit.
5. **Dedup-safe**: consumers tolerate redelivery (sink dedups on
   envelope id; keep ids stable across retries).
6. **PII**: raw NIN/TIN/MSISDN pseudonymised before publish; PII class
   recorded in the catalog.
7. **Money**: amounts are integer kobo (`*_kobo`, BIGINT) — never floats.
8. **Versioning**: payload changes go through
   `CheckCompatibility` (backward) before registering a new schema.
