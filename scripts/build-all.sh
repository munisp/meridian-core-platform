#!/bin/sh
# Build + vet + test every Go module in the workspace.
set -e
export PATH=$HOME/sdk/go/bin:$PATH
cd "$(dirname "$0")/.."
MODULES="packages/events packages/rulepack-schema packages/permify-models packages/temporal-sdkx packages/schemas/go workflows-go services/rp-registry services/tin-graph services/ledger services/notification services/audit-evidence services/geo services/consent services/search-indexer services/edge-policy"
for m in $MODULES; do
  echo "== $m"
  (cd "$m" && gofmt -l . | grep -v '^$' && exit 1 || true)
  (cd "$m" && go vet ./... && go build ./... && go test ./...)
done
echo "ALL GO MODULES OK"
