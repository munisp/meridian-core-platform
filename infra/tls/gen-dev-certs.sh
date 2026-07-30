#!/usr/bin/env bash
# gen-dev-certs.sh — dev CA + per-service TLS material for Meridian (H5).
# DEV ONLY. Production uses the organisation PKI / ACME; never commit real keys.
# Generates:
#   out/ca.key, out/ca.crt                  — Meridian dev root CA
#   out/enclave-ca.key, out/enclave-ca.crt  — sovereign-zone mTLS CA (enclave-gateway)
#   out/<svc>.key / <svc>.crt               — per-service certs (SAN: <svc>, <svc>.meridian.local, localhost)
set -euo pipefail
OUT="$(cd "$(dirname "$0")" && pwd)/out"
DAYS=825
mkdir -p "$OUT"

SERVICES="apisix keycloak postgres redis opensearch minio temporal redpanda permify trino iceberg-rest enclave-gateway rp-registry tin-graph ledger einvoicing onboarding"

gen_ca() { # $1=name
  openssl genrsa -out "$OUT/$1.key" 4096
  openssl req -x509 -new -nodes -key "$OUT/$1.key" -sha256 -days 1825 \
    -subj "/C=NG/O=Meridian Dev/CN=Meridian $1" -out "$OUT/$1.crt"
}

gen_cert() { # $1=service $2=ca-name
  local svc="$1" ca="$2"
  openssl genrsa -out "$OUT/$svc.key" 2048
  cat > "$OUT/$svc.cnf" <<EOF
[req]
distinguished_name = dn
req_extensions = ext
[dn]
[ext]
subjectAltName = DNS:$svc,DNS:$svc.meridian.local,DNS:localhost,IP:127.0.0.1
extendedKeyUsage = serverAuth,clientAuth
EOF
  openssl req -new -key "$OUT/$svc.key" -out "$OUT/$svc.csr" \
    -subj "/C=NG/O=Meridian Dev/CN=$svc" -config "$OUT/$svc.cnf"
  openssl x509 -req -in "$OUT/$svc.csr" -CA "$OUT/$ca.crt" -CAkey "$OUT/$ca.key" \
    -CAcreateserial -days $DAYS -sha256 -extensions ext -extfile "$OUT/$svc.cnf" \
    -out "$OUT/$svc.crt"
  rm -f "$OUT/$svc.csr" "$OUT/$svc.cnf"
  # combined chain for clients that want cert+ca
  cat "$OUT/$svc.crt" "$OUT/$ca.crt" > "$OUT/$svc-chain.pem"
}

echo ">> dev CA"
gen_ca ca
echo ">> enclave-gateway mTLS CA (sovereign zone)"
gen_ca enclave-ca

for s in $SERVICES; do
  echo ">> cert: $s"
  gen_cert "$s" ca
done
# enclave-gateway additionally gets a client cert from the enclave mTLS CA
gen_cert enclave-gateway-mtls enclave-ca
mv "$OUT/enclave-gateway-mtls.crt" "$OUT/enclave-gateway-client.crt"
mv "$OUT/enclave-gateway-mtls.key" "$OUT/enclave-gateway-client.key"
mv "$OUT/enclave-gateway-mtls-chain.pem" "$OUT/enclave-gateway-client-chain.pem"

# OpenSearch/MinIO/Keycloak convention aliases
cp "$OUT/opensearch.crt" "$OUT/opensearch.pem" 2>/dev/null || true
cp "$OUT/minio.crt" "$OUT/public.crt"; cp "$OUT/minio.key" "$OUT/private.key"

chmod 600 "$OUT"/*.key
echo "Done. Certs in $OUT (dev only — do NOT commit; out/ is git-ignored)."
