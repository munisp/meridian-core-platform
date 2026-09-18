package main

// R4-9c tests: ledger money routes gated on step-up tickets.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
	"github.com/munisp/meridian-core-platform/packages/stepup"
)

func newTestGate(t *testing.T, profile string) *stepupGate {
	t.Helper()
	t.Setenv("PROFILE", profile)
	t.Setenv("STEPUP_TICKET_KEY", "test-ticket")
	g, err := newStepupGate()
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func claimsCtx(r *http.Request, sub string) *http.Request {
	return r.WithContext(auth.ContextWithClaims(context.Background(), auth.Claims{Sub: sub}))
}

func gateReq(t *testing.T, g *stepupGate, sub, body, ticket string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/transfers", strings.NewReader(body))
	if ticket != "" {
		req.Header.Set("X-Stepup-Ticket", ticket)
	}
	req = claimsCtx(req, sub)
	rec := httptest.NewRecorder()
	g.requireStepUp("ledger.transfer.create", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})(rec, req)
	return rec
}

// Prod, no ticket -> 403 step_up_required (fail closed).
func TestLedgerMoneyRouteProdNoTicketForbidden(t *testing.T) {
	g := newTestGate(t, "prod")
	rec := gateReq(t, g, "op-1", `{"a":1}`, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "step_up_required") {
		t.Fatalf("want step_up_required, got %s", rec.Body.String())
	}
}

// Dev, no ticket -> loud-log bypass.
func TestLedgerMoneyRouteDevBypass(t *testing.T) {
	g := newTestGate(t, "dev")
	if rec := gateReq(t, g, "op-1", `{"a":1}`, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("dev bypass: got %d want 204", rec.Code)
	}
}

// Valid ticket proceeds; replay -> 401; tampered payload -> 401; wrong
// actor -> 401.
func TestLedgerTicketVerifyAndReplay(t *testing.T) {
	g := newTestGate(t, "prod")
	key := []byte("test-ticket")
	body := `{"a":1}`
	tok, err := stepup.MintTicket(key, "op-1", "ledger.transfer.create", stepup.HashPayload([]byte(body)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rec := gateReq(t, g, "op-1", body, tok); rec.Code != http.StatusNoContent {
		t.Fatalf("ticket use: got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := gateReq(t, g, "op-1", body, tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replay: got %d want 401", rec.Code)
	}
	tok2, _ := stepup.MintTicket(key, "op-1", "ledger.transfer.create", stepup.HashPayload([]byte(body)), time.Now())
	if rec := gateReq(t, g, "op-1", `{"a":2}`, tok2); rec.Code != http.StatusUnauthorized {
		t.Fatalf("payload tamper: got %d want 401", rec.Code)
	}
	tok3, _ := stepup.MintTicket(key, "op-1", "ledger.transfer.create", stepup.HashPayload([]byte(body)), time.Now())
	if rec := gateReq(t, g, "op-2", body, tok3); rec.Code != http.StatusUnauthorized {
		t.Fatalf("actor mismatch: got %d want 401", rec.Code)
	}
	// garbage ticket
	if rec := gateReq(t, g, "op-1", body, "not-a-ticket"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("malformed: got %d want 401", rec.Code)
	}
}
