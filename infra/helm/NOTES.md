# Helm deployment notes (core-platform)

The reference deployment target is Kubernetes with the trust-zone split from
the unified architecture doc: core in the shared zone, planes in market /
sovereign zones. Charts are intentionally **not** generated here; capture
decisions as notes for the platform team:

- One namespace per zone: `meridian-core`, `meridian-market`, `meridian-sovereign`.
- Redpanda: 3 brokers, `nrs.*` topic families with `.dlq` companions, retention
  7d hot / tiered to MinIO (S3) for WORM-adjacent archive.
- Postgres 16 + PostGIS per stateful service (or schemas in one cluster for
  cost profile); TigerBeetle as a 3-node cluster with replicated state file.
- Temporal: visibility on Postgres, one namespace per plane.
- OpenSearch: 3 data nodes, ISM policy for `nrs-events-*`.
- Keycloak realm `meridian`: OIDC issuers per zone, JWKS consumed by every
  service (`OIDC_ISSUER_URL`, AUTH_MODE=prod).
- Permify: DSL bundles from `packages/permify-models/schemas/*.perm` loaded at
  chart install time via a job.
- APISIX: standalone config rendered by edge-policy (`GET /v1/routes`) and
  applied via Admin API on route-table change.
- Secrets: `MERIDIAN_DEV_JWT_SECRET`, `TIN_HMAC_KEY` from ExternalSecrets;
  dev defaults are for local only.
