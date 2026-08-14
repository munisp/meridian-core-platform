# Restore Runbook — Meridian data layer (F-12 / R-2)

Quarterly restore rehearsal. All commands are env-configured; no credentials
are stored in this repo. Tooling assumed: `kubectl`, `psql`, `pg_restore`,
`mc`, and the backup automation in `infra/backup/` (compose `pg-backup`
sidecar or the K8s CronJob `infra/backup/cronjob.yaml`).

Targets: **RPO ≤ 15 min** (dumps daily + MinIO versioning on source buckets),
**RTO ≤ 4 h**. File each rehearsal report as evidence
(`POST /v1/evidence` on the audit-evidence service).

## Pre-conditions

- Maintenance window agreed.
- The backup sidecar/CronJob has completed at least one cycle and dumps are
  visible: `mc ls minio/meridian-pg-backups/<date>/`.
- MinIO backup bucket has versioning + retention enabled:
  `mc version enable minio/meridian-pg-backups` (mirrors the WORM pattern in
  `infra/minio/setup-buckets.sh`). Nightly DR mirror:
  `mc mirror --preserve minio/meridian-pg-backups minio-dr/meridian-pg-backups`.
- TigerBeetle replica topology recorded (`tigerbeetle inspect`; at least 2
  replicas — TB recovery is replica re-election, not a Postgres restore).

## Rehearse (from w4 R-2)

1. **Snapshot current state**
   - `psql "$DATABASE_URL" -c 'select count(*) from <key tables>'` per DB
     (core / compliance / kyc).
   - `mc ls --recursive --versions minio/kyc-raw | wc -l`.
   - TigerBeetle account/transfer counts via ledger `/v1/accounts` stats.
2. **Simulate loss** (rehearsed namespace only)
   - `psql -c 'drop schema public cascade'` on one Postgres database.
   - Delete one versioned MinIO object.
3. **Restore Postgres**
   - `pg_restore -Fc --clean --if-exists -d "$DATABASE_URL" \
      /backups/<date>/<db>.dump`
   - (If WAL-G PITR is later adopted: `backup-fetch` + `restore_command`
     replay to the timestamp before step 2.)
4. **Restore the object**
   - `mc cp minio/kyc-raw/<object>?versionId=<id> .` or from the DR mirror.
5. **TigerBeetle**
   - `kubectl delete pod tigerbeetle-0`; verify replica re-election.
   - Confirm the pending-transfer sweeper resumes
     (`infra/README.md:25-28`) and transfer IDs remain idempotent on replay.
6. **Verify**
   - Re-run step-1 counts; the diff MUST be empty.
   - Settlement smoke: `GET /v1/recon/breaks`.
   - Services boot WITHOUT the embedded fallback — grep pod logs for
     `profile=prod component=store postgres`.
7. **Record**
   - Time-to-restore vs RTO (≤ 4 h); RPO check against the dump cadence.
   - File the rehearsal report via audit-evidence `POST /v1/evidence`.

Cadence: **quarterly**. Automate steps 1 and 6 as a CI smoke job.

## Roles reminder

Postgres restores run as the superuser/backup operator; application services
use the least-privilege `svc_*` roles from
`infra/postgres/migrations/0003_roles.sql`. Audit evidence tables are
append-only (INSERT/SELECT) even for the audit role — a restored dump does
not change that posture.
