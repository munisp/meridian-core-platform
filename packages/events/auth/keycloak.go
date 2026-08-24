// Keycloak OIDC support (HARDENING H2). When AUTH_MODE=keycloak the Bearer
// token is an RS256 JWT issued by Keycloak; it is verified against the
// realm JWKS (KEYCLOAK_JWKS_URL, defaulting to
// {KEYCLOAK_ISSUER}/protocol/openid-connect/certs) with a 5-minute cache and
// refresh on unknown kid. iss/exp/aud (KEYCLOAK_AUDIENCE) are validated and
// realm_access.roles are mapped to the Claims.Roles claim.
package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/httpx"
)

// KeycloakConfig configures the RS256/JWKS verifier.
type KeycloakConfig struct {
	Issuer   string // KEYCLOAK_ISSUER, e.g. https://keycloak:8443/realms/meridian
	Audience string // KEYCLOAK_AUDIENCE, e.g. meridian-services
	JWKSURL  string // KEYCLOAK_JWKS_URL (derived from Issuer when empty)
	// HTTPClient is injectable for tests; nil uses a 10s-timeout client.
	HTTPClient *http.Client
}

// KeycloakConfigFromEnv reads the H1 env contract.
func KeycloakConfigFromEnv() KeycloakConfig {
	issuer := strings.TrimRight(httpx.Env("KEYCLOAK_ISSUER", ""), "/")
	jwks := httpx.Env("KEYCLOAK_JWKS_URL", "")
	if jwks == "" && issuer != "" {
		jwks = issuer + "/protocol/openid-connect/certs"
	}
	return KeycloakConfig{
		Issuer:   issuer,
		Audience: httpx.Env("KEYCLOAK_AUDIENCE", ""),
		JWKSURL:  jwks,
	}
}

// jwksCacheTTL is the HARDENING H2 mandated 5-minute JWKS cache.
const jwksCacheTTL = 5 * time.Minute

// KeycloakVerifier verifies RS256 JWTs against a Keycloak realm JWKS.
type KeycloakVerifier struct {
	cfg KeycloakConfig

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey // kid -> key
	fetchedAt time.Time
}

// NewKeycloakVerifier creates a verifier from config.
func NewKeycloakVerifier(cfg KeycloakConfig) (*KeycloakVerifier, error) {
	if cfg.JWKSURL == "" {
		return nil, errors.New("keycloak: JWKS URL required (set KEYCLOAK_ISSUER or KEYCLOAK_JWKS_URL)")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &KeycloakVerifier{cfg: cfg, keys: map[string]*rsa.PublicKey{}}, nil
}

// NewKeycloakVerifierFromEnv creates a verifier from the H1 env contract.
//
// A1-08: in PROFILE=prod, KEYCLOAK_AUDIENCE is mandatory — without audience
// pinning any token minted for ANY client of the realm is accepted
// (audience confusion). Refuse to construct the verifier (callers fail
// closed) rather than serve unverified auth. Mirrors the compliance authx
// fix (meridian-compliance-suite PR #29) and admin-api keycloak.go.
func NewKeycloakVerifierFromEnv() (*KeycloakVerifier, error) {
	cfg := KeycloakConfigFromEnv()
	if httpx.Env("PROFILE", "dev") == "prod" && cfg.Audience == "" {
		return nil, errors.New("keycloak: PROFILE=prod requires KEYCLOAK_AUDIENCE to be set explicitly (fail-closed: audience confusion otherwise)")
	}
	return NewKeycloakVerifier(cfg)
}

type jwksDoc struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

// verifyRS256 checks an RSASSA-PKCS1-v1_5 SHA-256 signature.
func verifyRS256(key *rsa.PublicKey, body, sig []byte) error {
	digest := sha256.Sum256(body)
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return errors.New("signature mismatch")
	}
	return nil
}

func b64urlInt(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

// refresh fetches the JWKS and replaces the cached key set.
func (v *KeycloakVerifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keycloak: JWKS fetch status %d", resp.StatusCode)
	}
	var doc jwksDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("keycloak: decode JWKS: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.N == "" || k.E == "" {
			continue
		}
		n, err := b64urlInt(k.N)
		if err != nil {
			continue
		}
		e, err := b64urlInt(k.E)
		if err != nil {
			continue
		}
		kid := k.Kid
		if kid == "" {
			kid = "_"
		}
		keys[kid] = &rsa.PublicKey{N: n, E: int(e.Int64())}
	}
	if len(keys) == 0 {
		return errors.New("keycloak: JWKS contained no usable RSA keys")
	}
	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

// keyFor returns the cached key for kid, fetching/refreshing the JWKS as
// needed: on first use, after the 5-minute TTL, or on unknown kid.
func (v *KeycloakVerifier) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	lookup := func() (*rsa.PublicKey, bool) {
		v.mu.RLock()
		defer v.mu.RUnlock()
		if kid == "" {
			if len(v.keys) == 1 {
				for _, k := range v.keys {
					return k, true
				}
			}
			k, ok := v.keys["_"]
			return k, ok
		}
		k, ok := v.keys[kid]
		return k, ok
	}
	if k, ok := lookup(); ok && time.Since(v.fetchedAt) < jwksCacheTTL {
		return k, nil
	}
	// Refresh: cache miss, stale TTL, or unknown kid (key rollover).
	if err := v.refresh(ctx); err != nil {
		// Fall back to a still-cached key if the refresh failed.
		if k, ok := lookup(); ok {
			return k, nil
		}
		return nil, err
	}
	if k, ok := lookup(); ok {
		return k, nil
	}
	return nil, fmt.Errorf("keycloak: unknown kid %q", kid)
}

