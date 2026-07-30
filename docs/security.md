# Meridian Security Model (production profile)

Threat model summary: two trust zones — **Market** (core + compliance + inclusion) and
**Sovereign** (gov-enclave). The enclave-gateway is the sole north-south audited path
between zones. All east-west traffic inside a zone is TLS; all cross-zone traffic is
mutual TLS. External adapters (NIMC/PSSP/USSD) are HMAC-signed with circuit breakers;
no raw NIN/TIN/MSISDN is ever logged (tin_hash pseudonymisation, SPEC §1.3).

## TLS / mTLS matrix

| Hop | Protocol | Notes |
|---|---|---|
| Browser ↔ APISIX | TLS 1.2+ (public 9443) | APISIX terminates TLS; WAF enforcement point |
| APISIX ↔ services (Market zone) | TLS | dev CA via `infra/tls/gen-dev-certs.sh`; prod = org PKI |
| Service ↔ Postgres/Redis/OpenSearch/MinIO | TLS | OpenSearch security plugin ON; MinIO TLS + object-lock |
| Services ↔ Keycloak | HTTPS (KC_HOSTNAME https) | RS256 JWKS verification |
| **Market ↔ Sovereign (enclave-gateway)** | **mTLS mandatory** | dedicated `enclave-ca`; gateway validates client cert + OIDC token, stamps `X-Meridian-Caller` before forwarding; in prod profile plain JWT alone is rejected |
| Services ↔ TigerBeetle / Redpanda / Temporal | TCP (private net) | bind internal; firewall-only exposure |
| Trino ↔ Iceberg REST ↔ MinIO | HTTPS (S3 sig v4) | warehouse bucket separate from WORM evidence bucket |

## Secrets policy

- All secrets are env-injected from a vault/secrets manager (`.env.prod` is a
  template; the filled file is git-ignored and never committed).
- No inline passwords in compose — `${VAR}` references only.
- Keycloak client secrets per service (`KC_SECRET_*`); rotate independently.
- Dev material (`infra/tls/out/`, `admin@meridian.local/admin123`, dev JWT secret)
  is DEV-ONLY and flagged as such in the realm import.
- ed25519 rule-pack signing key (governance-board) held by the board ceremony;
  never present on runtime hosts.

## F9/F10 forbidden-flow enforcement points

Forbidden-by-construction (SPEC §5): there is no code path for F9 (enclave →
market direct data push) or F10 (unaudited cross-zone call).

1. **enclave-gateway** — routing table contains only registered cross-zone flow ids;
   flow ids are denied at the middleware layer before schema validation.
2. **Network** — sovereign-zone egress to the market zone is denied except via
   the gateway listener; compose networks isolate `meridian-prod` bridges.
3. **Audit** — every accepted cross-zone message produces a synchronous WORM
   evidence receipt (object-lock bucket) before any enclave consumer sees it;
   the forbidden-flow monitor (admin console page 9) must always read empty.
4. **Permify** — scope check per flow (e.g. four-party visibility on EOI) runs
   pre-dispatch in the gateway pipeline.

## Auth flows

- **Humans** (admin console, compliance portal, gov-console, PWAs): OIDC
  authorization code + PKCE (S256) against realm `meridian`; tokens held in
  memory (never localStorage), silent renew. Realm roles:
  admin/board/operator/auditor/practitioner/taxpayer/agent mapped to `roles` claim.
- **Service-to-service**: client-credentials, `aud=meridian-services`;
  consumers validate iss/exp/aud against Keycloak JWKS (5-min cache, refresh on
  unknown kid).
- **Cross-zone**: enclave-gateway requires BOTH mTLS client cert (enclave-ca)
  and a valid service token; it stamps `X-Meridian-Caller` for downstream audit.
- **Dev fallback** (`AUTH_MODE=dev`): HS256 + `X-Dev-Role`, zero config;
  never enable in prod — `AUTH_MODE=keycloak` is required for go-live.
