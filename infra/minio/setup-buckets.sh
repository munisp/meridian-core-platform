#!/usr/bin/env bash
# MinIO bucket + lifecycle setup (SPEC B section 3). Idempotent.
# EC 8+4, 12 drives/node x 4 nodes (configured at tenant level, not here).
# Buckets: kyc-raw (object-lock WORM 7y), filings-attachments, os-snapshots.
set -euo pipefail

MC="${MC_BIN:-mc}"
ALIAS="${MINIO_ALIAS:-meridian}"

mk_bucket() {
  local bucket="$1" lock="${2:-false}"
  if "${MC}" ls "${ALIAS}/${bucket}" >/dev/null 2>&1; then
    echo "exists: ${bucket}"
  else
    if [ "${lock}" = "true" ]; then
      # --with-lock can only be set at creation time.
      "${MC}" mb --with-lock "${ALIAS}/${bucket}"
    else
      "${MC}" mb "${ALIAS}/${bucket}"
    fi
    echo "created: ${bucket}"
  fi
}

mk_bucket kyc-raw true
mk_bucket filings-attachments false
mk_bucket os-snapshots false

# kyc-raw: WORM retention 7 years (2555 days), compliance mode.
"${MC}" retention set --default compliance 7y "${ALIAS}/kyc-raw"
"${MC}" version enable "${ALIAS}/kyc-raw"

# filings-attachments: versioned, expire noncurrent versions after 90d.
"${MC}" version enable "${ALIAS}/filings-attachments"
"${MC}" ilm rule add "${ALIAS}/filings-attachments" \
  --noncurrent-expire-days 90 2>/dev/null || echo "ilm rule already present"

# os-snapshots: expire snapshots after 8y (covers 7y audit retention + margin).
"${MC}" ilm rule add "${ALIAS}/os-snapshots" \
  --expire-days 2920 2>/dev/null || echo "ilm rule already present"

echo "MinIO buckets ready: kyc-raw (WORM 7y), filings-attachments, os-snapshots"
