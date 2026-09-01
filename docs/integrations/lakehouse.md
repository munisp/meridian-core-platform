# Lakehouse OTel wiring

- **Active**: `ml/` lakehouse pipelines (`ml/data/lakehouse.py`,
  `ml/pipelines/lakehouse_sink.py`) and the Trino infra dir exist and run
  without telemetry.
- **Provisioned**: helm value `lakehouse.otelEndpoint` — when set it is
  injected as `OTEL_EXPORTER_OTLP_ENDPOINT` into pipeline jobs; the Python
  bootstrap follows `meridian_py.otel.init_otel` conventions
  (DESIGN-CONTRACT.md). Collector-side, Trino/lakehouse metrics arrive via
  Prometheus scrape (no dedicated contrib receiver for Trino is wired here).
- **Not done**: auto-instrumenting the ml pipelines themselves — that is the
  per-repo agent's job against DESIGN-CONTRACT.md.
