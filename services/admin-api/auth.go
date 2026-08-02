package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// Dev JWT issuer/verifier (HS256) per SPEC §1.3.
// Prod mode would validate Keycloak OIDC JWKS (OIDC_ISSUER_URL); out of scope for dev issuer.

type claims struct {
	Sub      string   `json:"sub"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	TenantID string   `json:"tenant_id"`
	Issuer   string   `json:"iss"`
	IssuedAt int64    `json:"iat"`
	Expires  int64    `json:"exp"`
}

type ctxKey string

const ctxClaims ctxKey = "claims"

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (a *app) issueJWT(c claims) (string, error) {
	header := b64url([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := header + "." + b64url(payload)
	mac := hmac.New(sha256.New, []byte(a.jwtSecret))
	mac.Write([]byte(body))
	return body + "." + b64url(mac.Sum(nil)), nil
}

func (a *app) verifyJWT(tok string) (*claims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errString("malformed token")
	}
	body := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(a.jwtSecret))
	mac.Write([]byte(body))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errString("bad signature encoding")
	}
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, errString("signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errString("bad payload encoding")
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, err
	}
	if time.Now().Unix() > c.Expires {
		return nil, errString("token expired")
	}
	return &c, nil
}

type errString string

func (e errString) Error() string { return string(e) }

// requireAuth enforces Bearer JWT; in AUTH_MODE=dev also accepts X-Dev-Role
// header (SPEC §1.3). AUTH_MODE=keycloak verifies RS256 tokens against the
// Keycloak JWKS and NEVER honours X-Dev-Role; a keycloak verifier that
// cannot be configured fails CLOSED (every request denied).
func (a *app) requireAuth(next http.Handler) http.Handler {
	var kc *keycloakVerifier
	if a.authMode == "keycloak" {
		v, err := newKeycloakVerifier()
		if err != nil {
			log.Printf("profile=prod component=admin-api keycloak misconfigured (%v); FAILING CLOSED", err)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeProblem(w, http.StatusServiceUnavailable, "auth misconfigured",
					"AUTH_MODE=keycloak but the Keycloak verifier is not configured; refusing all requests (fail closed)")
			})
		}
		kc = v
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			tok := strings.TrimPrefix(auth, "Bearer ")
			var c *claims
			var err error
			if kc != nil {
				c, err = kc.verifyJWT(tok)
			} else {
				c, err = a.verifyJWT(tok)
			}
			if err != nil {
				writeProblem(w, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxClaims, c)))
			return
		}
		if a.authMode == "dev" {
			if role := r.Header.Get("X-Dev-Role"); role != "" {
				c := &claims{Sub: "dev-" + role, Email: role + "@meridian.local", Roles: []string{role}, Issuer: "x-dev-role"}
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxClaims, c)))
				return
			}
		}
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "missing Bearer token")
	})
}

func getClaims(r *http.Request) *claims {
	c, _ := r.Context().Value(ctxClaims).(*claims)
	return c
}

func hasRole(c *claims, role string) bool {
	if c == nil {
		return false
	}
	for _, r := range c.Roles {
		if r == role || r == "admin" {
			return true
		}
	}
	return false
}

// requireRole wraps a handler func requiring a role (admin/board/operator/auditor).
func (a *app) requireRole(role string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowed, err := a.authorizeRole(r, role)
		if err != nil {
			// authz backend unreachable: fail closed
			writeProblem(w, http.StatusServiceUnavailable, "authorization unavailable",
				"Permify check failed; request denied (fail-closed)")
			return
		}
		if !allowed {
			writeProblem(w, http.StatusForbidden, "forbidden", "requires role "+role)
			return
		}
		h(w, r)
	}
}
