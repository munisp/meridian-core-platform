# National scale runbook (SPEC B summary)

Targets: 50k TPS peak ingest, 2M concurrent USSD sessions, RPO ≤ 5min,
RTO ≤ 30min, p99 API < 800ms at peak. Implementation lives in `infra/helm/`,
`infra/apisix/`, `infra/cilium/`, and the data-tier scripts under
`infra/{postgres,redis,kafka,opensearch,minio}/`.

## Capacity math (peak, derived)

| Layer | Steady | Peak | Sizing basis |
|---|---|---|---|
| API ingress (APISIX) | 8k RPS | 60k RPS | 50k TPS + 20% read amplification |
| FastAPI/Go services | 6 pods x 800 RPS | 60-80 pods total | 800 RPS/pod @70% CPU (uvicorn workers=4, 2 vCPU) |
| USSD gateway | 200k sess | 2M sess | ~40KB/session state in Redis = 80GB; 3 redis shards x 32GB + replicas |
| Kafka ingest | 60k msg/s | 300k msg/s | 50k TPS x ~4 events + peak bursts; 3x replication |
| Postgres writes | 8k w/s | 25k w/s | filings partitioned; reads via replicas 10:1 |
| TigerBeetle | 20k tx/s | 150k tx/s | TB single cluster >100k/s; 6-node (2 AZ x 3) |
| OpenSearch | 20k docs/s | 60k docs/s | audit+search; 6 data nodes, 1 shard/50GB |
| MinIO | 2GB/s | 8GB/s | EC 8+4 across 12 nodes |

## Scale up / down policy summary

- **HPA per service** (`infra/helm/values-prod.yaml`): cpu 65% utilization +
  `http_requests_per_second` 700/pod; Kafka consumers add a
  `kafka_consumer_lag` external metric (AverageValue 5000). min 4 / max 80
  (lower for admin/auxiliary services).
- **Scale up fast:** 30s stabilization, +100% of current pods per 30s.
- **Scale down slow (tax-season safe):** 15-min stabilization window, max
  2 pods per minute.
- **PDB:** minAvailable=2 everywhere (1 for small admin services).
- **Grace:** preStop sleep 15s + terminationGracePeriodSeconds 45 so
  in-flight requests drain.
- **KEDA:** cron pre-scale 06:00 Africa/Lagos before filing windows; Kafka
  lag scalers per consumer; ML serving (kyc-worker) on queue depth 2→20 pods
  (4 CPU / 8Gi per pod); ollama fixed 3 replicas, `OLLAMA_NUM_PARALLEL=4`,
  keep-alive 10m.
- **Shed order** (see `infra/LOADSHEDDING.md`): batch/export → search
  autocomplete → non-critical reads; never shed payments/filings POST below
  the hard cap. Edge limits in `infra/apisix/limit-plugins.yaml`.

## Rollout order (SPEC B section 6)

1. PgBouncer + partition migration → 2. APISIX limit plugins →
3. HPA+KEDA per service → 4. Kafka repartition (mirror topics, dual-produce,
cut over) → 5. TB 6-node expansion → 6. Redis cluster → 7. k6 load-test
`tax_season.js` at 2x peak for 2h. Sign-off: no 5xx > 0.1%, lag recovery <
10min post-peak.

## What does NOT apply in docker-compose dev

Per SPEC B section 6 and SPEC C section 1:

- **No HPA / KEDA / PDB** — `infra/helm/values-dev.yaml` pins 1 replica,
  autoscaling off.
- **No Cilium/Hubble/Tetragon** — Cilium is helm/k8s only; dev isolation
  relies on compose networks. Parity gap is accepted and documented; do not
  attempt to run Cilium locally.
- **No multi-replica data tier** — single Postgres/Redis/Redpanda/MinIO;
  partitioning and ISM scripts are still idempotent and safe to run, but
  replica/EC sizing is a no-op.
- **No APISIX rate-limit fleet** — compose runs a single gateway node;
  `limit-req` counters are per-node, so limits are effectively relaxed.
- Secrets use dev defaults only (`MERIDIAN_DEV_JWT_SECRET`, `TIN_HMAC_KEY`).
