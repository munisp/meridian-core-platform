package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// B3 #14 regression: in prod (PROFILE=prod) the admin console must never
// fall back to the dev-seed in-memory ledger — handlers answer 502 when
// the real ledger svc is unreachable/unregistered. Dev keeps the fallback.

func TestLedgerFallbackProdRefused(t *testing.T) {
	t.Setenv("PROFILE", "prod")
	a := &app{store: NewStore(), authMode: "keycloak", client: &http.Client{}}
	// seed an in-mem account/transfer that prod must NOT expose
	a.store.LedgerAccounts["acc-1"] = &LedgerAccount{ID: "acc-1", Balance: 500, Currency: "NGN"}

	// accounts list
	rec := httptest.NewRecorder()
	a.handleLedgerAccounts(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/ledger/accounts", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("prod accounts: got %d, want 502 (no dev-seed fallback)", rec.Code)
	}

	// balance
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/ledger/accounts/acc-1/balance", nil)
	req.SetPathValue("id", "acc-1")
	rec = httptest.NewRecorder()
	a.handleLedgerBalance(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("prod balance: got %d, want 502", rec.Code)
	}

	// transfer create must not write an audit row for a fake transfer
	before := len(a.store.LedgerTransfers)
	body := `{"debit_account_id":"a","credit_account_id":"b","amount_kobo":100}`
	rec = httptest.NewRecorder()
	a.handleLedgerTransfer(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/ledger/transfers", strings.NewReader(body)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("prod transfer: got %d, want 502", rec.Code)
	}
	if len(a.store.LedgerTransfers) != before {
		t.Fatal("prod wrote a dev-seed transfer")
	}

	// post/void
	a.store.LedgerTransfers["tr-1"] = &LedgerTransfer{ID: "tr-1", State: "pending", DebitAccountID: "acc-1", CreditAccountID: "acc-1", AmountKobo: 5}
	req = httptest.NewRequest(http.MethodPost, "/v1/admin/ledger/transfers/tr-1/post", nil)
	req.SetPathValue("id", "tr-1")
	rec = httptest.NewRecorder()
	a.handleLedgerPost(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("prod post: got %d, want 502", rec.Code)
	}
	if a.store.LedgerTransfers["tr-1"].State != "pending" {
		t.Fatal("prod mutated the dev-seed transfer state")
	}
}

func TestLedgerFallbackDevStillWorks(t *testing.T) {
	t.Setenv("PROFILE", "dev")
	a := &app{store: NewStore(), authMode: "dev", client: &http.Client{}}
	a.store.LedgerAccounts["acc-9"] = &LedgerAccount{ID: "acc-9", Balance: 700, Currency: "NGN"}
	rec := httptest.NewRecorder()
	a.handleLedgerAccounts(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/ledger/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dev accounts: got %d, want 200 dev-seed", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "dev-seed") {
		t.Fatalf("dev accounts should be tagged dev-seed: %s", rec.Body.String())
	}
}
