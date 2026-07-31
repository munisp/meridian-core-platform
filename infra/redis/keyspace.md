# Redis keyspace + TTL spec (SPEC B sections 1 & 3)

Cluster: 3 shards x 32GB + replicas. Two logical uses: USSD/session state and
cache-aside. Rate-limit counters are **NOT** in Redis — APISIX `limit-req` /
`limit-count` own rate limiting (see `infra/apisix/limit-plugins.yaml`).

## Keyspace

| Key pattern | Type | TTL | Notes |
|---|---|---|---|
| `sess:{id}` | hash | **900s** | USSD/session state, ~40KB/session. 2M peak sessions ≈ 80GB across 3 shards. Refresh TTL on every interaction. |
| `cache:taxpayer:{tin}` | string (JSON) | **300s** | Cache-aside taxpayer-360 view. Value carries a `version` stamp; bump version on write to invalidate. |
| `kyc:queue:tasks` | list | none | ML serving backlog; KEDA list-length scaler (`infra/helm/templates/keda-scaledobject.yaml`). |
| `ratelimit:*` | — | — | **Reserved/absent.** Rate limiting via APISIX, not Redis Lua. |

## Operational rules

- `maxmemory-policy`: `volatile-lru` on session shards (all keys have TTL);
  never evict `kyc:queue:*` — pin it on a separate logical DB or instance.
- Persistence: AOF everysec for session shards; RPO ≤ 5min (SPEC B targets).
- Session loss is degradable (citizen re-auths); cache loss is harmless
  (cache-aside repopulates from Postgres/MinIO).
- Alerts: shard memory > 80%, evicted_keys > 0 on cache DB,
  `LLEN kyc:queue:tasks` sustained > 10k (KEDA should be scaling).
