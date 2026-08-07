# Meridian infra — health probes & remediation (B6/D3)

This document covers the health-check wiring added for TigerBeetle and
Temporal and the operator remediation flow when a probe fails.

## Components and probes

| Component | Compose | Helm (k8s) | Why this probe |
|---|---|---|---|
| `tigerbeetle` | `tigerbeetle-sidecar` (busybox) runs `nc -z tigerbeetle 3000`; the TB image is a single static binary with **no shell/nc**, so an in-container healthcheck cannot execute — the TCP port check lives in the sidecar, whose own healthcheck mirrors replica health | `deployment-tigerbeetle.yaml`: `readinessProbe` + `livenessProbe` **tcpSocket :3000** | TCP accept on 3000 is only true once the replica has opened its data file and is serving the TB protocol |
| `temporal` / `temporal-worker` | `tctl --address temporal:7233 cluster health \| grep -q SERVING` (tctl ships in `temporalio/auto-setup`) with 60s `start_period` for schema setup | `deployment-temporal-worker.yaml`: readiness **exec `tctl cluster health`**, liveness **tcpSocket :7233** | `SERVING` confirms the frontend role is answering; tcpSocket liveness catches wedged-but-listening vs dead process |

Helm note: the chart previously shipped only HPA/PDB/KEDA templates — the two
Deployment templates above are the minimal workload manifests for these
components, with baseline pod securityContexts (runAsNonRoot,
readOnlyRootFilesystem, drop ALL caps, RuntimeDefault seccomp).

## Remediation

- **Restart policy**: every compose service runs `restart: unless-stopped`.
  The `tigerbeetle-sidecar` command loops while the TCP check succeeds and
  exits 1 on failure, so Docker restarts it; its healthcheck flips to
  `unhealthy`, which is the signal to alert on. In k8s, the kubelet restarts
  containers failing liveness; readiness gates traffic.
- **Recovery expectations** (see `docs/funds-flow.md`): after a TigerBeetle
  restart, pending-transfer expiry sweepers resume (boot sweep + 60s
  interval); deterministic transfer IDs make replays idempotent. Temporal
  workflow tasks re-dispatch to workers automatically once the frontend is
  SERVING again; activity retries follow the sdkx retry policy.
- **Alert names** (wire these in the Prometheus stack when it lands, wave D4):
  - `TigerBeetleDown` — sidecar healthcheck unhealthy (compose) or
    `kube_deployment_status_replicas_available{deployment="tigerbeetle"} == 0`
    for > 2m.
  - `TemporalClusterUnhealthy` — temporal healthcheck failing /
    `kube_pod_container_status_ready{pod=~"temporal-worker-.*"} == 0` > 2m.
  - `TigerBeetleReplicaRestarting` — restart count increase > 3 in 10m
    (crash-loop early warning).

## Verify locally

```sh
docker compose -f infra/docker-compose.prod.yml config -q   # validate
docker compose -f infra/docker-compose.prod.yml up -d tigerbeetle tigerbeetle-sidecar temporal
docker inspect --format '{{.State.Health.Status}}' meridian-tigerbeetle-sidecar-1
```
