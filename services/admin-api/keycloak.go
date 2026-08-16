package main

// keycloak.go — stdlib RS256/JWKS verification for AUTH_MODE=keycloak (A6).
// No external OIDC dependency: the JWKS document is fetched from
// KEYCLOAK_JWKS_URL (or OIDC_ISSUER_URL + "/protocol/openid-connect/certs"),
// the signing key is matched by kid, and the RS256 signature is verified
// with crypto/rsa. Claims map onto the shared `claims` struct (realm roles
// from realm_access.roles).

import (
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type jwksKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type keycloakVerifier struct {
	jwksURL  string
	issuer   string // expected iss; validated when set
	audience string // expected aud; validated when set
	client   *http.Client
	mu       sync.RWMutex
	keys     map[string]*rsa.PublicKey
	fetched  time.Time
}

func newKeycloakVerifier() (*keycloakVerifier, error) {
	url := os.Getenv("KEYCLOAK_JWKS_URL")
	issuer := os.Getenv("KEYCLOAK_ISSUER")
	if issuer == "" {
		issuer = os.Getenv("OIDC_ISSUER_URL")
	}
	issuer = strings.TrimSuffix(issuer, "/")
	if url == "" {
		if issuer == "" {
			return nil, fmt.Errorf("KEYCLOAK_JWKS_URL or OIDC_ISSUER_URL must be set in keycloak mode")
		}
		url = issuer + "/protocol/openid-connect/certs"
	}
	audience := os.Getenv("KEYCLOAK_AUDIENCE")
	if os.Getenv("PROFILE") == "prod" && audience == "" {
		return nil, fmt.Errorf("KEYCLOAK_AUDIENCE must be set in prod keycloak mode (audience confusion fail-closed)")
	}
	v := &keycloakVerifier{
		jwksURL:  url,
		issuer:   issuer,
		audience: audience,
		client:   &http.Client{Timeout: 3 * time.Second},
		keys:     map[string]*rsa.PublicKey{},
	}
	if err := v.refresh(); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *keycloakVerifier) refresh() error {
	resp, err := v.client.Get(v.jwksURL)
	if err != nil {
		return fmt.Errorf("jwks fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch: status %d", resp.StatusCode)
	}
	var doc struct {
		Keys []jwksKey `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("jwks decode: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		e := 0
		for _, b := range eb {
			e = e<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}
	}
	if len(keys) == 0 {
		return fmt.Errorf("jwks document contained no usable RSA keys")
	}
	v.mu.Lock()
	v.keys = keys
	v.fetched = time.Now()
	v.mu.Unlock()
	return nil
}

func (v *keycloakVerifier) verifyJWT(tok string) (*claims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errString("malformed token")
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errString("bad header encoding")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		return nil, errString("bad header")
	}
	if hdr.Alg != "RS256" {
		return nil, errString("unexpected alg " + hdr.Alg)
	}
	v.mu.RLock()
	key, ok := v.keys[hdr.Kid]
	stale := time.Since(v.fetched) > 10*time.Minute
	v.mu.RUnlock()
	if !ok && stale {
		if err := v.refresh(); err == nil {
			v.mu.RLock()
			key, ok = v.keys[hdr.Kid]
			v.mu.RUnlock()
		}
	}
	if !ok {
		return nil, errString("unknown signing key")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errString("bad signature encoding")
	}
	h := crypto.SHA256.New()
	h.Write([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, h.Sum(nil), sig); err != nil {
		return nil, errString("signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errString("bad payload encoding")
	}
	var raw struct {
		Sub         string   `json:"sub"`
		Email       string   `json:"email"`
		Roles       []string `json:"roles"`
		RealmAccess struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
		TenantID string   `json:"tenant_id"`
		Issuer   string   `json:"iss"`
		Aud      any      `json:"aud"` // string or []string
		Expires  int64    `json:"exp"`
		IssuedAt int64    `json:"iat"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, errString("bad claims")
	}
	if time.Now().Unix() > raw.Expires {
		return nil, errString("token expired")
	}
	// r9 pentest defect: iss/aud were not validated — a token minted for any
	// other audience/issuer but signed by the realm key was accepted.
	if v.issuer != "" && raw.Issuer != v.issuer {
		return nil, errString("unexpected issuer")
	}
	if v.audience != "" && !audContains(raw.Aud, v.audience) {
		return nil, errString("unexpected audience")
	}
	roles := raw.Roles
	if len(roles) == 0 {
		roles = raw.RealmAccess.Roles
	}
	return &claims{
		Sub: raw.Sub, Email: raw.Email, Roles: roles, TenantID: raw.TenantID,
		Issuer: raw.Issuer, IssuedAt: raw.IssuedAt, Expires: raw.Expires,
	}, nil
}

// audContains reports whether the JWT aud claim (string or array) includes
// the expected audience.
func audContains(aud any, want string) bool {
	switch a := aud.(type) {
	case string:
		return a == want
	case []any:
		for _, v := range a {
			if s, ok := v.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}
