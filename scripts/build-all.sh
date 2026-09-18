#!/bin/sh
# Build + vet + test every Go module in the workspace.
# Module list mirrors go.work `use` (single source of truth) — R4 (S2 finding
# #10): previously omitted services/admin-api, services/migration,
# packages/keyx, packages/events/otelx and sdk/go.
set -e
if ! command -v go >/dev/null 2>&1; then
  export PATH=$HOME/sdk/go/bin:$PATH
fi
cd "$(dirname "$0")/.."
MODULES="packages/events packages/events/otelx packages/keyx packages/rulepack-schema packages/permify-models packages/temporal-sdkx packages/schemas/go sdk/go workflows-go services/admin-api services/migration services/rp-registry services/tin-graph services/ledger services/notification services/audit-evidence services/geo services/consent services/search-indexer services/edge-policy"
for m in $MODULES; do
  echo "== $m"
  (cd "$m" && gofmt -l . | grep -v '^$' && exit 1 || true)
  (cd "$m" && go vet ./... && go build ./... && go test ./...)
done
echo "ALL GO MODULES OK"