type keycloakPayload struct {
	Sub      string   `json:"sub"`
	Iss      string   `json:"iss"`
	Exp      int64    `json:"exp"`
	Iat      int64    `json:"iat"`
	Aud      any      `json:"aud"` // string or []string
	TenantID string   `json:"tenant_id"`
	Roles    []string `json:"roles"`
	Realm    struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	ResourceAccess map[string]struct {
		Roles []string `json:"roles"`
	} `json:"resource_access"`
}

func (p *keycloakPayload) audiences() []string {
	switch a := p.Aud.(type) {
	case string:
		return []string{a}
	case []any:
		out := make([]string, 0, len(a))
		for _, s := range a {
			if str, ok := s.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// VerifyRS256 validates a compact RS256 Keycloak JWT: signature against the
// realm JWKS, iss/exp/aud per config, and maps realm roles to Claims.Roles.
func (v *KeycloakVerifier) VerifyRS256(token string) (Claims, error) {
	return v.VerifyRS256Context(context.Background(), token)
}

// VerifyRS256Context is VerifyRS256 with a caller context for JWKS fetches.
func (v *KeycloakVerifier) VerifyRS256Context(ctx context.Context, token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("malformed token")
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, errors.New("bad header encoding")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		return Claims{}, errors.New("bad header")
	}
	if hdr.Alg != "RS256" {
		return Claims{}, fmt.Errorf("unexpected alg %q (RS256 required)", hdr.Alg)
	}
	key, err := v.keyFor(ctx, hdr.Kid)
	if err != nil {
		return Claims{}, err
	}
	body := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, errors.New("bad signature encoding")
	}
	if err := verifyRS256(key, []byte(body), sig); err != nil {
		return Claims{}, err
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, errors.New("bad payload encoding")
	}
	var p keycloakPayload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		return Claims{}, fmt.Errorf("bad claims: %w", err)
	}
	if v.cfg.Issuer != "" && p.Iss != v.cfg.Issuer {
		return Claims{}, fmt.Errorf("issuer mismatch: got %q", p.Iss)
	}
	if p.Exp == 0 {
		return Claims{}, errors.New("exp claim required")
	}
	if time.Now().Unix() > p.Exp {
		return Claims{}, errors.New("token expired")
	}
	if v.cfg.Audience != "" {
		ok := false
		for _, a := range p.audiences() {
			if a == v.cfg.Audience {
				ok = true
				break
			}
		}
		if !ok {
			return Claims{}, fmt.Errorf("audience %q not present", v.cfg.Audience)
		}
	}
	// Role mapping: explicit roles claim wins; otherwise Keycloak realm roles
	// (plus client roles for the configured audience) are mapped through.
	roles := p.Roles
	if len(roles) == 0 {
		roles = append(roles, p.Realm.Roles...)
		if v.cfg.Audience != "" {
			if ra, ok := p.ResourceAccess[v.cfg.Audience]; ok {
				roles = append(roles, ra.Roles...)
			}
		}
	}
	return Claims{
		Sub:      p.Sub,
		Roles:    dedupe(roles),
		TenantID: p.TenantID,
		Exp:      p.Exp,
		Iat:      p.Iat,
	}, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Default shared verifier for the keycloak middleware path (lazy-init).
var (
	sharedMu       sync.Mutex
	sharedVerifier *KeycloakVerifier
	sharedErr      error
)

// SharedKeycloakVerifier returns the process-wide verifier built from env.
func SharedKeycloakVerifier() (*KeycloakVerifier, error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedVerifier == nil && sharedErr == nil {
		sharedVerifier, sharedErr = NewKeycloakVerifierFromEnv()
	}
	return sharedVerifier, sharedErr
}

// KeycloakMiddleware is the AUTH_MODE=keycloak variant of Middleware: Bearer
// tokens must be valid RS256 Keycloak JWTs. X-Dev-Role is NOT accepted.
func KeycloakMiddleware(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// B3 #5: service-to-service callers authenticate with the shared
		// service token instead of an end-user JWT.
		if ServiceTokenValid(r) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ServiceClaims(r))))
			return
		}
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			httpx.Errorf(w, http.StatusUnauthorized, "unauthorized", "Bearer JWT required")
			return
		}
		v, err := SharedKeycloakVerifier()
		if err != nil {
			httpx.Errorf(w, http.StatusInternalServerError, "auth misconfigured", "%v", err)
			return
		}
		claims, err := v.VerifyRS256Context(r.Context(), strings.TrimPrefix(authz, "Bearer "))
		if err != nil {
			httpx.Errorf(w, http.StatusUnauthorized, "unauthorized", "invalid bearer token: %v", err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, claims)))
	}
}
