package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression (B2-#12 repair, V2 round): the previous shared service token
// granted ONE identity BOTH the maker ("ledger:post") and checker
// ("ledger:settle") roles, so a single stolen service token ran the full
// hold->settle saga and maker/checker SoD never constrained the machine
// path. Now maker and settle tokens are DISTINCT env-injected secrets and
// no token carries both roles.

func svcReq(token string) *http.Request {
	r := httptest.NewRequest("POST", "/v1/transfers/pending", nil)
	r.Header.Set("X-Service-Token", token)
	r.Header.Set("X-Service-Name", "settlement")
	return r
}

func TestServiceTokens_DistinctMakerAndChecker(t *testing.T) {
	t.Setenv("MERIDIAN_LEDGER_MAKER_TOKEN", "maker-secret")
	t.Setenv("MERIDIAN_LEDGER_SETTLE_TOKEN", "settle-secret")

	mk, ok := ServiceClaims(svcReq("maker-secret"))
	if !ok {
		t.Fatal("maker token not honoured")
	}
	if !mk.HasRole("ledger:post") || mk.HasRole("ledger:settle") {
		t.Fatalf("maker token roles = %v, want [ledger:post] only", mk.Roles)
	}

	st, ok := ServiceClaims(svcReq("settle-secret"))
	if !ok {
		t.Fatal("settle token not honoured")
	}
	if !st.HasRole("ledger:settle") || st.HasRole("ledger:post") {
		t.Fatalf("settle token roles = %v, want [ledger:settle] only", st.Roles)
	}
}

func TestServiceTokens_NoTokenCarriesBothRoles(t *testing.T) {
	t.Setenv("MERIDIAN_LEDGER_MAKER_TOKEN", "maker-secret")
	t.Setenv("MERIDIAN_LEDGER_SETTLE_TOKEN", "settle-secret")
	for _, tok := range []string{"maker-secret", "settle-secret"} {
		c, ok := ServiceClaims(svcReq(tok))
		if !ok {
			t.Fatalf("token %q not honoured", tok)
		}
		if c.HasRole("ledger:post") && c.HasRole("ledger:settle") {
			t.Fatalf("token %q carries BOTH maker and checker roles: %v", tok, c.Roles)
		}
	}
}

func TestServiceTokens_WrongOrUnsetNotHonoured(t *testing.T) {
	t.Setenv("MERIDIAN_LEDGER_MAKER_TOKEN", "maker-secret")
	t.Setenv("MERIDIAN_LEDGER_SETTLE_TOKEN", "settle-secret")
	if _, ok := ServiceClaims(svcReq("attacker-guess")); ok {
		t.Fatal("wrong token must not authenticate")
	}
	// Unconfigured tokens: header never honoured (fail closed).
	t.Setenv("MERIDIAN_LEDGER_MAKER_TOKEN", "")
	t.Setenv("LEDGER_MAKER_TOKEN", "")
	if _, ok := ServiceClaims(svcReq("maker-secret")); ok {
		t.Fatal("unconfigured maker token must not be honoured")
	}
	// No header at all.
	if _, ok := ServiceClaims(httptest.NewRequest("POST", "/x", nil)); ok {
		t.Fatal("missing X-Service-Token must not authenticate")
	}
}

func TestServiceTokens_MiddlewareGrantsLedgerRoles(t *testing.T) {
	t.Setenv("MERIDIAN_LEDGER_MAKER_TOKEN", "maker-secret")
	t.Setenv("MERIDIAN_LEDGER_SETTLE_TOKEN", "settle-secret")
	var got Claims
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = FromContext(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), svcReq("settle-secret"))
	if !got.HasRole("ledger:settle") || got.HasRole("ledger:post") {
		t.Fatalf("middleware claims = %v, want settle-only", got.Roles)
	}
	if got.Sub != "service:settlement" {
		t.Fatalf("sub = %q", got.Sub)
	}
}
