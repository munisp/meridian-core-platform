package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid})
	body := base64.RawURLEncoding.EncodeToString(hdr) + "." +
		base64.RawURLEncoding.EncodeToString(mustJSON(t, claims))
	digest := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return body + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func jwksServer(t *testing.T, pub *rsa.PublicKey, kid string, fetches *atomic.Int32) *httptest.Server {
	t.Helper()
	e := big.NewInt(int64(pub.E))
	doc := map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(e.Bytes()),
	}}}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, string(mustJSON(t, doc)))
	}))
}

func TestKeycloakVerifierRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	srv := jwksServer(t, &key.PublicKey, "kid-1", &fetches)
	defer srv.Close()

	iss := "https://keycloak:8443/realms/meridian"
	v, err := NewKeycloakVerifier(KeycloakConfig{
		Issuer: iss, Audience: "meridian-services", JWKSURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{
		"sub": "svc-ledger", "iss": iss,
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
		"aud": "meridian-services",
		"realm_access": map[string]any{"roles": []string{"operator", "operator", "auditor"}},
	}
	tok := signRS256(t, key, "kid-1", claims)
	got, err := v.VerifyRS256(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Sub != "svc-ledger" || !got.HasRole("operator") || !got.HasRole("auditor") {
		t.Fatalf("bad claims: %+v", got)
	}
	if len(got.Roles) != 2 {
		t.Fatalf("roles not deduped: %v", got.Roles)
	}
	// second verify: cache hit, no extra JWKS fetch
	if _, err := v.VerifyRS256(tok); err != nil {
		t.Fatal(err)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("expected 1 JWKS fetch (cached), got %d", n)
	}
}

func TestKeycloakVerifierRefreshOnUnknownKid(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	var fetches atomic.Int32
	// server initially serves kid-old; we flip it to kid-new after first fetch
	kid := &atomic.Value{}
	kid.Store("kid-old")
	e := big.NewInt(int64(key.PublicKey.E))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		doc := map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": kid.Load().(string), "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(e.Bytes()),
		}}}
		fmt.Fprint(w, string(mustJSON(t, doc)))
	}))
	defer srv.Close()
	v, err := NewKeycloakVerifier(KeycloakConfig{JWKSURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	// prime cache with kid-old
	if _, err := v.VerifyRS256(signRS256(t, key, "kid-old", map[string]any{
		"sub": "a", "exp": time.Now().Add(time.Hour).Unix(),
	})); err != nil {
		t.Fatal(err)
	}
	// rollover: JWKS now serves kid-new; token uses kid-new -> refresh on unknown kid
	kid.Store("kid-new")
	if _, err := v.VerifyRS256(signRS256(t, key, "kid-new", map[string]any{
		"sub": "b", "exp": time.Now().Add(time.Hour).Unix(),
	})); err != nil {
		t.Fatalf("refresh on unknown kid failed: %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("expected 2 fetches, got %d", n)
	}
}

func TestKeycloakVerifierRejects(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	var fetches atomic.Int32
	srv := jwksServer(t, &key.PublicKey, "kid-1", &fetches)
	defer srv.Close()
	v, _ := NewKeycloakVerifier(KeycloakConfig{
		Issuer: "iss-x", Audience: "aud-y", JWKSURL: srv.URL,
	})
	base := map[string]any{
		"sub": "s", "iss": "iss-x", "aud": "aud-y",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	// wrong signer
	if _, err := v.VerifyRS256(signRS256(t, other, "kid-1", base)); err == nil {
		t.Fatal("expected signature mismatch")
	}
	// wrong issuer
	bad := map[string]any{"sub": "s", "iss": "nope", "aud": "aud-y", "exp": time.Now().Add(time.Hour).Unix()}
	if _, err := v.VerifyRS256(signRS256(t, key, "kid-1", bad)); err == nil {
		t.Fatal("expected issuer rejection")
	}
	// wrong audience
	bad = map[string]any{"sub": "s", "iss": "iss-x", "aud": "other", "exp": time.Now().Add(time.Hour).Unix()}
	if _, err := v.VerifyRS256(signRS256(t, key, "kid-1", bad)); err == nil {
		t.Fatal("expected audience rejection")
	}
	// expired
	bad = map[string]any{"sub": "s", "iss": "iss-x", "aud": "aud-y", "exp": time.Now().Add(-time.Hour).Unix()}
	if _, err := v.VerifyRS256(signRS256(t, key, "kid-1", bad)); err == nil {
		t.Fatal("expected expiry rejection")
	}
	// HS256 token rejected in keycloak mode
	hs, err := SignHS256(Claims{Sub: "s"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.VerifyRS256(hs); err == nil {
		t.Fatal("expected alg rejection")
	}
}
