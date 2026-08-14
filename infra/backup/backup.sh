#!/bin/sh
# backup.sh — pg_dump-based backup of all Meridian service databases
# (assurance F-12: previously NO database backup automation existed).
#
# Everything is env-configured; no hardcoded credentials:
#   PGHOST PGPORT                     — postgres endpoint (defaults postgres/5432)
#   BACKUP_POSTGRES_USER              — dump user (needs read on service schemas)
#   BACKUP_POSTGRES_PASSWORD          — exported as PGPASSWORD
#   BACKUP_DATABASES                  — space-separated DB list (default: meridian)
#   BACKUP_DIR                        — local dump dir (default /backups)
#   BACKUP_RETENTION_DAYS             — local retention (default 30)
#   BACKUP_S3_BUCKET                  — optional; if set and `mc` + MC_HOST_minio
#                                       alias exist, dumps are copied to
#                                       <alias>/<bucket>/<date>/<db>.dump
# Runs one dump cycle then exits; schedule via compose sidecar cron or the
# K8s CronJob in cronjob.yaml.
set -eu

PGHOST="${PGHOST:-postgres}"
PGPORT="${PGPORT:-5432}"
BACKUP_POSTGRES_USER="${BACKUP_POSTGRES_USER:?set BACKUP_POSTGRES_USER}"
export PGPASSWORD="${BACKUP_POSTGRES_PASSWORD:?set BACKUP_POSTGRES_PASSWORD}"
BACKUP_DATABASES="${BACKUP_DATABASES:-meridian}"
BACKUP_DIR="${BACKUP_DIR:-/backups}"
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"
BACKUP_S3_BUCKET="${BACKUP_S3_BUCKET:-}"

STAMP="$(date +%F)"
DEST="$BACKUP_DIR/$STAMP"
mkdir -p "$DEST"

echo "[backup] $(date -Iseconds) dumping databases: $BACKUP_DATABASES -> $DEST"
for db in $BACKUP_DATABASES; do
    out="$DEST/$db.dump"
    pg_dump -Fc -h "$PGHOST" -p "$PGPORT" -U "$BACKUP_POSTGRES_USER" -d "$db" -f "$out"
    echo "[backup] wrote $out ($(du -h "$out" | cut -f1))"
done

if [ -n "$BACKUP_S3_BUCKET" ] && command -v mc >/dev/null 2>&1; then
    for db in $BACKUP_DATABASES; do
        mc cp "$DEST/$db.dump" "minio/$BACKUP_S3_BUCKET/$STAMP/$db.dump"
        echo "[backup] shipped $db.dump to minio/$BACKUP_S3_BUCKET/$STAMP/"
    done
    # MinIO note: enable versioning + retention on the backup bucket
    # (see infra/minio/setup-buckets.sh pattern: `mc version enable` +
    # compliance-mode retention) so a compromised source cannot overwrite or
    # delete the only copy. MinIO versioning is deletion/overwrite protection,
    # NOT an offsite backup — mirror to DR via
    # `mc mirror --preserve minio/$BACKUP_S3_BUCKET minio-dr/$BACKUP_S3_BUCKET`.
else
    echo "[backup] BACKUP_S3_BUCKET unset or mc missing; local copy only at $DEST"
fi

# TigerBeetle note: financial state lives in the TigerBeetle cluster, not
# Postgres. Ensure the replica topology is recorded and at least 2 replicas
# run (see infra/README.md TigerBeetleReplicaRestarting alert); TB recovery
# is replica re-election, not pg_restore. Record topology via
# `tigerbeetle inspect` during each rehearsal (docs/restore-runbook.md).

echo "[backup] pruning local dumps older than $BACKUP_RETENTION_DAYS days"
find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -mtime "+$BACKUP_RETENTION_DAYS" -exec rm -rf {} +

echo "[backup] $(date -Iseconds) cycle complete"
