# Funds-Flow Hardening — State Machines, Idempotency, Recovery

This document describes how money moves through Meridian after the
funds-flow atomicity hardening (audit: `funds-flow-audit.md`). Every flow
below is **idempotent by construction** and has a **recovery worker** for
the crash windows. Amounts are always integer kobo; the ledger is
double-entry (TigerBeetle semantics: pending → post/void, two-phase).

## Flow catalogue

| # | Flow | Service | Ledger | Idempotency key(s) | Recovery worker |
|---|------|---------|--------|--------------------|-----------------|
| 1 | PSM PSSP capture | inclusion `presumptive` | 200 | `Idempotency-Key` on CreateIntent; `capture:{payment_id}` at PSSP; transfer ids `psm-intent:/psm-post:/psm-fee:/psm-compensation:{payment_id}` | `RecoverySweeper` (boot + 60s) |
| 2 | Refund fast-track | core `settlement` | 400 | `refund_id = sha256(tin, period, tax_type)`; transfer ids `ref-pend:/ref-post:/ref-fund:{refund_id}` | `RefundExecutor.sweep_pending` (`POST /v1/refunds/sweep`) |
| 3 | WHT remittance | compliance `wht` | wht_credits table | credit id `cr-{batch_id}-{deduction_id}`; batch id `remit-{period}-sha256(deduction ids)[:8]`; deduction `Idempotency-Key` | single-transaction step + activity retry; recon step |
| 4 | Agent float & commissions | inclusion `presumptive`/`onboarding` | 500/700 | float movement id = hash(kind, reference); `float-pending:/float-post:{kind}:{reference}`; commission payout marker per (agent, period) | `SweepFloatMovements` (recovery worker) |
| 5 | POS VAT settlement | compliance `pos-vat` | 300 | `settled_periods(tenant, period)` marker; pending ids `posv-pend:{tenant}:{period}:{leg}` | resume-from-marker on re-run |
| 6 | PSSP fee leg | inclusion `presumptive` + core recon | 200 | `psm-fee:{payment_id}` | recovery sweeper reposts |
| 7 | Pending expiry | core `ledger` | all | `timeout_seconds` per pending | sweeper goroutine (30s, boot pass) |
| 8 | Event publication | core `settlement` | — | outbox `dedup_key` per event | `OutboxRelay` (at-least-once) |
| 9 | Settlement recon | core `settlement` | — | `recognised_references` per reference; investigation case `case:{reference}`; `Idempotency-Key` on pull-run | auto-heal investigation cases |
| 10 | Money sagas | core `temporal-sdkx/money` | via LedgerPort | workflow entity ids (payment/refund/run) | Temporal server (prod) / saga compensation |

## 1. PSM capture (inclusion `presumptive`)

States: `intent → pending_authorisation → authorised → captured_awaiting_post → captured`
(failure exits: `failed`, `compensated`, `expired`).

- **Single durable write**: at PSSP capture success, the payment record is
  persisted ONCE with `status=captured_awaiting_post` **and** the
  deterministic `post_transfer_id` (`psm-post:{payment_id}`) and `fee_kobo`
  — before any ledger post. A crash after the ledger post is resumable
  because the post id is already durable.
- **RecoverySweeper** (`recovery.go`, boot sweep + 60s interval):
  - `captured_awaiting_post` → idempotently `PostPendingAs(pending, post)`,
    post fee leg, issue certificate (idempotent per payment), mark
    `captured`. Permanent post failure → compensate (reversal **only if the
    post actually landed**, verified by `LookupTransfer`; PSSP refund).
    Legacy records without a post id are compensated *without* a ledger
    reversal (no invented money).
  - `intent/pending_authorisation` older than **30 min** → void hold,
    status `expired`.
- **Idempotency**: `Idempotency-Key` on `CreateIntent` (24h TTL, store
  `idempotency`); PSSP capture calls carry key `capture:{payment_id}`;
  all ledger transfers use deterministic ids with TB dedup semantics
  (replay = same id returned, conflict = error).

