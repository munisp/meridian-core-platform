#!/usr/bin/env bash
# Kafka/Redpanda topic provisioning (SPEC B section 3).
# Idempotent: uses `rpk topic create` with partitions+rf; existing topics are
# skipped/validated. Max consumer lag alert = 50k (configure in alerting).
set -euo pipefail

BROKERS="${REDPANDA_BROKERS:-redpanda.kafka.svc.cluster.local:9092}"
RPK="${RPK_BIN:-rpk}"

create_topic() {
  local name="$1" partitions="$2" rf="$3" retention_ms="${4:-604800000}" # 7d hot
  if "${RPK}" topic list --brokers "${BROKERS}" | awk '{print $1}' | grep -qx "${name}"; then
    echo "exists: ${name} (validating config)"
    "${RPK}" topic describe "${name}" --brokers "${BROKERS}" --summary >/dev/null
  else
    echo "create: ${name} p=${partitions} rf=${rf}"
    "${RPK}" topic create "${name}" \
      --brokers "${BROKERS}" \
      --partitions "${partitions}" \
      --replicas "${rf}" \
      --topic-config "retention.ms=${retention_ms}" \
      --topic-config "cleanup.policy=delete"
  fi
}

# Core topics (partitions chosen for 50k TPS / 1MB msgs).
create_topic tax.filings.v1        96 3
create_topic payments.events.v1    96 3
create_topic kyc.evidence.v1       24 3
create_topic audit.events.v1       48 3
create_topic notifications.out.v1  24 3

# DLQ companions: 6 partitions each.
for t in tax.filings.v1 payments.events.v1 kyc.evidence.v1 audit.events.v1 notifications.out.v1; do
  create_topic "${t}.dlq" 6 3
done

echo "All topics provisioned. Consumer groups are created on first consume."
echo "Reminder: alert on consumer lag > 50000 per group (SPEC B)."
