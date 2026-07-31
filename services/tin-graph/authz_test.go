// authz_test.go — audit M-3: provision + taxpayer360 scoping.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/events/store"
	"github.com/munisp/meridian-core-platform/services/tin-graph/internal/graph"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// seed an entity with a known tin_hash
	if err := st.Put("entities", "ent-1", graph.Entity{ID: "ent-1", TINHash: "hash-alice", EntityType: "individual"}); err != nil {
		t.Fatal(err)
	}
	s := &server{st: st, cfg: graph.DefaultMatchConfig}
	return auth.Middleware(s.routes())
}

func bearer(t *testing.T, c auth.Claims) string {
	t.Helper()
	tok, err := auth.SignHS256(c, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + tok
}

func get(t *testing.T, h http.Handler, path, authz string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTaxpayer360CrossTINReadForbidden(t *testing.T) {
	h := testHandler(t)
	// taxpayer with tenant_id hash-bob tries to read hash-alice -> 403
	rec := get(t, h, "/v1/taxpayer360/hash-alice", bearer(t, auth.Claims{Sub: "bob", TenantID: "hash-bob"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-TIN read: got %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
		t.Fatalf("want RFC7807 problem+json, got %q", ct)
	}
}

func TestTaxpayer360OwnRecordAllowed(t *testing.T) {
	h := testHandler(t)
	rec := get(t, h, "/v1/taxpayer360/hash-alice", bearer(t, auth.Claims{Sub: "alice", TenantID: "hash-alice"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("own record: got %d, want 200", rec.Code)
	}
	// also via sub claim
	rec = get(t, h, "/v1/taxpayer360/hash-alice", bearer(t, auth.Claims{Sub: "hash-alice"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("own record via sub: got %d, want 200", rec.Code)
	}
}

func TestTaxpayer360OfficerAndAdminAllowed(t *testing.T) {
	h := testHandler(t)
	for _, role := range []string{"nrs:officer", "admin"} {
		rec := get(t, h, "/v1/taxpayer360/hash-alice", bearer(t, auth.Claims{Sub: "officer1", Roles: []string{role}}))
		if rec.Code != http.StatusOK {
			t.Fatalf("role %s: got %d, want 200", role, rec.Code)
		}
	}
}

func TestProvisionRequiresOfficerOrAdmin(t *testing.T) {
	h := testHandler(t)
	body := `{"nin":"12345678901","entity_type":"individual","name":"A"}`
	mk := func(authz string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/tin/provision", strings.NewReader(body))
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// low-role token -> 403
	if rec := mk(bearer(t, auth.Claims{Sub: "aud", Roles: []string{"auditor"}})); rec.Code != http.StatusForbidden {
		t.Fatalf("auditor provision: got %d, want 403", rec.Code)
	}
	// taxpayer (no roles) -> 403
	if rec := mk(bearer(t, auth.Claims{Sub: "taxpayer", TenantID: "hash-x"})); rec.Code != http.StatusForbidden {
		t.Fatalf("taxpayer provision: got %d, want 403", rec.Code)
	}
	// unauthenticated -> 401
	if rec := mk(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: got %d, want 401", rec.Code)
	}
	// officer -> reaches handler (not 401/403)
	if rec := mk(bearer(t, auth.Claims{Sub: "o1", Roles: []string{"nrs:officer"}})); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Fatalf("officer provision: got %d, want handler to run", rec.Code)
	}
}
