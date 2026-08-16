# Temporal Real-Worker Migration Assessment

Pinned source: `meridian-core-platform @ c31c8e93fdce7e0e742609c7e0d8ba082a5fa711`.

## Inventory (what actually uses temporal-sdkx at the pinned SHA)

Contrary to earlier planning notes ("~8 services inproc"), only **three Go
modules** import `packages/temporal-sdkx`, and exactly **one service
endpoint** executes workflow-shaped work. The honest inventory:

| Module / service | Usage | Runner today | Evidence |
|---|---|---|---|
| `packages/temporal-sdkx` | Runner interface, inproc dev runner, **real `TemporalRunner` already implemented** (HARDENING H3) | env-selected: `TEMPORAL_URL` set → real worker (`sdkx.go:264` `NewRunnerFromEnv`), unset → inproc | `packages/temporal-sdkx/worker.go:1-60`; `money/money_test.go:202-204` (env-selection test) |
| `packages/temporal-sdkx/money` | Money movement workflows; `NewRunnerFromEnv` (money.go:300-310) | env-selected, same contract | `money/money_test.go` |
| `workflows-go` | Shared wf-* primitives (`Compose`, `WFGateFlip`, `WFPackRollout`) built on sdkx sagas | executed by whoever embeds them; no dedicated worker binary exists | `workflows-go/workflows.go:30-120`, `workflows_test.go` |
| `services/admin-api` | `handleWorkflowTrigger` records a run row and returns `mode: dev-inproc` — **it does not execute any workflow through sdkx at all** | stub (no runner) | `services/admin-api/handlers_ledger.go:146-177` |
| Python services (settlement, rules-engine, reg-watch, feature-store) | No Temporal client; settlement uses an in-process TB-semantics dev ledger | n/a | `services/settlement/app/refund_execution.py:17-68` |

Infrastructure for real workers **already exists**: `temporalio/auto-setup`
+ `temporalio/ui` in `infra/docker-compose.prod.yml:119-147`, and a
`temporalWorker` workload in `infra/helm/values-prod.yaml:159-161`
(`deployment-temporal-worker.yaml` template).

## Gap summary

1. `workflows-go` primitives have **no worker process**: nothing calls
   `sdkx.NewRunnerFromEnv()` + `RegisterWorkflow` + `Start` in a deployable
   binary.
2. `admin-api` workflow triggers are bookkeeping stubs; the console shows
   "completed" runs that never executed.
3. Python services have no Temporal path at all (out of scope here; any
   future Python worker should use the official Python SDK against the same
   task queues, not a sdkx port).

## Per-service migration steps

### admin-api (reference implementation — done on this branch)

- Add `packages/temporal-sdkx` + `workflows-go` deps (go.work already covers
  both; `replace` directives added in `go.mod`).
- At startup: `runner := sdkx.NewRunnerFromEnv()`; if a real
  `*sdkx.TemporalRunner`, `Start()` it (inproc runner needs no start).
- `handleWorkflowTrigger`: look up the registered workflow for the
  definition id, execute through the runner, record status from the actual
  result (`completed`/`failed` with error), keep the audit append.
- Triggerable defs are registered as concrete sdkx workflows backed by the
  shared registry (`wf-activity.noop` for catalog entries without a plane
  implementation yet — honestly labelled).
- Tests: `handlers_admin_test.go`-style httptest coverage asserting (a)
  inproc fallback still works with `TEMPORAL_URL` unset, (b) failed workflow
  input surfaces as `failed`, not silent `completed`.

### workflows-go (done — fix/assurance-r8)

- Added `workflows-go/cmd/workflow-worker`: `sdkx.NewRunnerFromEnv`,
  registers `wf-gate-flip` / `wf-pack-rollout` (plane clients are HTTP
  calls to `GATE_PLANE_URL` / `PACK_PLANE_URL`; an unconfigured endpoint
  fails the run honestly) plus `wf-noop`, blocks on signal. Deploy via the
  existing helm `temporalWorker` workload (image becomes the worker
  binary, not `temporalio/auto-setup`).

### money workflows (done — fix/assurance-r8)

- `services/ledger` now calls `money.NewRunnerFromEnv()` at boot
  (fail-closed when `TEMPORAL_URL` is set) and registers the money sagas
  via `money.Register` with a `tbMoneyAdapter` over the service's
  `tb.LedgerClient` (`money_workflows.go`). Deterministic ids make
  activity retries replay idempotently; covered by
  `money_workflows_test.go` (capture happy-path, replay idempotency,
  honest failure on unknown account).

### Python services (deferred)

- No sdkx coupling exists; if durable execution is needed, adopt the
  official `temporalio` Python SDK with the same `TEMPORAL_URL` env contract.

## Rollout / compatibility

- `TEMPORAL_URL` unset → inproc runner (dev default, unchanged).
- `TEMPORAL_URL` set → real cluster; `TEMPORAL_TASK_QUEUE` (default
  `meridian-core`), `TEMPORAL_NAMESPACE` (default `default`).
- Inproc and Temporal runners register workflows under identical names, so
  cutover is config-only per environment.