Timeout values: intent TTL 30m; idempotency TTL 24h; sweep interval 60s.

## 2. Refund fast-track (core `settlement`)

States: `decision(auto_approve|manual_review|standard) → pending → posted`
(exits: `post_failed`→voided, `voided`).

- Idempotency: one refund per **(tin_hash, period, tax_type)**
  (`refund_id` = sha256). Double-submit returns the stored execution (200
  replay) — one transfer, guaranteed.
- `auto_approve` (≤ ₦5,000,000): executes immediately through the refund
  workflow: pending transfer refund-treasury → taxpayer → single durable
  write of both transfer ids → `post_pending_as`. Post failure → pending
  voided, execution `post_failed`.
- Above the cap: `manual_review` event; a human executes the SAME workflow
  via `POST /v1/refunds/{refund_id}/approve`.
- `prior_breaks` is read server-side from the breaks store (no caller
  self-certification).
- Recovery: `sweep_pending()` (endpoint + call on boot) resumes crash-after-
  pending executions from actual ledger state.
- Pending TTL: `REFUND_PENDING_TTL_SECONDS` (default 1800s).

## 3. WHT remittance (compliance `wht`)

`collect → aggregate → generate-files → post-credits-and-mark-remitted → reconcile`

- **post_credits idempotent**: credit id is deterministic
  (`cr-{batch_id}-{deduction_id}`) and the batch id is deterministic per
  deduction set; dedup on both the credit PK and `source=deduction_id`.
- **Atomicity**: credits + `remitted` flags commit in ONE SQL transaction.
  Crash = rollback = clean retry. The `reconcile` step asserts Σcredits ==
  Σdeductions for the run.
- Deduction creation accepts an `Idempotency-Key` (deterministic deduction
  id; replay returns the original).

## 4. Agent float & commissions (inclusion)

- Float top-up/debit is a saga: pending (deterministic id per
  (kind, reference)) → movement record (`float_movements`, id = hash of
  (kind, reference) — the reference dedup key) → post. Compensation voids
  the pending. **Reference replay returns the existing movement (200).**
- The float treasury account carries `DEBITS_MUST_NOT_EXCEED_CREDITS`:
  unfunded top-ups fail (was: silently negative).
- Recovery: `SweepFloatMovements` finishes or voids `pending` movements.
- Commissions (ledger 700): per-(agent, period) `commission_payouts`
  marker; payout is pending → mark → post (deterministic ids
  `comm-pending:/comm-post:{agent}:{period}`). Re-running a settled period
  is a no-op; pool funding is idempotent per period.

## 5. POS VAT settlement (compliance `pos-vat`)

- `settled_periods(tenant, period)` marker table (durable append-log),
  checked BEFORE any posting: settled → 200 no-op replay; `pending` →
  resume from ledger state (`GetTransfer` per leg).
- Federal + state legs settle as a **compensated pair**: both pendings
  (deterministic ids) → post federal → post state; if the state leg fails,
  it is voided and the federal leg is reversed (`posv-rev:...`) — the pair
  never splits. Dev ledger now dedupes client-supplied transfer ids.

## 6. PSSP fee leg

Capture posts gross to collections, then the fee (`gross − settled`) from
collections to a dedicated **PSSP fee-income account** (ledger 200,
namespace `200000000002`, id `psm-fee:{payment_id}`). Recon therefore
balances by construction: `platform gross == pssp net + fee` and
`treasury == pssp net`; the core reconciler treats exactly this shape as
matched (`_fee_accounted`) — any other delta remains an amount-mismatch
break.

## 7. Pending-expiry sweeper (core `ledger`)

- `PendingTransfer` accepts `timeout_seconds` → `expires_at` persisted.
- Sweeper goroutine (`LEDGER_SWEEP_INTERVAL_SECONDS`, default 30s; boot
  pass included) voids expired unresolved pendings and emits
  **`nrs.ledger.pending_expired.v1`** per expiry via the outbox. Posting an
  expired pending fails (`pending_transfer_not_pending`).

