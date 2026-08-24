// authz_test.go — audit H-4: money-movement endpoints require roles.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/services/ledger/internal/tb"
)

func testHandler() http.Handler {
	srv := &server{client: tb.NewDevClient()}
	return auth.Middleware(srv.routes())
}

func bearer(t *testing.T, roles ...string) string {
	t.Helper()
	tok, err := auth.SignHS256(auth.Claims{Sub: "tester", Roles: roles}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + tok
}

func doReq(t *testing.T, h http.Handler, method, path, authz, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTransferRequiresLedgerPostRole(t *testing.T) {
	h := testHandler()
	body := `{"debit_account_id":"00000064000000000000000000000001","credit_account_id":"00000064000000000000000000000002","amount_kobo":100,"ledger":100,"code":1}`

	// forged low-role token (auditor) -> 403 RFC7807
	rec := doReq(t, h, "POST", "/v1/transfers", bearer(t, "auditor"), body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("auditor token: got %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
		t.Fatalf("want RFC7807 problem+json, got %q", ct)
	}

	// unauthenticated -> 401
	rec = doReq(t, h, "POST", "/v1/transfers", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: got %d, want 401", rec.Code)
	}

	// ledger:post -> reaches the handler (not 401/403)
	rec = doReq(t, h, "POST", "/v1/transfers", bearer(t, "ledger:post"), body)
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("ledger:post: got %d, want handler to run", rec.Code)
	}
}

func TestMoneyMovementEndpointsRequireRoles(t *testing.T) {
	h := testHandler()
	aud := bearer(t, "auditor")
	post := bearer(t, "ledger:post")
	settle := bearer(t, "ledger:settle")

	cases := []struct {
		method, path, body, okRole string
	}{
		{"POST", "/v1/accounts", `{"namespace":100}`, "ledger:admin"},
		{"POST", "/v1/transfers", `{}`, "ledger:post"},
		{"POST", "/v1/transfers/pending", `{}`, "ledger:post"},
		// B2-#12: settle/release are checker actions requiring the
		// DISTINCT "ledger:settle" role; the maker role must be rejected.
		{"POST", "/v1/transfers/00000064000000000000000000000001/post", `{}`, "ledger:settle"},
		{"POST", "/v1/transfers/00000064000000000000000000000001/void", `{}`, "ledger:settle"},
	}
	for _, tc := range cases {
		rec := doReq(t, h, tc.method, tc.path, aud, tc.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with auditor: got %d, want 403", tc.method, tc.path, rec.Code)
		}
		rec = doReq(t, h, tc.method, tc.path, post, tc.body)
		switch tc.okRole {
		case "ledger:admin":
			// ledger:post alone must NOT create accounts
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with ledger:post only: got %d, want 403", tc.method, tc.path, rec.Code)
			}
		case "ledger:settle":
			// maker role must NOT settle/void (maker != checker)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with ledger:post (maker): got %d, want 403", tc.method, tc.path, rec.Code)
			}
			rec = doReq(t, h, tc.method, tc.path, settle, tc.body)
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Errorf("%s %s with ledger:settle: got %d, want handler to run", tc.method, tc.path, rec.Code)
			}
		default:
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Errorf("%s %s with %s: got %d, want handler to run", tc.method, tc.path, tc.okRole, rec.Code)
			}
		}
	}
}

func TestReadEndpointsAllowAnyAuthenticatedRole(t *testing.T) {
	h := testHandler()
	rec := doReq(t, h, "GET", "/v1/accounts", bearer(t, "auditor"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/accounts with auditor: got %d, want 200", rec.Code)
	}
}
