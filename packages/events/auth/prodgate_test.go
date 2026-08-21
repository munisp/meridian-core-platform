package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A1-08: PROFILE=prod + AUTH_MODE=keycloak without KEYCLOAK_AUDIENCE must
// fail closed (verifier construction error), mirroring compliance authx
// PR #29 and admin-api keycloak.go:57.
func TestProdKeycloakRequiresAudience(t *testing.T) {
	t.Setenv("PROFILE", "prod")
	t.Setenv("AUTH_MODE", "keycloak")
	t.Setenv("KEYCLOAK_ISSUER", "https://idp.example/realms/m")
	t.Setenv("KEYCLOAK_AUDIENCE", "")
	if _, err := NewKeycloakVerifierFromEnv(); err == nil {
		t.Fatal("prod keycloak without KEYCLOAK_AUDIENCE must fail closed")
	}
	t.Setenv("KEYCLOAK_AUDIENCE", "meridian-services")
	if _, err := NewKeycloakVerifierFromEnv(); err != nil {
		t.Fatalf("prod keycloak with audience configured must build: %v", err)
	}
	// non-prod stays permissive (dev convenience)
	t.Setenv("PROFILE", "dev")
	t.Setenv("KEYCLOAK_AUDIENCE", "")
	if _, err := NewKeycloakVerifierFromEnv(); err != nil {
		t.Fatalf("dev keycloak without audience must still build: %v", err)
	}
}

// A1-10: PROFILE=prod must never serve dev auth — AUTH_MODE=dev or the
// default/missing MERIDIAN_DEV_JWT_SECRET fails closed (503 on every
// request, including requests presenting a validly-signed dev token).
func TestProdRefusesDevAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	// prod + AUTH_MODE=dev: forgeable X-Dev-Role path must be denied.
	t.Setenv("PROFILE", "prod")
	t.Setenv("AUTH_MODE", "dev")
	t.Setenv("MERIDIAN_DEV_JWT_SECRET", "strong-prod-secret-0123456789abcdef")
	h := Middleware(ok)
	r := httptest.NewRequest("GET", "/v1/x", nil)
	r.Header.Set("X-Dev-Role", "admin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("prod AUTH_MODE=dev must fail closed, got %d", w.Code)
	}

	// prod + keycloak but default dev secret still set: denied.
	t.Setenv("AUTH_MODE", "keycloak")
	t.Setenv("KEYCLOAK_ISSUER", "https://idp.example/realms/m")
	t.Setenv("KEYCLOAK_AUDIENCE", "meridian-services")
	t.Setenv("MERIDIAN_DEV_JWT_SECRET", "meridian-dev-secret-change-me") // the well-known default
	h = Middleware(ok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/x", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("prod with default dev secret must fail closed, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "auth misconfigured") {
		t.Fatalf("expected fail-closed body, got %q", w.Body.String())
	}

	// prod fully configured: requests flow (401 without credentials).
	t.Setenv("MERIDIAN_DEV_JWT_SECRET", "strong-prod-secret-0123456789abcdef")
	h = Middleware(ok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/x", nil))
	if w.Code == http.StatusServiceUnavailable {
		t.Fatalf("configured prod must not fail closed, got %d", w.Code)
	}

	// dev default remains usable outside prod.
	t.Setenv("PROFILE", "dev")
	t.Setenv("AUTH_MODE", "dev")
	t.Setenv("MERIDIAN_DEV_JWT_SECRET", "")
	h = Middleware(ok)
	r = httptest.NewRequest("GET", "/v1/x", nil)
	r.Header.Set("X-Dev-Role", "admin")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("dev X-Dev-Role must still work, got %d", w.Code)
	}
}