## 8. Event publication — outbox pattern (core `settlement`)

Events are written to the `FileOutbox` in the same request as the state
change; the `OutboxRelay` publishes **at-least-once**. Consumers MUST dedup
on the documented `dedup_key`:

| Event | dedup_key |
|-------|-----------|
| `nrs.revenue.settled.v1` | `revenue:{reference}` |
| `nrs.refund.executed.v1` | `refund:{refund_id}` |
| `nrs.refund.manual_review.v1` | `manual_review:{refund_id}` |
| `nrs.recon.investigation.v1` | `case:{reference}` |
| `nrs.ledger.pending_expired.v1` | `transfer_id` (unique per transfer) |

## 9. Settlement recon — pull mode + auto-heal (core `settlement`)

- `POST /v1/recon/pssp/pull-run`: the service **pulls** the PSSP side via
  an adapter (`PSSP_REPORT_URL` in prod; the **sim adapter is honestly
  tagged** `adapter=sim, honest=true`) instead of trusting caller-fed
  records. Platform/treasury sides come from durably ingested collections
  (`POST /v1/recon/ingest`, upsert by reference).
- **Auto-heal class**: break `missing_in=["pssp"]` within the tolerance
  window (`RECON_AUTO_HEAL_TOLERANCE_DAYS`, default 7) → investigation case
  auto-created (`investigation_cases`, deduped per reference), break moves
  to `investigating`.
- Revenue is recognised **once per reference, globally**
  (`recognised_references`); a re-submitted reference in a later run never
  double-counts revenue.

## 10. Temporal money workflows (core `packages/temporal-sdkx/money`)

`CaptureSaga`, `RefundWorkflow`, `RemittanceWorkflow` are real workflow
definitions over the saga primitive: deterministic transfer ids per
workflow entity, compensation on every leg (reverse only legs that landed,
verified via `GetTransfer`; void un-posted pendings).

- Dev: in-process runner (`NewRunnerFromEnv`, TEMPORAL_URL unset).
- Prod: `TEMPORAL_URL` set → registers on a real Temporal server; **fail-
  closed**: connection/start failure is a hard error, never a silent
  in-proc fallback for money flows.

## Remaining risks (honest)

1. **Real-PSSP double-capture**: provider idempotency is *relied upon* —
   the sim adapters honour `Idempotency-Key` faithfully; a real PSP that
   ignores the key could still double-charge. Contract tests against each
   PSP sandbox are required before go-live.
2. **Sim adapters**: PSSP settlement-report and payment sim adapters are
   honest dev stubs (`adapter=sim`); pull-mode recon against a live PSP is
   untested (no sandbox available in CI).
3. **No live Temporal server tested**: the money workflows are verified on
   the in-proc runner; registration against a real cluster is env-selected
   code that has not run in CI.
4. **Inclusion presumptive event publication** is still direct-publish
   after the DB write (at-most-once) — the Go services lack the Python
   outbox helper; the recovery sweeper republishes recovered captures, but
   a crash between commit and publish can lose the first emission.
   Consumers must therefore treat capture events as a hint and reconcile
   from state (flow 9 pull-mode).
5. **Single-node stores**: the dev/dev-fallback stores (JSON store,
   append-log, SQLite) are single-writer; multi-instance deployment needs
   the Postgres/cluster variants or unique-constraint enforcement moves to
   the DB.
6. **Legacy pre-hardening records** (`captured_awaiting_post` without a
   persisted post id) are compensated *without* ledger reversal — if such a
   record actually had a post on the ledger, ops must reconcile manually
   (flow 9 will surface it as a break).
7. **Clock dependence**: TTLs (idempotency 24h, intent 30m, pending
   timeouts) use the node clock; clock skew widens/narrows replay windows.
