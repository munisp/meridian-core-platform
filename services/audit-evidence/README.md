# audit-evidence

WORM evidence store. Evidence URIs keep the `worm://minio/<bucket>/<key>`
scheme with sha256 sidecar metadata in both profiles.

## Environment

| Var | Purpose | Default (dev) |
|---|---|---|
| MINIO_ENDPOINT | MinIO/S3 endpoint host:port; selects the real minio-go/v7 client | unset → local WORM dir at DATA_DIR |
| MINIO_ACCESS_KEY / MINIO_SECRET_KEY | credentials | unset |
| MINIO_BUCKET | object-lock (WORM) bucket name | unset |
| MINIO_USE_SSL | `true`/`false` for TLS to MinIO | false |
| DATABASE_URL | Postgres DSN for the store (pgx/v5) | unset → SQLite at DATA_DIR |
| AUTH_MODE | `dev` or `keycloak` | dev |
| TLS_CERT_FILE / TLS_KEY_FILE | optional service TLS | unset → plain HTTP |

## Prod profile

Point `MINIO_*` at a bucket with object-lock enabled. Startup logs
`profile=prod component=minio`; unset vars fall back to the local WORM
directory (`profile=dev component=minio`).
