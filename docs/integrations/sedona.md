# Apache Sedona integration

- **Active**: nothing. Geo workloads today are `geo-rs` (embedded polygons)
  and `services/geo` (PostGIS).
- **Provisioned**: Spark/Sedona runtime profile for batch geo jobs:
  `infra/sedona/spark-sedona.yml` (compose profile `geo-batch`, image
  `apache/sedona` spark runtime) intended for large spatial joins the OLTP
  engines should not run.
- **To activate**: `docker compose --profile geo-batch up sedona-spark`, or
  point an external Spark cluster at the same jars. OTel: Spark driver/executor
  OTel agent wiring is documented in the profile (env, off by default).
