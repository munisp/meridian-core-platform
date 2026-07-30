# Meridian ML Stack

End-to-end, CPU-only machine-learning stack for the Meridian tax platform:
fraud detection, credit scoring, graph (GCN) fraud-ring detection, MCMC
anomaly thresholding, and a fusion ensemble — with a versioned registry,
FastAPI serving, drift/performance monitoring, and a continual-training loop.

## Architecture

```
 platform DBs / event bus (txs.events, filings.events)
        │
        ▼
 ml/data            synthetic generator (realistic Nigerian patterns) +
                    production extract (Postgres → parquet → features) + lakehouse writer
        ▼
 ml/training        train.py / finetune.py / continual.py / evaluate.py
        ▼
 ml/registry        file-based registry: manifest.json + weights/*.pt.b64
                    (MLflow adapter when MLFLOW_TRACKING_URI is set)
        ▼
 ml/serving         FastAPI CPU inference  ──►  ml/monitoring   drift (PSI/KS),
        │                                       latency histograms, alerts → Kafka
        ▼
 ml/pipelines       kafka_consumer.py (events → features → score → emit)
                    continual_trigger.py (scheduled retrain, Ray optional)
        └──────────────► loop back into ml/training (continual.py)
```

## Registry contract

`ml/registry/manifest.json`:

```json
{
  "fraud": {
    "active": "v1",
    "versions": {
      "v1": {
        "weights": "weights/fraud-v1.pt.b64",
        "baseline_stats": "baseline/fraud-v1.json",
        "metrics": {"auc": 0.0}
      }
    }
  }
}
```

Weights are `torch.save` state_dicts, base64-wrapped (`.pt.b64`) so they can
travel through text-only file APIs. Serving decodes them at load time.

## Serving (ml/serving)

```bash
pip install -r ml/requirements.txt        # fastapi, uvicorn, torch (cpu), ...
ML_REGISTRY_DIR=ml/registry python -m serving.main   # or: uvicorn serving.main:app --port 8090
```

Endpoints:

| Endpoint | Purpose |
|---|---|
| `POST /v1/score/fraud\|credit\|graph\|anomaly\|fusion` | single record or `{"records": [...]}` batch |
| `GET /healthz` / `GET /readyz` | liveness / readiness (readyz reports per-model load state) |
| `GET /v1/ab/metrics` | champion/challenger counters |
| `GET /v1/monitoring/drift\|performance\|alerts` | monitoring snapshots |

Request: `{"entity_id": "<pseudonymised id>", "features": [..floats..], "amount_kobo": 1500000}`.
**Integer kobo in/out** — monetary values are never floats.

**Hot-skip**: a model whose weights are missing/corrupt returns
`503 application/problem+json` (RFC7807) and the failure is remembered; the
process never crashes and other models keep serving.

**Auth** (HARDENING.md H2): `AUTH_MODE=dev` accepts an HS256 bearer token
(`MERIDIAN_DEV_JWT_SECRET`) or `X-Dev-Role`; `AUTH_MODE=keycloak` trusts the
caller stamped by the enclave gateway. Raw NIN/TIN/MSISDN are never logged —
only truncated SHA-256 hashes.

### Measured CPU latency

Single record (10-feature fraud MLP, FastAPI TestClient round-trip incl.
HTTP + auth + monitoring hooks, n=300, warm):

```
p50 = 1.67 ms   p95 = 1.81 ms   p99 = 1.94 ms
```

Comfortably under the **p95 < 50 ms** target. Pure in-handler inference is
~0.15 ms; the remainder is HTTP/framework overhead. Real-model latency will
scale with network size and feature dimension but has ~45 ms of headroom.

## A/B testing & shadow mode (ml/serving/ab.py)

Config via `ML_AB_CONFIG` (JSON file) or env:

```bash
ML_AB_MODEL=fraud ML_AB_CHALLENGER_VERSION=v2 ML_AB_CHALLENGER_PCT=10   # 10% to challenger
ML_AB_SHADOW=true                                                       # score challenger, serve champion
```

- Assignment is **sticky**: SHA-256(entity id) → bucket in [0,100).
- **Shadow mode**: challenger is scored on every eligible request but the
  champion response is always served; shadow scores are counted separately.
- `GET /v1/ab/metrics` exposes per-arm requests/served/shadowed/errors,
  average score, and average latency.

## Monitoring (ml/monitoring)

- **Drift** (`drift.py`): sliding window (default 500) of live feature
  vectors per model vs training baseline stats from the registry manifest.
  PSI (Laplace-smoothed, alert ≥ 0.25) and two-sample KS (alert ≥ 0.15,
  baseline reconstructed spread within bins). Endpoints/env:
  `ML_DRIFT_PSI_THRESHOLD`, `ML_DRIFT_KS_THRESHOLD`, `ML_DRIFT_WINDOW`.
- **Performance** (`performance.py`): latency histograms (p50/p95/p99),
  score-distribution shift vs a baseline mean, alert rules
  (`ML_PERF_P95_ALERT_MS`, default 50).
- **Alerts** go to Kafka topic `ml.monitoring.alerts` when `KAFKA_BROKERS`
  is set and kafka-python is installed (`profile=prod`), else an append-only
  JSONL file `ml-monitoring-alerts.jsonl` (`profile=dev`).

## Pipelines (ml/pipelines)

- `kafka_consumer.py` — consumes `txs.events, filings.events`, pseudonymises
  NIN/TIN/MSISDN to hashes, builds an online feature vector, calls serving,
  emits `ml.scored.events`. Dev fallback: NDJSON from stdin/`ML_EVENTS_FILE`
  → `ml-scored-events.jsonl`.
- `continual_trigger.py` — scheduled invocation of the training contract
  `python -m training.continual --since <window>` (default `7d`,
  every `ML_CONTINUAL_INTERVAL_HOURS`=24h, `--once` for cron/Temporal).
  **Ray is optional**: if `import ray` succeeds, `ray.init()` runs
  (`RAY_ADDRESS=auto` attaches to an existing cluster, giving distributed
  data-parallel training across workers; local mode otherwise). Without Ray
  the same command runs sequentially.

## Continual training loop

1. Platform events accumulate in the lakehouse (ml/data).
2. `continual_trigger.py` fires on schedule → `training.continual --since 7d`.
3. New candidate version registered in `ml/registry/manifest.json`.
4. Promote via champion/challenger (shadow first, then 10% → 100%).
5. Drift/performance alerts gate rollback.

## Honest limitations

- **Synthetic data only** until real production transaction data flows; no
  labelled real fraud cases exist yet (operational next step).
- **CPU-only**; no GPU training or inference. GCN is pure torch
  (no torch-geometric). MCMC is hand-rolled Metropolis-Hastings in numpy.
- **Ray and MLflow are optional extras** (`pip install ray[default] mlflow`);
  everything above runs without them.
- Serving reconstructs a generic MLP from state_dict Linear layers; exotic
  architectures need an adapter in `serving/registry_client.py`.
