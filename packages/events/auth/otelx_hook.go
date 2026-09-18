package auth

// otelx_hook.go — registers the platform's VERIFIED token verifiers as the
// otelx tenant-attribution source (R4, S3 finding #7: tenant.id was
// spoofable via inbound headers/baggage/unverified JWT decode). otelx
// cannot import auth (auth -> httpx -> otelx cycle), so the dependency is
// inverted: auth registers the verifier hook here.
//
// The hook uses the SAME verifiers as Middleware: HS256 dev secret in
// AUTH_MODE=dev, RS256 Keycloak JWKS in AUTH_MODE=keycloak. Any failure
// yields "" — the span simply carries no tenant.id.

import (
	"strings"

	"github.com/munisp/meridian-core-platform/packages/events/httpx"
	"github.com/munisp/meridian-core-platform/packages/events/otelx"
)

func init() {
	otelx.TenantVerifier = verifiedTenantClaim
}

func verifiedTenantClaim(authz string) string {
	if !strings.HasPrefix(authz, "Bearer ") {
		return ""
	}
	tok := strings.TrimPrefix(authz, "Bearer ")
	if httpx.Env("AUTH_MODE", "dev") == "keycloak" {
		v, err := SharedKeycloakVerifier()
		if err != nil {
			return ""
		}
		claims, err := v.VerifyRS256(tok)
		if err != nil {
			return ""
		}
		return claims.TenantID
	}
	claims, err := VerifyHS256(tok)
	if err != nil {
		return ""
	}
	return claims.TenantID
}
