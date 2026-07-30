# Package auth implements the Meridian auth conventions (SPEC 1.3):
// Bearer JWT (HS256 dev secret MERIDIAN_DEV_JWT_SECRET, claims sub/roles/
// tenant_id); AUTH_MODE=dev also accepts X-Dev-Role: admin|operator|auditor.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/httpx"
)

type ctxKey struct{}

// Claims are the authenticated principal's attributes.
type Claims struct {
	Sub      string   `json:"sub"`
	Roles    []string `json:"roles"`
	TenantID string   `json:"tenant_id"`
	Exp      int64    `json:"exp,omitempty"`
	Iat      int64    `json:"iat,omitempty"`
}

// HasRole reports whether the principal holds a role.
func (c Claims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// FromContext extracts claims placed by Middleware.
func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(Claims)
	return c, ok
}

func secret() string {
	return httpx.Env("MERIDIAN_DEV_JWT_SECRET", "meridian-dev-secret-change-me")
}

// SignHS256 issues a compact HS256 JWT for the given claims (dev issuer).
func SignHS256(c Claims, ttl time.Duration) (string, error) {
	now := time.Now()
	c.Iat = now.Unix()
	if c.Exp == 0 {
		c.Exp = now.Add(ttl).Unix()
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret()))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyHS256 validates a compact HS256 JWT and returns its claims.
func VerifyHS256(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("malformed token")
	}
	body := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret()))
	mac.Write([]byte(body))
	expected := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, errors.New("bad signature encoding")
	}
	if !hmac.Equal(expected, got) {
		return Claims{}, errors.New("signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, errors.New("bad payload encoding")
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, fmt.Errorf("bad claims: %w", err)
	}
	if c.Exp != 0 && time.Now().Unix() > c.Exp {
		return Claims{}, errors.New("token expired")
	}
	return c, nil
}

var devRoles = map[string]bool{"admin": true, "operator": true, "auditor": true, "board": true}

// Middleware enforces SPEC 1.3 auth. In AUTH_MODE=dev (default) the
// X-Dev-Role header is accepted as a principal; Bearer JWT always works.
// Requests without credentials are rejected with 401 except on /healthz
// and /readyz which should be registered before wrapping.
func Middleware(next http.Handler) http.Handler {
	mode := httpx.Env("AUTH_MODE", "dev")
	if mode == "keycloak" {
		if _, err := SharedKeycloakVerifier(); err != nil {
			httpx.ProfileLog("auth", "dev", "AUTH_MODE=keycloak but verifier misconfigured (%v); dev fallback active", err)
		} else {
			httpx.ProfileLog("auth", "prod", "Keycloak RS256/JWKS verification active")
			return http.HandlerFunc(KeycloakMiddleware(next))
		}
	} else {
		httpx.ProfileLog("auth", "dev", "HS256 + X-Dev-Role accepted")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var claims Claims
		authz := r.Header.Get("Authorization")
		switch {
		case strings.HasPrefix(authz, "Bearer "):
			c, err := VerifyHS256(strings.TrimPrefix(authz, "Bearer "))
			if err != nil {
				httpx.Errorf(w, http.StatusUnauthorized, "unauthorized", "invalid bearer token: %v", err)
				return
			}
			claims = c
		case mode == "dev" && devRoles[r.Header.Get("X-Dev-Role")]:
			role := r.Header.Get("X-Dev-Role")
			claims = Claims{
				Sub:      "dev-" + role,
				Roles:    []string{role},
				TenantID: r.Header.Get("X-Tenant-ID"),
			}
		default:
			httpx.Errorf(w, http.StatusUnauthorized, "unauthorized", "Bearer JWT or X-Dev-Role required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, claims)))
	})
}

// RequireRole wraps a handler, enforcing that the caller has the role.
func RequireRole(role string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := FromContext(r.Context())
		if !ok || !c.HasRole(role) {
			httpx.Errorf(w, http.StatusForbidden, "forbidden", "role %q required", role)
			return
		}
		next(w, r)
	}
}
