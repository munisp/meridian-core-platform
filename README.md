# Meridian Core Platform

Core services, shared packages, workflow building blocks, spatial engine and
local infrastructure for the Meridian TaxTech platform (Nigerian NRS tax
platform). Binding contract: `../SPEC.md`.

## Layout

```
services/        rp-registry, tin-graph (Go); rules-engine, feature-store,
                 reg-watch, settlement (Python FastAPI); ledger, notification,
                 audit-evidence, geo, consent, search-indexer, edge-policy (Go);
                 admin-api (owned separately)
packages/        events (Go+Python envelope/bus/outbox/auth/store),
                 rulepack-schema (JSON Schema + Go/Python validators),
                 schemas (envelope + nrs.*.v1 payload types, JSON/Go/Py/TS),
                 temporal-sdkx (retry/SAGA/registry + inproc runner),
                 permify-models (Permify DSL + dev checker)
workflows-go/    shared wf-* building blocks (wf-gate-flip, wf-pack-rollout, Compose)
geo-rs/          Rust axum spatial engine (pip + haversine + attribution)
infra/           docker-compose.full.yml, .env.example, helm notes
rule-packs/      consumer lockfile (packs.lock.json, [seed] 1.0.0 pins)
```

## Build & test

```bash
export PATH=$HOME/sdk/go/bin:$PATH
./scripts/build-all.sh            # go build + vet + test for every module
# Python:
python3 -m venv ~/venv-core && . ~/venv-core/bin/activate
pip install fastapi uvicorn pydantic pyyaml httpx pytest duckdb pyarrow jsonschema
pip install -e packages/events/python
cd services/rules-engine && pytest tests/   # (same for feature-store, reg-watch, settlement)
```

Go workspace: `go.work` at repo root (`go 1.23`, per-module `replace` for
local packages). Only third-party Go dep: `gopkg.in/yaml.v3`.

## Run (dev, zero external deps)

Every service defaults to `EVENT_BUS=inproc`, embedded stores in `DATA_DIR`,
`AUTH_MODE=dev` (accepts `X-Dev-Role: admin|operator|auditor|board` or HS256
JWT with `MERIDIAN_DEV_JWT_SECRET`).

| Service | Lang | Port | Surface |
|---|---|---|---|
| rules-engine | Py | 8001 | `POST /v1/evaluate`, `GET /v1/packs` |
| rp-registry | Go | 8002 | packs CRUD/publish, `/v1/consumers/stale` |
| tin-graph | Go | 8003 | tin provision, verify tin/nin/cac, resolve, graph |
| audit-evidence | Go | 8004 | audit events, WORM evidence, TAT assemble |
| geo | Go | 8005 | attribution point/batch, boundaries |
| notification | Go | 8006 | `/v1/send`, `/v1/status/{id}` |
| consent | Go | 8007 | NDPA consents + receipts |
| search-indexer | Go | 8008 | `/v1/search?q=`, `/v1/index` |
| edge-policy | Go | 8009 | `/v1/routes` (APISIX YAML), WAF mode |
| ledger | Go | 8010 | accounts, transfers, pending/post/void, balance |
| reg-watch | Py | 8011 | gates + board flips, gazette watch |
| feature-store | Py | 8012 | materialise, online, batch |
| settlement | Py | 8013 | PSSP 3-way recon, breaks |
| geo-rs | Rust | 8100 | same attribution surface (primary engine) |

Example:

```bash
cd services/ledger && DATA_DIR=/tmp/ledger go run . &
curl -X POST localhost:8010/v1/accounts -H 'X-Dev-Role: operator' -H 'Content-Type: application/json' \
  -d '{"namespace":100,"flags":1}'
```

Full rails (Redpanda, Temporal+UI, Postgres/PostGIS, Redis, OpenSearch,
MinIO, TigerBeetle, Keycloak, Permify, APISIX+etcd):
`docker compose -f infra/docker-compose.full.yml up -d` (see `infra/.env.example`).

## What is REAL vs SIMULATED (honesty tags)

REAL:
- rules-engine: full rp-* YAML evaluation — rate_bps/threshold/band/formula
  (AST-whitelisted)/decision-table — with per-condition decision traces.
- ledger: TigerBeetle semantics (double-entry, pending transfers,
  post/void, partial capture, DEBITS_MUST_NOT_EXCEED_CREDITS, idempotent
  create) in the durable dev client behind `LedgerClient`.
- rp-registry: pack validation, semver latest, publish lifecycle, outbox
  event `nrs.rulepacks.published.v1`, stale-consumer detection.
- tin-graph: deterministic NIN=TIN/CAC-RC=TIN fusion, HMAC-SHA256
  pseudonymisation, weighted entity resolution with pack thresholds,
  shared-attribute graph.
- audit-evidence: hash-chained append-only audit log (tamper-evident),
  sha256 WORM objects (FS read-only, overwrite rejected, verify endpoint),
  HMAC-sealed TAT assembly.
- geo/geo-rs: ray-casting point-in-polygon + haversine prefilter over
  embedded polygons; **boundary data is [seed] coarse**, not survey-grade.
- settlement: real 3-way reconciliation (missing/amount-mismatch breaks).
- feature-store: real aggregations into DuckDB offline + online KV.
- search-indexer: real inverted index + real OpenSearch bulk when
  `OPENSEARCH_URL` set.
- events: ULID, envelope, inproc bus with DLQ, durable JSONL outbox + relay,
  HS256 JWT; **Redpanda publish/consume via pandaproxy REST is real HTTP**
  (EVENT_BUS=kafka).

SIMULATED (behind interfaces, per SPEC §8.3):
- NIMC/CAC verification (tin-graph deterministic simulators).
- Notification providers (log simulator writing to DATA_DIR/notifications.log).
- Gazette watchers (reg-watch [seed] fixture sources).
- Temporal server execution: inproc runner; real worker wires behind the
  same `sdkx.Runner` interface when the SDK is linked.
- TigerBeetle cluster client: dev in-process implementation; real client
  selected when `TIGERBEETLE_ADDRESSES` is set (native binding required).
- Kafka binary protocol clients: pandaproxy REST used instead (no sarama dep).
- Postgres: embedded JSON/SQLite stores are the dev fallback (`DATABASE_URL`
  adapters documented).

Seed data marked `[seed]` throughout (NG state/LGA polygons, gate registry,
identity thresholds, WHT sample pack, packs.lock.json pins).
