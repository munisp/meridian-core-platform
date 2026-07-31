# search-indexer

Outbox-driven search indexer. Index naming `nrs-{family}-v1`; the outbox
consumer loop is identical in both profiles.

## Environment

| Var | Purpose | Default (dev) |
|---|---|---|
| OPENSEARCH_URL | https://host:9200; selects the real OpenSearch bulk API client (opensearch-go v2) | unset → local JSON index at DATA_DIR |
| DATABASE_URL | Postgres DSN for the outbox store (pgx/v5) | unset → SQLite at DATA_DIR |
| KAFKA_BROKERS | comma list (Redpanda, franz-go) for bus consumption | unset → embedded bus |
| AUTH_MODE | `dev` or `keycloak` | dev |
| TLS_CERT_FILE / TLS_KEY_FILE | optional service TLS | unset → plain HTTP |

## Prod profile

Set `OPENSEARCH_URL` (plus `DATABASE_URL`/`KAFKA_BROKERS` for the shared
infra). Startup logs `profile=prod component=opensearch`; unset vars fall back
to the local JSON index (`profile=dev component=opensearch`).
