# PCI DSS CDE Applicability — Memo Template (Input to Counsel / QSA)

Status: **template + current-state evidence**. This memo frames the questions
and supplies engineering evidence. It is **not a legal conclusion** and does
not determine PCI DSS applicability; that determination belongs to counsel
and/or a QSA.

Pinned source: `meridian-core-platform @ c31c8e93fdce7e0e742609c7e0d8ba082a5fa711`.

## 1. Determination questions counsel/QSA must answer

1. **Do any cardholder data flows exist?** Does Meridian (or any plane built
   on it) store, process, or transmit primary account numbers (PAN), CVV/CVC,
   expiry, or full track data — including transiently in logs, queues, ML
   features, or support tooling?
2. **Provider tokenization boundary.** If card payments are accepted, is the
   card data captured and tokenized entirely by a PCI DSS Level 1 PSP (e.g.
   paystack/flutterwave-style hosted fields or redirect), such that PAN never
   touches Meridian-controlled infrastructure? Where exactly is the trust
   boundary (browser → PSP directly, or via our edge)?
3. **System-component inclusion.** If the PSP integration uses our APIs as a
   pass-through (even without storage), which Meridian components are "in
   scope" as connected-to systems under PCI DSS v4.0 scoping rules?
4. **Shared-responsibility mapping.** Which SAQ type would apply (likely
   SAQ A vs SAQ D) given the integration pattern, and who attests?
5. **NIBSS / local scheme overlays.** Do Nigerian payment-scheme or NIBSS
   requirements impose card-data-adjacent obligations even if PAN is absent?

## 2. Current-state engineering evidence (repo-verifiable)

| Evidence item | Finding | Source |
|---|---|---|
| Card-data storage | Repo-wide content scan for PAN/CVV/track-data identifiers (account data keys, card table/column names) returns **no card-storage schema or field**; ledger amounts are `amount_kobo` integers on TigerBeetle (`services/ledger/`, `infra/postgres/migrations/`) | `grep` scan @ pinned SHA; `infra/postgres/init/001-schemas.sql` |
| Secrets in repo | No inline credentials in prod compose — all `${VAR}` references (`infra/docker-compose.prod.yml:1-8`); no committed `.env.prod` | compose header; `.env.prod.template` (template only) |
| Edge controls | All external traffic via APISIX with TLS, jwt-auth, rate limiting; WAF mode managed by edge-policy service (`detect`→`enforce` persisted) | `infra/apisix/config.yaml:6,28-31`; `services/edge-policy/main.go` |
| AuthN/Z | RS256 JWT via Keycloak JWKS; Permify centralized authz; least-privilege DB roles | `docs/control-mapping.md` CM-1, CM-2, CM-7 |
| Audit/WORM | Evidence store with object-lock; would support PCI DSS req. 10-style logging *if* scope is confirmed | CM-3, CM-11 |
| Tokenization boundary | **Not found in this repo** — no PSP card-tokenization integration code exists here; payment topics (`nrs.psm.payments.v1`) carry tax/payment events with pseudonymised IDs (`tin_hash`), not PANs | `infra/postgres/init/002-bronze-views.sql:23-40` |

## 3. Preliminary engineering position (for counsel to test)

Based on the evidence above, the core platform as implemented **does not
store, process, or transmit cardholder data**, and no card-present or
card-not-present capture path exists in this repository. If counsel confirms
that (a) no plane introduces PAN flows and (b) any future PSP integration
keeps PAN inside a Level-1 PSP boundary (hosted fields/redirect), the likely
conclusion is **no CDE exists** and PCI DSS applies, at most, via SAQ A to
the integrating merchant entities — not to this platform.

## 4. Action items if any answer changes

- Any future card-capture feature MUST be reviewed against this memo before
  merge; introduce PAN only behind a documented PSP tokenization boundary.
- If a CDE is ever established: network segmentation review (Cilium policies
  in `infra/cilium/`), key management (`packages/keyx/KEY_CEREMONY.md`), and PCI
  DSS v4.0 requirements mapping become mandatory follow-ups.

## Sign-off block (counsel/QSA)

| Role | Name | Determination | Date |
|------|------|---------------|------|
| Counsel | | | |
| QSA (if engaged) | | | |
| Platform engineering (evidence only) | | evidence accurate @ pinned SHA | |
