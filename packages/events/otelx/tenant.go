package otelx

// tenant.go — tenant attribute extraction. Canonical span attribute is
// tenant.id (DESIGN-CONTRACT.md).
//
// R4 hardening (S3 finding #7): tenant.id is stamped ONLY from a VERIFIED
// bearer token (HS256 dev secret / RS256 Keycloak JWKS — the same verifiers
// the auth middleware uses, registered via TenantVerifier). Inbound
// tenant-asserting inputs are attacker controlled and are NEVER trusted:
//   - X-Meridian-Tenant / X-Tenant-ID request headers
//   - the unverified JWT payload decode that used to live here
//   - inbound baggage tenant.id (stripped at the server edge, see
//     StripTenantAssertions)
//
// Before this fix any remote caller could spoof tenant.id, which the
// collector copies to a resource attribute and
// resource_to_telemetry_conversion turns into the tenant_id metric label:
// tenant metric pollution plus unbounded-label-cardinality DoS.

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"
)

// TenantKey is the canonical attribute/baggage key.
const TenantKey = "tenant.id"

// TenantHeaderNames are the inbound headers that assert a tenant identity.
// They are stripped from inbound requests at the server edge; services that
// need a tenant must re-stamp it from verified claims downstream.
var TenantHeaderNames = []string{"X-Meridian-Tenant", "X-Tenant-ID"}

// TenantAttr builds the tenant.id attribute (empty values are dropped by
// callers; attribute.Value rejects nothing but we keep spans clean).
func TenantAttr(tenant string) attribute.KeyValue {
	return attribute.String(TenantKey, tenant)
}

// StripTenantAssertions removes every caller-controlled tenant assertion
// from an inbound request: the tenant headers and any inbound baggage
// tenant.id member. Call at the server edge (the otelx Middleware does this
// before extracting trace context) so nothing downstream can reflect a
// spoofed tenant into spans, baggage or metric labels.
func StripTenantAssertions(r *http.Request) {
	for _, h := range TenantHeaderNames {
		r.Header.Del(h)
	}
	if r.Header.Get("Baggage") != "" {
		r.Header.Del("Baggage") // inbound baggage is not tenant-trustable; the middleware re-stamps
	}
}

// TenantVerifier verifies a bearer token and returns its tenant_id claim.
// It is registered by the auth package (auth registers the platform's real
// HS256/Keycloak verifiers in init(); otelx cannot import auth directly —
// auth -> httpx -> otelx would be an import cycle). Nil verifier = no
// tenant attribution (fail closed), never a forged label.
var TenantVerifier func(authorizationHeader string) string

// TenantFromRequest resolves the tenant for an inbound request from a
// VERIFIED bearer token only. Returns "" when no token verifies — spans
// simply carry no tenant.id rather than a forged one.
func TenantFromRequest(r *http.Request) string {
	if TenantVerifier == nil {
		return ""
	}
	return TenantVerifier(r.Header.Get("Authorization"))
}
