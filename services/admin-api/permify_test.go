// permify_test.go — P0 authz wiring: live Permify checks, dev fallback,
// prod fail-closed.
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	permifymodels "github.com/munisp/meridian-core-platform/packages/permify-models"
)

func TestPermifyFromEnvDevFallback(t *testing.T) {
	t.Setenv("PERMIFY_URL", "")
	t.Setenv("PROFILE", "")
	c, err := permifyFromEnv("dev")
	if err != nil || c != nil {
		t.Fatalf("dev without PERMIFY_URL must fall back (nil client, nil err), got %v %v", c, err)
	}
}

func TestPermifyFromEnvProdFailClosed(t *testing.T) {
	t.Setenv("PERMIFY_URL", "")
	t.Setenv("PROFILE", "prod")
	if _, err := permifyFromEnv("dev"); err == nil {
		t.Fatal("PROFILE=prod without PERMIFY_URL must fail closed")
	}
	t.Setenv("PROFILE", "")
	if _, err := permifyFromEnv("keycloak"); err == nil {
		t.Fatal("AUTH_MODE != dev without PERMIFY_URL must fail closed")
	}
}

func TestPermifyFromEnvLive(t *testing.T) {
	t.Setenv("PERMIFY_URL", "http://permify:3476")
	t.Setenv("PROFILE", "prod")
	c, err := permifyFromEnv("keycloak")
	if err != nil || c == nil {
		t.Fatalf("PERMIFY_URL set must yield a live client, got %v %v", c, err)
	}
}

// requireRole with a live Permify backend: server decision is authoritative.
func TestRequireRoleLivePermify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"can":"RESULT_ALLOWED"}`))
	}))
	defer srv.Close()
	a := &app{perm: permifymodels.NewClient(srv.URL, "t1")}

	called := false
	h := a.requireRole("operator", func(w http.ResponseWriter, r *http.Request) { called = true })
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	// no claims at all -> deny without even asking Permify
	h(rec, req)
	if called || rec.Code != http.StatusForbidden {
		t.Fatalf("no claims: want 403, got called=%v code=%d", called, rec.Code)
	}
}

func TestRequireRoleDevFallback(t *testing.T) {
	a := &app{} // PERMIFY_URL unset -> dev role-claim check
	called := false
	h := a.requireRole("operator", func(w http.ResponseWriter, r *http.Request) { called = true })
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if called || rec.Code != http.StatusForbidden {
		t.Fatalf("claims-less dev request: want 403, got called=%v code=%d", called, rec.Code)
	}
}
