// Package auth provides HS256 (dev) and Keycloak RS256/JWKS (prod)
// verification with shared Claims and role checks for both Go and Python
// callers via equivalent logic (see python/meridian_events/auth.py).
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/httpx"
)

// Claims is the normalised identity attached to every authenticated call.
type Claims struct {
	Sub        string   `json:"sub"`
	TenantID   string   `json:"tenant_id,omitempty"`
	Roles      []string `json:"roles,omitempty"`
	TINHash    string   `json:"tin_hash,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
	IssuedAt   int64    `json:"iat,omitempty"`
	ExpiresAt  int64    `json:"exp,omitempty"`
	Raw        map[string]any `json:"-"`
}

// HasRole reports whether the claims include a role.
func (c Claims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// HasAnyRole reports whether any of the roles are present.
func (c Claims) HasAnyRole(roles ...string) bool {
	for _, want := range roles {
		if c.HasRole(want) {
			return true
		}
	}
	return false
}

type ctxKey struct{}

// FromContext extracts claims (nil if unauthenticated).
func FromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(*Claims)
	return c, ok && c != nil
}

// WithClaims attaches claims.
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// --------------------------------------------------------------------------
// HS256 (dev profile)
// --------------------------------------------------------------------------

// DevHS256Secret returns the dev-mode HS256 secret (never used in prod profile).
func DevHS256Secret() []byte {
	return []byte(httpx.Env("MERIDIAN_DEV_JWT_SECRET", "meridian-dev-secret-change-me"))
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// SignHS256 issues a compact JWS for dev/test flows.
func SignHS256(claims map[string]any, secret []byte) string {
	hdr := b64url([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	pl := b64url(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(hdr + "." + pl))
	return hdr + "." + pl + "." + b64url(mac.Sum(nil))
}

// VerifyHS256 verifies a compact JWS and returns claims.
func VerifyHS256(token string, secret []byte) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errString("malformed token")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(mac.Sum(nil), sig) {
		return nil, errString("signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errString("bad payload")
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, errString("bad claims")
	}
	c := mapClaims(raw)
	if c.ExpiresAt != 0 && time.Now().Unix() > c.ExpiresAt {
		return nil, errString("token expired")
	}
	return c, nil
}

func mapClaims(raw map[string]any) *Claims {
	c := &Claims{Raw: raw}
	c.Sub, _ = raw["sub"].(string)
	c.TenantID, _ = raw["tenant_id"].(string)
	c.TINHash, _ = raw["tin_hash"].(string)
	c.IssuedAt = numFrom(raw["iat"])
	c.ExpiresAt = numFrom(raw["exp"])
	c.Roles = stringsFrom(raw["roles"])
	if len(c.Roles) == 0 {
		// keycloak realm_access.roles
		if ra, ok := raw["realm_access"].(map[string]any); ok {
			c.Roles = stringsFrom(ra["roles"])
		}
	}
	c.Scopes = stringsFrom(raw["scopes"])
	if sc, ok := raw["scope"].(string); ok && len(c.Scopes) == 0 {
		c.Scopes = strings.Fields(sc)
	}
	return c
}

func numFrom(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

func stringsFrom(v any) []string {
	switch xs := v.(type) {
	case []string:
		return xs
	case []any:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

// --------------------------------------------------------------------------
// Middleware (HS256 + X-Dev-Role in dev profile; Keycloak RS256 in prod profile)
// --------------------------------------------------------------------------

// Middleware validates requests per AUTH_MODE (dev|keycloak). In dev,
// X-Dev-Role alone is accepted as claims{sub:"dev", roles:[role]}.
func Middleware(next http.Handler) http.Handler {
	mode := httpx.Env("AUTH_MODE", "dev")
	if mode == "keycloak" {
		if _, err := SharedKeycloakVerifier(); err != nil {
			// FAIL CLOSED (audit: prod selector set but auth fell back to
			// HS256 + X-Dev-Role => full bypass). Deny every request rather
			// than honouring dev credentials in prod mode.
			httpx.ProfileLog("auth", "prod", "AUTH_MODE=keycloak but verifier misconfigured (%v); FAILING CLOSED — all requests denied until Keycloak is configured", err)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				httpx.Errorf(w, http.StatusServiceUnavailable, "auth misconfigured",
					"AUTH_MODE=keycloak but the Keycloak verifier is not configured; refusing all requests (fail closed)")
			})
		}
		httpx.ProfileLog("auth", "prod", "Keycloak RS256/JWKS verification active")
		return http.HandlerFunc(KeycloakMiddleware(next))
	}
	if mode != "dev" {
		httpx.ProfileLog("auth", "prod", "unknown AUTH_MODE=%q; FAILING CLOSED", mode)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpx.Errorf(w, http.StatusServiceUnavailable, "auth misconfigured",
				"unsupported AUTH_MODE %q; refusing all requests (fail closed)", mode)
		})
	}
	httpx.ProfileLog("auth", "dev", "HS256 + X-Dev-Role accepted")
	secret := DevHS256Secret()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var claims *Claims
		switch mode {
		case "dev":
			if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
				if c, err := VerifyHS256(strings.TrimPrefix(ah, "Bearer "), secret); err == nil {
					claims = c
				}
			}
			if claims == nil {
				if role := r.Header.Get("X-Dev-Role"); role != "" {
					claims = &Claims{Sub: "dev", Roles: []string{role}}
				}
			}
		}
		if claims == nil {
			httpx.Problem(w, http.StatusUnauthorized, "unauthenticated", "missing or invalid credentials")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
	})
}

// RequireRoles wraps a handler with a role gate.
func RequireRoles(next http.Handler, roles ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := FromContext(r.Context())
		if !ok || !c.HasAnyRole(roles...) {
			httpx.Problem(w, http.StatusForbidden, "forbidden", "insufficient role")
			return
		}
		next.ServeHTTP(w, r)
	})
}
