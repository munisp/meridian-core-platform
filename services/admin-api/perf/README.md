# perf/ — local load smoke harness

`harness/main.go` is a dependency-free HTTP load smoke tool used for the
Meridian performance baseline. Build and run:

```sh
go build -o /tmp/loadsmoke ./services/admin-api/perf/harness
/tmp/loadsmoke -name admin-api-healthz -url http://127.0.0.1:8095/healthz \
  -duration 60s -concurrency 50 -p95-budget-ms 200
```

Flags: `-method`, `-header "K: V"`, `-warmup` (default 3s). The gate fails
(exit 1) when p95 exceeds `-p95-budget-ms`, any 5xx is seen, or any network
error occurs. Method, measured results, budgets, and the staging k6 path:
see `docs/perf-baseline.md`.
