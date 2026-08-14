package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression (F-2, W4 MEDIUM): admin-api must not combine a wildcard origin
// with the Authorization header. Explicit origins come from
// CORS_ALLOWED_ORIGINS; unmatched origins get no CORS grant.
func TestCORSExplicitOriginsOnly(t *testing.T) {
	t.Setenv("PROFILE", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://console.example.com")
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// allowed origin is echoed and may send Authorization
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/x", nil)
	req.Header.Set("Origin", "https://console.example.com")
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://console.example.com" {
		t.Fatalf("allowed origin not echoed: %q", got)
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Fatal("Vary: Origin required when echoing origins")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization,Content-Type,X-Dev-Role" {
		t.Fatalf("credentialed headers for allowlisted origin: %q", got)
	}

	// unknown origin gets no CORS grant
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/v1/x", nil)
	req.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unknown origin must get no grant, got %q", got)
	}
}

// Dev wildcard fallback must not allow Authorization.
func TestCORSDevWildcardStripsAuthorization(t *testing.T) {
	t.Setenv("PROFILE", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/v1/x", nil)
	req.Header.Set("Origin", "https://anything.example")
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("dev fallback: want *, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Fatalf("wildcard must not allow Authorization, got %q", got)
	}
}

// Wildcard entries in CORS_ALLOWED_ORIGINS are ignored (never combined with
// credentialed headers).
func TestCORSWildcardEntryIgnored(t *testing.T) {
	t.Setenv("PROFILE", "dev")
	t.Setenv("CORS_ALLOWED_ORIGINS", "*, https://console.example.com")
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/x", nil)
	req.Header.Set("Origin", "https://random.example")
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("wildcard entry must not grant arbitrary origins, got %q", got)
	}
}
