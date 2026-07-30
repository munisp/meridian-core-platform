# admin-api — Management/Admin backend (Go, stdlib-only)

Aggregation/auth backend for the Meridian admin console (SPEC §2). Zero external
dependencies; runs standalone in dev with seeded fallback data.

## Run

```bash
export PATH=$HOME/sdk/go/bin:$PATH
go run .            # PORT=8095 default
go build ./... && go vet ./...
```

Config (env): `PORT` (8095), `AUTH_MODE` (`dev` accepts `X-Dev-Role` header),
`MERIDIAN_DEV_JWT_SECRET` (HS256 dev secret), plus per-service URL overrides:
`RP_REGISTRY_URL`, `TIN_GRAPH_URL`, `RULES_ENGINE_URL`, `FEATURE_STORE_URL`,
`LEDGER_URL`, `NOTIFICATION_URL`, `AUDIT_EVIDENCE_URL`, `GEO_URL`, `GEO_RS_URL`,
`CONSENT_URL`, `REG_WATCH_URL`, `SEARCH_INDEXER_URL`, `SETTLEMENT_URL`,
`EDGE_POLICY_URL`, and plane-app URLs (`EINVOICING_URL`, `WHT_URL`, `ETR_URL`,
`VASP_CARF_URL`, `POS_VAT_URL`, `CASE_MGMT_URL`, `REV360_URL`, `TP_CBCR_URL`,
`ONBOARDING_URL`, `PRESUMPTIVE_URL`, `EDUCATION_URL`, `USSD_GATEWAY_URL`,
`ANALYTICS_URL`, `JRB_URL`, `OMBUD_URL`, `ENCLAVE_GATEWAY_URL`, …).

## API surface (all under Bearer JWT except login/healthz)

- `POST /v1/admin/login` — dev JWT issuer. Seed: `admin@meridian.local / admin123`
  (roles admin+board), `operator@…/operator123`, `auditor@…/auditor123`.
- `GET /v1/admin/overview` — counts: packs, tenants, workflows, transfers,
  evidence objects, gate states, service health.
- `GET|POST /v1/admin/tenants`, `GET|PUT|DELETE /v1/admin/tenants/{id}` —
  isolation levels `enclave|schema|row` validated.
- `GET|POST /v1/admin/users`, `PUT|DELETE /v1/admin/users/{id}`;
  `GET /v1/admin/identity/relations` (Permify tuples).
- `GET /v1/admin/services` — registry (15 core services + compliance/inclusion/gov
  plane apps) with concurrent `/healthz` rollup;
  `POST /v1/admin/services/{id}/toggle`.
- `GET /v1/admin/packs[/{id}]`, `POST /v1/admin/packs/{id}/{ver}/publish` (board) —
  proxies rp-registry, dev-seed fallback.
- `GET /v1/admin/gates`, `POST /v1/admin/gates/{id}/flip` (board, confirm=true) —
  proxies reg-watch, local fallback; `GET /v1/admin/gazette-watch`.
- `GET|POST /v1/admin/audit/events`; `GET|POST /v1/admin/evidence[/{id}]`
  (sha256 sealed, WORM URI); `POST /v1/admin/tat/assemble`.
- `GET /v1/admin/flows/matrix` (F1–F10), `GET|POST /v1/admin/flows/receipts`,
  `GET /v1/admin/flows/forbidden` (F9/F10 sightings — must be empty; POSTing an
  F9/F10 receipt is rejected 422 and recorded as a security sighting).
- `GET /v1/admin/ledger/accounts[/{id}/balance]`, `POST /v1/admin/ledger/transfers`,
  `POST /v1/admin/ledger/transfers/{id}/post|void` — proxies ledger svc, in-memory
  dev fallback (integer kobo only); `GET /v1/admin/ledger/recon-breaks`.
- `GET /v1/admin/workflows`, `POST /v1/admin/workflows/{id}/trigger`,
  `GET /v1/admin/workflow-runs`.
- `GET|PUT /v1/admin/settings/flags`, `GET|POST /v1/admin/settings/api-keys`,
  `POST /v1/admin/settings/api-keys/{id}/revoke`,
  `GET /v1/admin/settings/notifications`, `GET /v1/admin/settings/routes`,
  `POST /v1/admin/settings/waf-mode`.
- `/v1/admin/proxy/{service}/{path...}` — reverse-proxy pass-through to any
  registered service (admin JWT stripped, `X-Dev-Role: admin` injected).

## Graceful degradation

Every downstream-backed view tries the owning core service first (1.2s timeout)
and falls back to marked dev seeds (`"source": "dev-seed"` in responses) so the
console demo always works with zero external deps. Mutating admin-owned state
(tenants, users, flags, keys, registry toggles, local audit/evidence/receipts) is
in-memory for dev; restart re-seeds.

Errors are RFC7807 `application/problem+json`. CORS is permissive in dev.
