# Meridian — Management/Admin Console

React 18 + TypeScript + Vite + Tailwind management plane for the NRS Unified
Platform (SPEC §2.1). Hand-rolled Tailwind components (no UI kit), low-saturation
warm-neutral palette (`sand`/`clay`/`moss`), no gradients.

## Pages (left nav)

1. **Dashboard** — service health rollup, control-plane counts (packs / tenants /
   transfers / evidence / gates), recent audit event feed.
2. **Applications** — card per registered service/app: all 15 core services plus
   compliance, inclusion and gov plane apps (served from the admin-api service
   registry), with health, T-items, version, link and enable/disable.
3. **Rule Packs** — rp-* list from rp-registry, detail view (YAML, provenance,
   ed25519 signature status), board-role publish action, stale-consumer alerts.
4. **Gates & Reg-watch** — gate table (G1/G2/G8, carf.transmit_enabled,
   qdmtt_upgrade, ombud.rules_active) with armed+confirmed flip modal, gazette watch.
5. **Ledger** — accounts browser (integer kobo), balance lookup, pending-transfer
   initiation, PSSP recon breaks.
6. **Workflows** — wf-* registry per plane, JSON trigger form, run history.
7. **Audit & Evidence (WORM)** — audit search (subject/type), WORM evidence viewer
   with **in-browser sha256 verification via WebCrypto**, TAT assembly.
8. **Tenants & Identity** — tenant CRUD with isolation level (enclave/schema/row),
   users & roles, Permify relation viewer.
9. **Cross-Zone Flows** — F1–F10 matrix, WORM receipt log, forbidden-flow monitor
   (F9/F10 — must always be empty).
10. **Settings** — APISIX route table, WAF mode, notification providers, feature
    flags, API keys.

## Run (dev)

Requires Node 20+ and the admin-api backend (`services/admin-api`, default :8095).

```bash
# terminal 1 — backend
cd services/admin-api
export PATH=$HOME/sdk/go/bin:$PATH
go run .                      # listens on :8095

# terminal 2 — console
cd admin
npm install
npm run dev                   # http://localhost:5173
```

Sign in with the seeded dev admin: **admin@meridian.local / admin123**
(also `operator@meridian.local / operator123`, `auditor@meridian.local / auditor123`).

The Vite dev server proxies `/v1/*` to `http://localhost:8095`
(override with `ADMIN_API_URL`). For a built bundle served elsewhere, point the
axios client with `VITE_ADMIN_API_URL`.

## Build

```bash
npm install && npm run build   # tsc -b && vite build → dist/
```

## Graceful degradation ("dev seed")

admin-api serves seeded data when downstream core services (rp-registry,
reg-watch, audit-evidence, ledger, settlement, edge-policy) are unreachable.
Views rendered from seeds carry an amber **dev seed** badge; live data is used
automatically when services come up (responses carry `source: live|dev-seed`).

## Honesty tags

- JWT auth is the dev HS256 issuer (`MERIDIAN_DEV_JWT_SECRET`); prod mode would
  validate Keycloak OIDC JWKS.
- Workflow triggers use dev in-process runner semantics (Temporal server optional).
- Ledger views fall back to an in-memory store when the ledger svc is down;
  money is always integer kobo.
- No real SMS/email is sent — notification providers are simulators.
