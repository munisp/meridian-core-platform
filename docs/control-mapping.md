# SOC 2 Control Mapping — Meridian Core Platform

Pinned source: `meridian-core-platform @ c31c8e93fdce7e0e742609c7e0d8ba082a5fa711` (`main`).

This document maps **only controls that exist in the repository at the pinned
SHA** to AICPA Trust Services Criteria (TSC) categories. Every row cites the
implementation path and test evidence. Nothing here is aspirational: controls
marked **partial** are explicitly called out with the gap. If a control is not
in this table, it is not implemented in this repo and must not be claimed.

Legend: **Status** = `implemented` (code + test evidence in repo) or
`partial` (code exists but an operational element is unproven — gap stated).

TSC categories: **S** Security (CC-series), **A** Availability,
**PI** Processing Integrity, **C** Confidentiality, **P** Privacy.

## Control inventory

| # | Control | TSC | Implementation (path:line @ c31c8e9) | Test evidence | Owner role | Frequency | Exception handling | Status |
|---|---------|-----|--------------------------------------|---------------|-----------|-----------|--------------------|--------|
| CM-1 | Service authentication: RS256 JWT verification against Keycloak realm JWKS (prod); HS256 dev issuer only in `PROFILE=dev` | S | `packages/events/auth/keycloak.go:53` (`KeycloakVerifier`), JWKS cache TTL 5 min `keycloak.go:51`; prod activation log `packages/events/auth/auth.go:120` | `packages/events/auth/keycloak_test.go`; `packages/events/python/tests/test_keycloak.py` | Platform security engineer | Continuous (every authenticated request) | Prod profile without Keycloak config refuses dev-secret fallback (fail-closed at startup) | implemented |
| CM-2 | Centralized authorization via Permify (tenant-scoped permission checks; RBAC role→permission mapping) | S | `packages/permify-models/client.go:62` (client), permission constants `client.go:30-33`; enforcement `services/tin-graph/permify_gate.go:1-30`, `services/admin-api/permify.go`; schemas `packages/permify-models/schemas/*.perm` | `packages/permify-models/client_test.go`, `checker_test.go`, `schema_test.go`; `services/tin-graph/permify_gate_test.go`; `services/admin-api/permify_test.go` | Platform security engineer | Continuous (per privileged action) | `PROFILE=prod` + `PERMIFY_URL` unset → startup fails closed (`permify_gate.go:11-12`); dev falls back to JWT role claims, logged at startup | implemented |
| CM-3 | WORM audit evidence store: content-addressed, hash-sealed evidence objects; MinIO object-lock backend in prod | S, PI, C | `services/audit-evidence/internal/evidence/evidence.go:200-276` (`WormStore`, `Put/Get/Verify`); MinIO backend `internal/evidence/minio.go:1-30`; bucket provisioning with object-lock `infra/docker-compose.prod.yml:192-231`; seal key fail-closed `services/audit-evidence/main.go:32-37` | `services/audit-evidence/internal/evidence/evidence_test.go`; `services/audit-evidence/main_test.go` | Compliance engineer (evidence), platform engineer (bucket) | Continuous (per evidence write) | Prod without `TAT_SEAL_KEY` → `log.Fatal` at startup; object `Verify` detects tamper via sha256 sidecar | implemented |
| CM-4 | Database backup automation: daily `pg_dump` of all service databases, retention, optional MinIO ship with versioning/retention guidance; K8s CronJob alternative | A | `infra/backup/backup.sh:1-40` (env-configured dump cycle, 30-day local retention default); `infra/backup/cronjob.yaml:13` (schedule `0 3 * * *`); `infra/backup/cron-loop.sh` | Manual/scripted; runbook `docs/restore-runbook.md` | Database/SRE on-call | Daily (03:00 UTC) | Dump failure aborts cycle (`set -eu`); missing `mc`/bucket degrades to local-only copy with warning | **partial** — scheduled backups implemented; **quarterly restore rehearsal procedure documented (`docs/restore-runbook.md`) but no completed rehearsal evidence is filed in-repo** |
| CM-5 | Observability + alerting: Prometheus scrape, Alertmanager routing, 14 alert rules incl. ServiceDown, APISIX 5xx/latency, DLQ depth warn/crit, Temporal errors, TigerBeetle unavailable | A, PI | `infra/observability/alerts.yml` (14 `alert:` rules), `infra/observability/prometheus.yml`, `infra/observability/alertmanager.yml`; Grafana provisioning `infra/observability/grafana/` | Config lint via CI boot smoke; alert rule syntax loadable by Prometheus | SRE on-call | Continuous | Alertmanager routes to configured receivers; no paging integration evidence in repo (receiver config is environment-supplied) | **partial** — rules and dashboards implemented; on-call paging/escalation integration is environment config, not evidenced in repo |
| CM-6 | CI gates: build/vet/race-test every Go module; pytest per Python service; frontend typecheck+build; Rust check | S, PI | `ci/workflows/ci.yml` jobs `go` (build+vet+`go test -race`, lines 8-21), `python` (23-42), `admin-frontend` (44-56), `geo-rs` (58-70); `ci/workflows/ml.yml` | The workflows themselves; green runs required on `main`/`hardening/**` pushes and PRs | Engineering (all) | Every push / PR | Failing job blocks merge; no bypass mechanism in workflow config | implemented |
| CM-7 | Least-privilege database roles: per-service Postgres roles with schema-scoped GRANTs and default privileges | S, C | `infra/postgres/migrations/0003_roles.sql:1-84` (per-service roles, `GRANT USAGE` + table DML per schema, `ALTER DEFAULT PRIVILEGES`; keycloak role lines 83-84) | Idempotent migration; boot verification via service connectivity | Database administrator | Applied at provision; reviewed on schema change | Services hold no cross-schema rights by construction; superuser remains DBA-only | implemented |
| CM-8 | Fail-closed deployment gates: prod profile refuses dev fallbacks (ledger TigerBeetle client, audit seal key, consent gate, Permify gate) | S, PI | Ledger: `services/ledger/main.go:35-42` (profile guard), `main.go:77` (prod TB connect failure → fatal, no in-mem fallback); audit: `services/audit-evidence/main.go:32-37`; consent gate: `services/tin-graph/consent_gate.go:88-123`; permify: `services/tin-graph/permify_gate.go:11-12` | `services/ledger/profile_guard_test.go`; `services/tin-graph/consent_gate_test.go`, `permify_gate_test.go` | Platform engineer | At every service startup | Startup `log.Fatal` (service never serves with a degraded control) | implemented |
| CM-9 | NDPA consent lifecycle: consent records with lawful basis, HMAC-sealed receipts, consent check gate, breach workflow, DSR export with legal hold | P, C | Consent model + receipts `services/consent/main.go:28-60`; check gate `services/consent/check.go:1-95`; breach workflow `services/consent/breach.go:92-192`; DSR export/legal hold `services/consent/dsr.go:29-108` | `services/consent/check_test.go`, `breach_test.go`, `dsr_test.go`, `main_test.go` | Data protection officer (process), platform engineer (service) | Continuous (per data-access gate); breach: per incident | Consent service unreachable → verification denied (fail-closed, `consent_gate.go:121-123`); unknown lawful basis → 400 | implemented |
| CM-10 | Edge protection: APISIX gateway with jwt-auth, rate limiting (limit-req/limit-count), WAF mode managed detect→enforce by edge-policy service | S, A | `infra/apisix/config.yaml:24-31` (ssl, jwt-auth, limit plugins); route table + WAF mode `services/edge-policy/main.go:1-40`; plugin configs `infra/apisix/limit-plugins.yaml` | `services/edge-policy/main_test.go` | Platform/security engineer | Continuous | WAF persists mode across restarts; OTP endpoints capped 100/day (`limit-plugins.yaml:59-64`) | implemented |
| CM-11 | Immutable raw-data retention: `kyc-raw` bucket WORM 7-year compliance-mode retention + versioning; DR mirror guidance | C, A | `infra/minio/setup-buckets.sh:29-38` | Scripted provisioning; restore runbook references (`docs/restore-runbook.md`) | Platform engineer | At provision; verified in restore rehearsal | Compliance mode prevents deletion/overwrite even by bucket admin until retention expiry | implemented |
| CM-12 | Event bus integrity: dev inproc bus vs Kafka/Redpanda with DLQ topics; unknown mode falls back with explicit log | PI, A | `packages/events/bus/bus.go:31-57` (mode selection), DLQ routing `bus.go`/`kafka.go`; Python parity `packages/events/python/meridian_events/bus.py:23-77` | `packages/events/bus/validate_test.go` | Platform engineer | Continuous | Handler exceptions route to `<topic>.dlq`; DLQ depth alerted (CM-5) | implemented |

## Explicit non-claims (do NOT assert these)

1. **No SOC 2 audit has been performed.** This is a control-to-TSC mapping of
   implemented code, not an auditor opinion.
2. **Restore rehearsal is documented, not yet evidenced** (CM-4).
3. **On-call paging integration is not in-repo** (CM-5).
4. **No PCI DSS controls are claimed** — see `docs/pci-scope-memo.md` for the
   applicability determination input.
5. **Authz tenancy is a shared Permify tenant (`t1`)** — isolation model and
   limits are documented in `docs/authz-tenancy.md`.
6. **Temporal workflows run on the sdkx inproc runner in dev;** real-worker
   wiring exists but per-service migration status is in
   `docs/temporal-migration.md`.
7. **Container images are tag-pinned, not digest-pinned** — policy and
   deploy-time resolution in `docs/image-pinning.md` / `scripts/pin-images.sh`.

## Maintenance

- Owner: platform security engineer; reviewed quarterly or on any control
  change (whichever first).
- Every new control claim MUST cite path:line at a pinned SHA and test
  evidence, or it does not belong in this table.
