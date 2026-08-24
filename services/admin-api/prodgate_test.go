package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
	"testing"
)

// TestProdRefusesDevAuth (A1-04 regression): PROFILE=prod must refuse to
// boot with AUTH_MODE=dev / unset / default dev secret (forgeable auth:
// public HS256 secret + X-Dev-Role header). Pre-fix validateAuthConfig did
// not exist and main() happily ran prod with dev auth.
func TestProdRefusesDevAuth(t *testing.T) {
	t.Setenv("PROFILE", "prod")
	if err := validateAuthConfig("dev", "meridian-dev-secret-change-me"); err == nil {
		t.Fatal("prod + AUTH_MODE=dev must fail closed")
	}
	if err := validateAuthConfig("dev", "explicit-secret"); err == nil {
		t.Fatal("prod + AUTH_MODE=dev must fail closed even with explicit secret")
	}
	t.Setenv("KEYCLOAK_AUDIENCE", "")
	if err := validateAuthConfig("keycloak", "x"); err == nil {
		t.Fatal("prod keycloak without KEYCLOAK_AUDIENCE must fail closed")
	}
	t.Setenv("KEYCLOAK_AUDIENCE", "nrs-api")
	t.Setenv("AUDIT_EVIDENCE_URL", "https://audit-evidence.internal") // B4-6: prod requires the WORM sink
	if err := validateAuthConfig("keycloak", "x"); err != nil {
		t.Fatalf("prod keycloak properly configured: %v", err)
	}
	// dev profile untouched
	t.Setenv("PROFILE", "dev")
	if err := validateAuthConfig("dev", "meridian-dev-secret-change-me"); err != nil {
		t.Fatalf("dev mode broken: %v", err)
	}
}

// TestAdminWriteEndpointsRoleGated (A1-11 regression): an authenticated
// principal WITHOUT the required role must get 403 on the audit/evidence/
// receipts/TAT POST endpoints (pre-fix: authn only — any authenticated user
// could append audit/evidence records).
func TestAdminWriteEndpointsRoleGated(t *testing.T) {
	a := &app{store: NewStore(), jwtSecret: "test-secret", authMode: "dev"}
	token, err := a.issueJWT(claims{Sub: "auditor-user", Email: "auditor@meridian.local", Roles: []string{"auditor"}, Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	posts := map[string]http.HandlerFunc{
		"/v1/admin/audit/events":    a.requireRole("operator", a.handleAuditAppend),
		"/v1/admin/evidence":        a.requireRole("operator", a.handleEvidenceCreate),
		"/v1/admin/flows/receipts":  a.requireRole("operator", a.handleFlowReceiptAppend),
		"/v1/admin/tat/assemble":    a.requireRole("admin", a.handleTATAssemble),
	}
	for path, h := range posts {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		a.requireAuth(h).ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s with auditor role: want 403, got %d (%s)", path, rec.Code, rec.Body.String())
		}
	}
}
