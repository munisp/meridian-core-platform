# Helm deployment notes (core-platform)

## Application services (B1 finding #5 — fixed)

`templates/deployment-services.yaml` renders a Deployment + Service for every
enabled entry in `.Values.services` with an in-repo port (single source of
truth: `meridian.servicePort` in `_helpers.tpl`, matching the source binds).
HPAs, PDBs and KEDA ScaledObjects only target enabled services, so every
scaler/disruption budget is backed by a real Deployment (verified by
`helm template` against both values files).

Honest gap: `filing-api`, `payments-api` and `kyc-worker` appear in values
(HPA/KEDA sizing from SPEC B) but have NO implementation in this repo; they
carry `enabled: false` and render nothing until the services exist.

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
  service (`KEYCLOAK_ISSUER` / `KEYCLOAK_JWKS_URL`, `AUTH_MODE=keycloak`).
  NOTE (R4 fix): `dev` and `keycloak` are the ONLY valid AUTH_MODEs —
  `AUTH_MODE=prod` is rejected fail-closed ("unsupported AUTH_MODE"),
  so this chart injects `AUTH_MODE=keycloak` into every service pod.
- Permify: DSL bundles from `packages/permify-models/schemas/*.perm` loaded at
  chart install time via a job.
- APISIX: standalone config rendered by edge-policy (`GET /v1/routes`) and
  applied via Admin API on route-table change.
- Secrets: `templates/secrets.yaml` defines `meridian-platform`
  (TAT_SEAL_KEY / TAT_CHAIN_HMAC_KEY / CONSENT_RECEIPT_KEY / TIN_HMAC_KEY),
  `grafana-admin`, `meridian-postgres` (default + monitoring namespaces) and
  `temporal-db` from `.Values.secrets.values`. The shipped values are
  OBVIOUS placeholders (`CHANGE_ME-...`) — replace via `--set
  secrets.values.X=...`, a sealed values file, or set `secrets.create=false`
  and provision the same names via ExternalSecrets.
  `MERIDIAN_DEV_JWT_SECRET` dev defaults are for local only.

## Production env reference (values-prod.yaml, R4)

Every value injected by `templates/deployment-services.yaml`:

| Value | Injected env | Consumed by |
|---|---|---|
| `environment` | `PROFILE` | all services (fail-closed prod gates) |
| `auth.mode` | `AUTH_MODE` | packages/events/auth (dev\|keycloak only) |
| `auth.keycloakIssuer` | `KEYCLOAK_ISSUER` | RS256/JWKS verifier (auth/keycloak.go) |
| `auth.keycloakJwksUrl` | `KEYCLOAK_JWKS_URL` | defaults to `<issuer>/protocol/openid-connect/certs` |
| `auth.keycloakAudience` | `KEYCLOAK_AUDIENCE` | aud claim check |
| `platform.kafkaBrokers` | `KAFKA_BROKERS` | services flagged `kafkaConsumer: true` |
| `platform.tigerbeetleAddresses` | `TIGERBEETLE_ADDRESSES` | ledger |
| `platform.otelExporterOtlpEndpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` | otelx (all services) |
| `platform.metricsPort` | `METRICS_PORT` | httpx metrics listener (all services); pods carry `prometheus.io/scrape` annotations |
| `secrets.values.postgres*` | `DATABASE_URL`/`POSTGRES_PASSWORD` | geo (from `meridian-postgres` Secret) |
| `secrets.values.tatSealKey` / `tatChainHmacKey` | `TAT_SEAL_KEY` / `TAT_CHAIN_HMAC_KEY` | audit-evidence (fatal without) |
| `secrets.values.consentReceiptKey` | `CONSENT_RECEIPT_KEY` | consent (fatal without) |
| `secrets.values.tinHmacKey` | `TIN_HMAC_KEY` | tin-graph |
| `secrets.values.grafanaAdmin*` | grafana admin login | grafana (`grafana-admin` Secret) |
| `secrets.values.temporalDb*` | `POSTGRES_USER`/`POSTGRES_PWD` | temporal (`temporal-db` Secret) |

Render check: `helm template meridian infra/helm -f infra/helm/values-prod.yaml`
(and `-f values-dev.yaml`) both render cleanly.
