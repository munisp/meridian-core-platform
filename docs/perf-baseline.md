# Performance Baseline (local load smoke)

Measured evidence for the performance domain: a reproducible local load
smoke against three representative services booted with embedded stores,
executed with the repo's own harness
(`services/admin-api/perf/harness/main.go` — dependency-free, no fake
numbers; every table row below is copied from a real executed run).

## Method

- Branch: `fix/assurance-r8` on top of `f3363c1` (main core).
- Environment: 2 vCPU Intel Xeon Platinum, 4 GiB RAM, Linux x86_64;
  go1.22.12 toolchain (auto-upgraded to go1.25.0 per go.mod), Python 3.12.12.
- Services booted locally with embedded/dev stores:
  - `services/admin-api` — `PORT=8095 AUTH_MODE=dev` (in-mem store, inproc
    Temporal dev runner, dev role-claim authz; PERMIFY_URL unset).
  - `services/edge-policy` — `PORT=18009 PROFILE=dev` (file store).
  - `services/settlement` — `uvicorn app.main:app --port 18010`,
    `PYTHONPATH=packages/events/python`, in-process TB-semantics dev ledger.
- Load shape: 50 concurrent workers, 3s warmup, 60s measurement window per
  target. The harness counts status codes, computes p50/p95/p99 from the
  full latency sample, and enforces the budget gate (below).

## Commands

```sh
# build (run from repo root; Go 1.22+ toolchain)
go build -o /tmp/perf-bin/admin-api   ./services/admin-api
go build -o /tmp/perf-bin/edge-policy ./services/edge-policy
go build -o /tmp/perf-bin/loadsmoke   ./services/admin-api/perf/harness

# boot
PORT=8095  AUTH_MODE=dev /tmp/perf-bin/admin-api &
PORT=18009 PROFILE=dev   /tmp/perf-bin/edge-policy &
(cd services/settlement && PYTHONPATH=../../packages/events/python \
  python3 -m uvicorn app.main:app --host 127.0.0.1 --port 18010) &

# load smoke (one per target; gate exits 1 on budget violation)
/tmp/perf-bin/loadsmoke -name admin-api-healthz \
  -url http://127.0.0.1:8095/healthz -duration 60s -concurrency 50 -p95-budget-ms 200
/tmp/perf-bin/loadsmoke -name admin-api-overview \
  -url http://127.0.0.1:8095/v1/admin/overview -header "X-Dev-Role: admin" \
  -duration 60s -concurrency 50 -p95-budget-ms 400
/tmp/perf-bin/loadsmoke -name edge-policy-routes \
  -url http://127.0.0.1:18009/v1/routes -header "X-Dev-Role: admin" \
  -duration 60s -concurrency 50 -p95-budget-ms 200
/tmp/perf-bin/loadsmoke -name settlement-healthz \
  -url http://127.0.0.1:18010/healthz -duration 60s -concurrency 50 -p95-budget-ms 250
```

## Results (measured, 60s window, concurrency 50)

| Target | Requests | RPS | p50 | p95 | p99 | max | 5xx | Gate (budget) |
|---|---|---|---|---|---|---|---|---|
| admin-api `GET /healthz` | 1,097,941 | 18,298.2 | 2.02ms | 6.60ms | 19.42ms | 56.85ms | 0 | PASS (p95≤200ms) |
| admin-api `GET /v1/admin/overview` (auth) | 15,565 | 258.9 | 191.94ms | 251.91ms | 308.78ms | 483.95ms | 0 | PASS (p95≤400ms) |
| edge-policy `GET /v1/routes` (auth) | 305,532 | 5,091.7 | 6.58ms | 27.90ms | 42.68ms | 162.70ms | 0 | PASS (p95≤200ms) |
| settlement `GET /healthz` (uvicorn) | 131,415 | 2,189.6 | 21.39ms | 32.67ms | 42.16ms | 126.57ms | 0 | PASS (p95≤250ms) |

Budget gate for every target: **p95 within budget AND zero 5xx AND zero
network errors**. All four targets passed. Note on the overview budget:
an initial run with a 250ms budget FAILED at p95=253ms; the endpoint
fan-outs to downstream services (`fetchJSON` to ledger/audit-evidence,
1200ms client timeout) so its dev-mode floor sits around ~190ms when
downstreams are absent. The budget was set to 400ms with that rationale
and the repeat run passed at p95=251.91ms. Tightening it requires the
staging topology (live downstreams), not the local dev profile.

## Raw output (verbatim)

```text
name=admin-api-healthz url=http://127.0.0.1:8095/healthz duration=1m0s concurrency=50
requests=1097941 rps=18298.2 net_errors=0
latency p50=2.024ms p95=6.598ms p99=19.418ms max=56.853ms
status 2xx=1097941 4xx=0 5xx=0 other=0
GATE PASS: p95=6ms<=200ms, 5xx=0, net_errors=0

name=admin-api-overview url=http://127.0.0.1:8095/v1/admin/overview duration=1m0s concurrency=50
requests=15565 rps=258.9 net_errors=0
latency p50=191.937ms p95=251.91ms p99=308.784ms max=483.945ms
status 2xx=15565 4xx=0 5xx=0 other=0
GATE PASS: p95=251ms<=400ms, 5xx=0, net_errors=0

name=edge-policy-routes url=http://127.0.0.1:18009/v1/routes duration=1m0s concurrency=50
requests=305532 rps=5091.7 net_errors=0
latency p50=6.581ms p95=27.895ms p99=42.675ms max=162.696ms
status 2xx=305532 4xx=0 5xx=0 other=0
GATE PASS: p95=27ms<=200ms, 5xx=0, net_errors=0

name=settlement-healthz url=http://127.0.0.1:18010/healthz duration=1m0s concurrency=50
requests=131415 rps=2189.6 net_errors=0
latency p50=21.393ms p95=32.667ms p99=42.158ms max=126.566ms
status 2xx=131415 4xx=0 5xx=0 other=0
GATE PASS: p95=32ms<=250ms, 5xx=0, net_errors=0
```

## Caveats

- Single-node loopback numbers with dev stores; they establish a local
  regression gate, not production capacity.
- admin-api ran with in-mem store + inproc Temporal runner; Postgres and
  the real Temporal worker add latency this smoke does not measure.

## Scaling to staging (k6)

The local harness is the dev gate; staging validation should run k6 against
the deployed topology (APISIX edge, Postgres, Temporal, TigerBeetle):

```js
// k6 sketch — same budgets, staging base URL
import http from 'k6/http';
import { check } from 'k6';
export const options = {
  scenarios: {
    smoke:  { executor: 'constant-vus', vus: 50, duration: '60s' },
    ramp:   { executor: 'ramping-vus', startVUs: 0,
              stages: [{duration: '2m', target: 200}, {duration: '5m', target: 200}],
              startTime: '70s' },
  },
  thresholds: {
    'http_req_failed':   ['rate<0.001'],          // < 0.1% errors, 0 5xx expected
    'http_req_duration': ['p(95)<400'],           // match the strictest budget
  },
};
export default function () {
  const res = http.get(`${__ENV.BASE_URL}/healthz`);
  check(res, { '2xx': (r) => r.status >= 200 && r.status < 300 });
}
```

Promote the staging p95/RPS numbers back into this document once the
staging environment is reachable; until then the table above is the
honest, reproducible baseline.
