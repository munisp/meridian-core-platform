# ledger

Double-entry ledger service. TigerBeetle semantics (ledger ids 100–700, codes
1–7, DEBITS_MUST_NOT_EXCEED_CREDITS) are preserved across profiles.

## Environment

| Var | Purpose | Default (dev) |
|---|---|---|
| TIGERBEETLE_ADDRESSES | comma list host:port; selects the real tigerbeetle-go client (`internal/tb/real.go`) | unset → in-memory ledger |
| DATABASE_URL | Postgres DSN for the store (pgx/v5) | unset → SQLite at DATA_DIR |
| AUTH_MODE | `dev` or `keycloak` | dev |
| KEYCLOAK_ISSUER / KEYCLOAK_AUDIENCE / KEYCLOAK_JWKS_URL | OIDC config when AUTH_MODE=keycloak | unset |
| TLS_CERT_FILE / TLS_KEY_FILE | optional service TLS | unset → plain HTTP |

## Prod profile

Set `TIGERBEETLE_ADDRESSES=tb0:3000` (and `DATABASE_URL` for durable storage).
Startup logs `profile=prod component=tb`; with no env it logs
`profile=dev component=tb` and uses the in-memory implementation.
