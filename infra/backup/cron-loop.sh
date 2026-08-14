#!/bin/sh
# cron-loop.sh — tiny scheduler for the compose backup sidecar (F-12).
# Runs backup.sh daily at BACKUP_HOUR_UTC (default 3). For precise cron
# expressions use the K8s CronJob (cronjob.yaml) instead.
set -eu
BACKUP_HOUR_UTC="${BACKUP_HOUR_UTC:-3}"
DIR="$(dirname "$0")"
while true; do
    now_h="$(date -u +%H)"
    if [ "$now_h" -eq "$BACKUP_HOUR_UTC" ] && [ ! -f "/backups/.done-$(date -u +%F)" ]; then
        sh "$DIR/backup.sh" && touch "/backups/.done-$(date -u +%F)"
    fi
    sleep 600
done
