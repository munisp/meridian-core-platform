# In-service load shedding middleware spec (SPEC B section 4)

Edge rate limiting lives in APISIX (`infra/apisix/limit-plugins.yaml`). This
document specifies the **in-service** shedder each service should adopt. This
is a spec only — services are NOT modified by this change; each service team
wires the middleware into its own router.

## Algorithm (identical for Go chi and FastAPI)

1. **Token bucket per route class.** Classes (from `X-Load-Class` header set
   by APISIX, default `standard`): `critical` (payments/filings POST),
   `standard`, `sheddable-search`, `sheddable-batch`.
2. **Shed trigger:** p95 latency > 600ms **or** pod CPU > 85%.
3. **Shed order (never inverted):**
   1. `sheddable-batch` (exports/batch jobs) → `503` + `Retry-After: 30`
   2. `sheddable-search` (autocomplete) → `503` (stale cache allowed instead)
   3. non-critical reads (`standard` GET) → serve stale cache or `503`
   4. **Never** shed `critical` (payments/filings POST) below the hard cap —
      only the APISIX `limit-req` 20 r/s per-TIN cap applies.
4. **Concurrency guard:** semaphore = `4 x vCPU` per pod; requests that cannot
   acquire a slot within a short wait → `429`.

## Go (chi) snippet

```go
// shedder.go — reference implementation sketch; copy into services and adapt.
func ShedMiddleware(class string, maxConc int) func(http.Handler) http.Handler {
    sem := make(chan struct{}, maxConc) // 4 * runtime.NumCPU()... use vCPU
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            lc := r.Header.Get("X-Load-Class")
            if lc == "" { lc = class }
            if shouldShed(lc) { // p95 > 600ms || cpu > 85%
                w.Header().Set("Retry-After", "30")
                http.Error(w, "shedding load", http.StatusServiceUnavailable)
                return
            }
            select {
            case sem <- struct{}{}:
                defer func() { <-sem }()
                next.ServeHTTP(w, r)
            case <-time.After(50 * time.Millisecond):
                http.Error(w, "overloaded", http.StatusTooManyRequests)
            }
        })
    }
}
// shouldShed: critical -> always false; sheddable-batch first, then
// sheddable-search, then standard reads. p95 from prometheus histogram,
// cpu from /proc or gopsutil, sampled every 2s.
```

## FastAPI snippet

```python
# shedder.py — reference implementation sketch; copy into services and adapt.
import asyncio, time
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.responses import JSONResponse

SEM = asyncio.Semaphore(4 * 2)  # 4 x vCPU (2 vCPU pods per SPEC B)
SHED_ORDER = ["sheddable-batch", "sheddable-search", "standard"]

class ShedMiddleware(BaseHTTPMiddleware):
    async def dispatch(self, request, call_next):
        lc = request.headers.get("x-load-class", "standard")
        if lc != "critical" and should_shed(lc):  # p95 > 600ms or cpu > 85%
            return JSONResponse({"detail": "shedding load"}, 503,
                                headers={"Retry-After": "30"})
        try:
            await asyncio.wait_for(SEM.acquire(), timeout=0.05)
        except asyncio.TimeoutError:
            return JSONResponse({"detail": "overloaded"}, 429)
        try:
            return await call_next(request)
        finally:
            SEM.release()
```

## Kafka consumers (SPEC B section 4)

- Pause partitions when downstream p99 > SLO: `consumer.pause(assigned)`;
  resume when p99 < 80% of SLO.
- DLQ after 5 retries with backoff 1s → 5m (exponential), topic `<topic>.dlq`.

## Notes

- `Retry-After` is mandatory on 503s so USSD gateway + APISIX back off cleanly.
- Metrics: export `meridian_shed_total{class}` and `meridian_shed_semaphore_inuse`
  for the HPA `http_requests_per_second` pipeline and SLO dashboards.
