// Package auth implements the Meridian auth conventions (SPEC 1.3):
// Bearer JWT (HS256 dev secret MERIDIAN_DEV_JWT_SECRET, claims sub/roles/
// tenant_id); AUTH_MODE=dev also accepts X-Dev-Role: admin|operator|auditor.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
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

// devSecretDefault is the well-known insecure dev JWT secret (A1-10).
const devSecretDefault = "meridian-dev-secret-change-me"

func secret() string {
	return httpx.Env("MERIDIAN_DEV_JWT_SECRET", devSecretDefault)
}

// ProdMisconfigured reports whether the process runs with PROFILE=prod but
// forgeable dev auth: AUTH_MODE=dev (X-Dev-Role honoured) or the
// default/missing MERIDIAN_DEV_JWT_SECRET (A1-10). Middleware fails closed
// in this state rather than serving forgeable auth.
func ProdMisconfigured() bool {
	if httpx.Env("PROFILE", "dev") != "prod" {
		return false
	}
	if httpx.Env("AUTH_MODE", "dev") == "dev" {
		return true
	}
	s := secret()
	return s == "" || s == devSecretDefault
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

var devRoles = map[string]bool{"admin": true, "operator": true, "auditor": true, "board": true, "privacy:officer": true}

// Middleware enforces SPEC 1.3 auth. In AUTH_MODE=dev (default) the
// X-Dev-Role header is accepted as a principal; Bearer JWT always works.
// Requests without credentials are rejected with 401 except on /healthz
// and /readyz which should be registered before wrapping.
func Middleware(next http.Handler) http.Handler {
	mode := httpx.Env("AUTH_MODE", "dev")
	// A1-10: PROFILE=prod with dev-mode auth or the default/missing dev
	// secret must never serve — both are fully forgeable. Fail closed.
	if ProdMisconfigured() {
		httpx.ProfileLog("auth", "prod", "PROFILE=prod with AUTH_MODE=dev or default/missing MERIDIAN_DEV_JWT_SECRET; FAILING CLOSED — all requests denied")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpx.Errorf(w, http.StatusServiceUnavailable, "auth misconfigured",
				"PROFILE=prod refuses dev auth / default JWT secret; refusing all requests (fail closed)")
		})
	}
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var claims Claims
		authz := r.Header.Get("Authorization")
		if sc, ok := ServiceClaims(r); ok {
			// B2-#12 repair: service-to-service caller authenticated by a
			// DISTINCT env-injected maker or settle token (constant-time
			// compare). No single token carries both ledger roles.
			claims = sc
		} else {
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

// ---- B2-#12 repair: DISTINCT service-to-service tokens ----------------
// Money services (settlement, pos-vat, ombud, admin console proxy,
// inclusion PSM/NIP) call the core ledger from server-side jobs with no
// end-user JWT. They authenticate with env-injected service tokens sent as
// X-Service-Token. The maker/checker split ALSO constrains the machine
// path: the maker token grants ONLY "ledger:post" (pending-create) and the
// settle token grants ONLY "ledger:settle" (post/void). No token carries
// both — a single stolen service token can no longer run the full
// hold->settle saga (V2 round: the previous shared token granted both
// roles, bypassing maker/checker SoD for the machine path).
//
// Tokens are never honoured when unconfigured (fail closed). The forgeable
// X-Dev-Role header remains dev-only.

// ServiceMakerTokenEnvNames are consulted for the maker service token
// (first non-empty wins). Grants ledger:post only.
var ServiceMakerTokenEnvNames = []string{"MERIDIAN_LEDGER_MAKER_TOKEN", "LEDGER_MAKER_TOKEN"}

// ServiceSettleTokenEnvNames are consulted for the settle (checker)
// service token (first non-empty wins). Grants ledger:settle only.
var ServiceSettleTokenEnvNames = []string{"MERIDIAN_LEDGER_SETTLE_TOKEN", "LEDGER_SETTLE_TOKEN"}

func configuredServiceToken(names []string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// ServiceClaims returns the principal for a request carrying a configured
// service token, and true. ok is false when the header is absent, no token
// is configured, or the token matches neither role (constant-time compare
// against each configured token).
func ServiceClaims(r *http.Request) (Claims, bool) {
	got := r.Header.Get("X-Service-Token")
	if got == "" {
		return Claims{}, false
	}
	sub := "service:" + r.Header.Get("X-Service-Name")
	tenant := r.Header.Get("X-Tenant-ID")
	if want := configuredServiceToken(ServiceMakerTokenEnvNames); want != "" &&
		subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
		return Claims{Sub: sub, Roles: []string{"ledger:post"}, TenantID: tenant}, true
	}
	if want := configuredServiceToken(ServiceSettleTokenEnvNames); want != "" &&
		subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
		return Claims{Sub: sub, Roles: []string{"ledger:settle"}, TenantID: tenant}, true
	}
	return Claims{}, false
}
