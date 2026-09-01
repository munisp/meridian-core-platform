# Fluvio integration

- **Active**: nothing. The platform event bus is Redpanda (Kafka protocol).
- **Provisioned**: helm `fluvio` section in `infra/helm/values-*.yaml`
  (disabled by default) and topic CRD skeleton
  `infra/helm/templates/fluvio.yaml` (rendered only when
  `.Values.fluvio.enabled`). No compose service — Fluvio is k8s-only.
- **To activate**: install the Fluvio SC via its helm charts (`fluvio-sys`,
  then the cluster chart), then enable the topics here. No OTel exporter
  exists for Fluvio; broker metrics would need a future Prometheus endpoint,
  so Fluvio alerts stay tagged instrumentation-pending.
