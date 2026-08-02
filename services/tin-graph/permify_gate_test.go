// permify_gate_test.go — P0 authz wiring: live Permify officer scope, dev
// fallback, prod fail-closed.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	permifymodels "github.com/munisp/meridian-core-platform/packages/permify-models"
)

// runWithClaims executes canAdministerTIN behind auth.Middleware with a
// signed dev token carrying the given claims.
func runWithClaims(sub string, roles ...string) bool {
	tok, err := auth.SignHS256(auth.Claims{Sub: sub, Roles: roles}, time.Hour)
	if err != nil {
		panic(err)
	}
	allowed := false
	h := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed = canAdministerTIN(r)
	}))
	req := httptest.NewRequest("POST", "/v1/tin/provision", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(httptest.NewRecorder(), req)
	return allowed
}

func TestPermifyFromEnvDevFallback(t *testing.T) {
	t.Setenv("PERMIFY_URL", "")
	c, err := permifyFromEnv("dev")
	if err != nil || c != nil {
		t.Fatalf("dev without PERMIFY_URL must fall back, got %v %v", c, err)
	}
}

func TestPermifyFromEnvProdFailClosed(t *testing.T) {
	t.Setenv("PERMIFY_URL", "")
	if _, err := permifyFromEnv("prod"); err == nil {
		t.Fatal("PROFILE=prod without PERMIFY_URL must fail closed")
	}
}

func TestPermifyFromEnvLive(t *testing.T) {
	t.Setenv("PERMIFY_URL", "http://permify:3476")
	c, err := permifyFromEnv("prod")
	if err != nil || c == nil {
		t.Fatalf("PERMIFY_URL set must yield a live client, got %v %v", c, err)
	}
}

func TestCanAdministerTINDevFallback(t *testing.T) {
	old := permChecker
	permChecker = nil
	defer func() { permChecker = old }()

	if !runWithClaims("u1", "nrs:officer") {
		t.Fatal("nrs:officer must be allowed in dev fallback")
	}
	if runWithClaims("u2", "taxpayer") {
		t.Fatal("plain taxpayer must be denied in dev fallback")
	}
}

func TestCanAdministerTINLivePermify(t *testing.T) {
	var gotEntity, gotSub, gotPerm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Entity     struct{ Type, ID string } `json:"entity"`
			Subject    struct{ Type, ID string } `json:"subject"`
			Permission string                    `json:"permission"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotEntity = body.Entity.Type + ":" + body.Entity.ID
		gotSub = body.Subject.Type + ":" + body.Subject.ID
		gotPerm = body.Permission
		w.Write([]byte(`{"can":"RESULT_ALLOWED"}`))
	}))
	defer srv.Close()

	old := permChecker
	permChecker = permifymodels.NewClient(srv.URL, "t1")
	defer func() { permChecker = old }()

	if !runWithClaims("officer-7", "taxpayer") {
		t.Fatal("Permify RESULT_ALLOWED must authorize")
	}
	if gotEntity != "tenant:core" || gotSub != "user:officer-7" || gotPerm != "operate" {
		t.Fatalf("bad Permify check: entity=%s subject=%s perm=%s", gotEntity, gotSub, gotPerm)
	}
}

func TestCanAdministerTINLiveDeniedAndUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"can":"RESULT_DENIED"}`))
	}))
	old := permChecker
	defer func() { permChecker = old }()

	permChecker = permifymodels.NewClient(srv.URL, "t1")
	if runWithClaims("u9", "nrs:officer") {
		t.Fatal("Permify RESULT_DENIED must deny even for nrs:officer claims")
	}
	srv.Close() // unreachable -> fail closed
	if runWithClaims("u9", "nrs:officer") {
		t.Fatal("unreachable Permify must fail closed")
	}
}
