# OpenSearch integration

- **Active**: `opensearch` single-node container (compose + existing infra),
  search-indexer service consumes it; collector `opensearch` receiver scrapes
  cluster metrics (otel-collector-config.yaml).
- **Provisioned**: `opensearch-dashboards` service (compose always-on in the
  full stack; helm gated on `.Values.opensearchDashboards.enabled`).
- **OTel**: cluster metrics via the contrib `opensearch` receiver; there is
  no trace instrumentation of OpenSearch queries — search-indexer spans carry
  tenant.id once that service adopts otelx/meridian_py.otel.
