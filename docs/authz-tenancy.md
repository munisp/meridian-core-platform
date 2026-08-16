# Authorization & Tenancy — Permify Shared-Tenant Model

Pinned source: `meridian-core-platform @ c31c8e93fdce7e0e742609c7e0d8ba082a5fa711`.

## Current model (what is actually deployed)

All Meridian authorization checks run against **one shared Permify tenant**,
`PERMIFY_TENANT` (default **`t1`**):

- Client: `packages/permify-models/client.go:55` (`tenant` field, default
  `"t1"` at `client.go:63-65`); every check hits
  `POST /v1/tenants/{tenant}/permissions/check` (`client.go:123`).
- Schema: `packages/permify-models/schemas/core.perm` — a Permify `entity
  tenant` with relations `admin | operator | auditor | board` and permissions
  `manage / operate / read / govern`.
- Callers namespace **inside tuples**, not via Permify tenants:
  - tin-graph officer scope: `tenant:<tenant_id>#operate@user:<sub>`
    (`services/tin-graph/permify_gate.go:62`)
  - admin-api: `tenant:<tenant_id>#<permission>@user:<sub>`
    (`services/admin-api/permify.go:55`)

So "tenant isolation" today = **tuple-level isolation inside one Permify
tenant**: each business tenant is a `tenant:<id>` object with its own
relation tuples, and all checks are scoped to that object.

### What this guarantees

- A user holding `admin` on `tenant:t-a` has **no permissions** on
  `tenant:t-b` — the check API is object-scoped, so cross-tenant grants
  cannot arise from the schema alone.
- Role semantics are centralized and versioned (`schema_test.go` enforces
  parity between the `.perm` DSL and the Go constants).

### Risk boundary (honest limits)

1. **Single blast radius.** All tenants' tuples live in one Permify tenant
   namespace (one Postgres schema `permify`, one API surface). A bug in
   tuple-writing code, an over-privileged Permify API token, or a
   misconfigured bulk-write could create cross-tenant tuples. There is no
   server-side tenant boundary to catch it.
2. **No per-tenant schema variance.** All tenants share
   `schemas/*.perm`; a tenant needing a custom model (e.g. an extra
   relation) cannot diverge.
3. **Operational coupling.** Snapshot/restore, schema migration, and Permify
   upgrades are global — one tenant's maintenance window is everyone's.
4. **Audit attribution.** Permify-level tuple write audit is per-server, not
   per-business-tenant; tenant-scoped audit trails must be reconstructed by
   filtering on tuple object prefixes.
5. **Fail-closed compensating control.** `PROFILE=prod` without `PERMIFY_URL`
   refuses to boot (`permify_gate.go:11-12`); check transport errors are
   returned as errors and callers deny (`client.go:101-103` comment,
   `tin-graph/consent_gate.go:121-123` pattern). This limits silent
   *authorization bypass*, not cross-tenant tuple corruption.

This model is acceptable for a single-operator deployment (one revenue
authority, one platform team). It is **not** sufficient for multi-operator /
multi-jurisdiction SaaS without compensating controls on tuple writes.

## Migration path: per-tenant Permify tenants

Permify natively supports multiple tenants (separate schema + tuple stores
per tenant id). Target state: `tenant_id` = business tenant id, provisioned
at tenant onboarding.

1. **API changes (platform code)**
   - `permify-models.NewClientFromEnv`: stop defaulting to `"t1"`; resolve
     tenant per request. Add `Client.ForTenant(tid string) *Client` returning
     a shallow copy with `tenant: tid` (cheap: shared `http.Client`).
   - Callers (`admin-api/permify.go:55`, `tin-graph/permify_gate.go:62`)
     already compute `tenant` from the request context — pass it to
     `ForTenant(tid)` instead of interpolating only into the tuple object.
   - Tenant onboarding (rp-registry / admin-api): call Permify
     `POST /v1/tenants/create` + write the base schema (`core.perm`) as part
     of provisioning, idempotently.
2. **Tuple namespacing**
   - Inside a per-tenant Permify tenant, the object id no longer needs the
     tenant prefix: `tenant:core#operate@user:x` (singleton object per
     tenant) instead of `tenant:<tid>#operate@user:x`. Keep the `tenant`
     entity type so schemas remain source-compatible.
   - Provide a one-shot migration job: read all tuples from `t1` where
     object = `tenant:<tid>`, rewrite object to `tenant:core`, write into
     Permify tenant `<tid>`. Verify counts per tenant before cutover.
3. **Rollout**
   - Phase 0 (flag `PERMIFY_TENANCY=shared`, current default): no behavior
     change.
   - Phase 1 (`PERMIFY_TENANCY=dual`): write tuples to both `t1` and the
     per-tenant tenant; read from `t1`. Compare-check job samples reads
     against both and alerts on divergence.
   - Phase 2 (`PERMIFY_TENANCY=isolated`): reads switch to per-tenant
     tenant; `t1` kept as rollback for one release.
   - Phase 3: decommission `t1`; remove the dual-write flag.
   - Each phase is env-flagged per service so tin-graph and admin-api can
     migrate independently; prod fail-closed behavior is unchanged.
4. **Tests to add at Phase 1**: parity test asserting `Check` result equality
   across `t1` and per-tenant backends for a fixed tuple fixture
   (extend `packages/permify-models/client_test.go`).

## Non-goals

- This note does not change any code; it documents the deployed model and
  the migration design. Implementation is tracked as follow-up work.
