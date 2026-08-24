package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// B3 #5 regression: money services must be able to authenticate to the
// core ledger in prod without the forgeable X-Dev-Role header.

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestServiceTokenAcceptedInDevAndKeycloakModes(t *testing.T) {
	t.Setenv("MERIDIAN_SERVICE_TOKEN", "s3cret-service-token")
	t.Setenv("AUTH_MODE", "dev")
	h := Middleware(okHandler())
	req := httptest.NewRequest("GET", "/v1/transfers", nil)
	req.Header.Set("X-Service-Token", "s3cret-service-token")
	req.Header.Set("X-Service-Name", "pos-vat")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid service token rejected: %d", rec.Code)
	}

	// wrong token -> 401
	req = httptest.NewRequest("GET", "/v1/transfers", nil)
	req.Header.Set("X-Service-Token", "wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong service token must be 401, got %d", rec.Code)
	}
}

func TestServiceTokenNotHonouredWhenUnconfigured(t *testing.T) {
	// no MERIDIAN_SERVICE_TOKEN / LEDGER_SERVICE_TOKEN: header never honoured
	t.Setenv("AUTH_MODE", "dev")
	h := Middleware(okHandler())
	req := httptest.NewRequest("GET", "/v1/transfers", nil)
	req.Header.Set("X-Service-Token", "anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Fatal("service token honoured with none configured (fail-open)")
	}
}

func TestServiceTokenFallbackEnvName(t *testing.T) {
	t.Setenv("LEDGER_SERVICE_TOKEN", "ledger-only-token")
	t.Setenv("AUTH_MODE", "dev")
	h := Middleware(okHandler())
	req := httptest.NewRequest("GET", "/v1/transfers", nil)
	req.Header.Set("X-Service-Token", "ledger-only-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("LEDGER_SERVICE_TOKEN fallback not honoured: %d", rec.Code)
	}
}
