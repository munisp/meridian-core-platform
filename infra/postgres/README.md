# Postgres roles: migration vs runtime split

Meridian separates the identity that runs **DDL (migrations)** from the
identity that serves **runtime traffic**:

| Role          | Purpose                                   | Can run DDL? | Created by |
| ------------- | ----------------------------------------- | ------------ | ---------- |
| `svc_migrator` | Boot-time auto-migrate / schema migrations | yes (CREATE/USAGE on app schemas) | `migrations/0004_migrator_role.sql` |
| `svc_<service>` (e.g. `svc_consent`, `svc_audit_evidence`) | Runtime DML only | **no** — CREATE revoked by `0003_roles.sql` | `migrations/0003_roles.sql` |

## Why

The live-infrastructure gates showed that the `svc_*` runtime roles as
shipped by `0003_roles.sql` cannot run services' boot-time auto-migrate
(`packages/events/store` `OpenPg` DDL): `REVOKE ... CREATE` on the service
schemas makes `CREATE TABLE IF NOT EXISTS` fail with `permission denied for
schema`. Running migrations as the shared superuser would defeat the
least-privilege posture, so DDL gets its own single role.

## How services boot

1. **Migrate step** — if `DB_MIGRATE_USER` is set, the service first opens
   a short-lived connection as `DB_MIGRATE_USER` / `DB_MIGRATE_PASSWORD`
   (`svc_migrator`) and runs the idempotent DDL
   (`store.MigratePg` / `store.ResolveMigrateDatabaseURL`).
2. **Runtime** — the serving pool then opens as `DB_USER` / `DB_PASSWORD`
   (the per-service `svc_*` role) **without** running any DDL.

If `DB_MIGRATE_USER` is unset the legacy path runs the DDL as the runtime
user — this only works for the shared/superuser dev database and fails
closed against the 0003-hardened roles, which is intentional.

Tables created by `svc_migrator` are usable by the runtime roles because
`0004` installs `ALTER DEFAULT PRIVILEGES FOR ROLE svc_migrator` per schema
(`SELECT/INSERT/UPDATE/DELETE` for app schemas; `SELECT/INSERT` only for
`audit_evidence`, preserving the append-only WORM invariant).

## Provisioning

Passwords are set out-of-band, same as the runtime roles:

```sh
psql "$ADMIN_DATABASE_URL" -c \
  "ALTER ROLE svc_migrator PASSWORD :'pw'" -v pw="$DB_MIGRATE_PASSWORD"
```

Apply order: `init/001-schemas.sql`, `init/002-bronze-views.sql`,
`migrations/0003_roles.sql`, `migrations/0004_migrator_role.sql`.
Rollbacks live next to each migration (`*.rollback.sql`).

Environment template: see `.env.prod.template` (`DB_USER`, `DB_PASSWORD`,
`DB_MIGRATE_USER`, `DB_MIGRATE_PASSWORD`).
