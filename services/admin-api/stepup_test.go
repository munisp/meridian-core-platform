package main

// R4-9c tests: TOTP step-up gating on admin money routes.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/stepup"
)

func newStepupTestApp(t *testing.T, profile string) *app {
	t.Helper()
	t.Setenv("PROFILE", profile)
	t.Setenv("STEPUP_SEAL_KEY", "test-seal")
	t.Setenv("STEPUP_TICKET_KEY", "test-ticket")
	a := &app{store: NewStore(), authMode: "dev", jwtSecret: "x"}
	if err := a.initStepup(); err != nil {
		t.Fatal(err)
	}
	return a
}

func withAdminClaims(r *http.Request, sub string) *http.Request {
	c := &claims{Sub: sub, Roles: []string{"admin"}}
	return r.WithContext(context.WithValue(r.Context(), ctxClaims, c))
}

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

// enroll + confirm actor, returning the raw secret.
func enrollActor(t *testing.T, a *app, sub string) string {
	t.Helper()
	secret, _, _, err := a.stepup.svc.Enroll(sub)
	if err != nil {
		t.Fatal(err)
	}
	code, err := stepup.Code(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.stepup.svc.Confirm(sub, code); err != nil {
		t.Fatal(err)
	}
	return secret
}

func gatedReq(t *testing.T, a *app, sub, body string, hdrs map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/ledger/transfers", strings.NewReader(body))
	req = withAdminClaims(req, sub)
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.requireStepUp("admin.ledger.transfer", okHandler)(rec, req)
	return rec
}

// Money route without any step-up credential: enrolled -> 403 everywhere;
// unenrolled in prod -> 403 step_up_required (fail closed).
func TestMoneyRouteNoStepUpForbidden(t *testing.T) {
	a := newStepupTestApp(t, "prod")
	enrollActor(t, a, "admin-1")
	if rec := gatedReq(t, a, "admin-1", `{}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("enrolled, no credential: got %d want 403", rec.Code)
	}
	// unenrolled admin in prod: fail closed
	if rec := gatedReq(t, a, "admin-2", `{}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("unenrolled prod: got %d want 403", rec.Code)
	} else if !strings.Contains(rec.Body.String(), "step_up_required") {
		t.Fatalf("want step_up_required, got %s", rec.Body.String())
	}
}

// Dev profile: unenrolled admin bypasses with a loud log (documented).
func TestMoneyRouteDevBypassUnenrolled(t *testing.T) {
	a := newStepupTestApp(t, "dev")
	if rec := gatedReq(t, a, "admin-x", `{}`, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("dev bypass: got %d want 204 (%s)", rec.Code, rec.Body.String())
	}
}

// Valid TOTP proceeds; out-of-window code -> 401.
func TestMoneyRouteValidTOTP(t *testing.T) {
	a := newStepupTestApp(t, "prod")
	secret := enrollActor(t, a, "admin-1")
	code, err := stepup.Code(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rec := gatedReq(t, a, "admin-1", `{}`, map[string]string{"X-Stepup-Code": code}); rec.Code != http.StatusNoContent {
		t.Fatalf("valid TOTP: got %d want 204 (%s)", rec.Code, rec.Body.String())
	}
	stale, _ := stepup.Code(secret, time.Now().Add(-120*time.Second))
	if rec := gatedReq(t, a, "admin-1", `{}`, map[string]string{"X-Stepup-Code": stale}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale TOTP: got %d want 401", rec.Code)
	}
	if rec := gatedReq(t, a, "admin-1", `{}`, map[string]string{"X-Stepup-Code": "123456"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong TOTP: got %d want 401", rec.Code)
	}
}

// Challenge endpoint mints a ticket; ticket works once; replay -> 401;
// wrong payload binding -> 401.
func TestStepUpTicketFlowAndReplay(t *testing.T) {
	a := newStepupTestApp(t, "prod")
	secret := enrollActor(t, a, "admin-1")
	code, _ := stepup.Code(secret, time.Now())

	body := `{"amount_kobo":500}`
	chal := `{"code":"` + code + `","action":"admin.ledger.transfer","payload":` + body + `}`
	req := httptest.NewRequest(http.MethodPost, "/v1/stepup/challenge", strings.NewReader(chal))
	req = withAdminClaims(req, "admin-1")
	rec := httptest.NewRecorder()
	a.handleStepupChallenge(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge: got %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Ticket == "" {
		t.Fatalf("ticket decode: %v", err)
	}
	// first use proceeds
	if rec := gatedReq(t, a, "admin-1", body, map[string]string{"X-Stepup-Ticket": out.Ticket}); rec.Code != http.StatusNoContent {
		t.Fatalf("ticket use: got %d (%s)", rec.Code, rec.Body.String())
	}
	// replay -> 401
	if rec := gatedReq(t, a, "admin-1", body, map[string]string{"X-Stepup-Ticket": out.Ticket}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("ticket replay: got %d want 401", rec.Code)
	}
	// fresh ticket, tampered payload -> 401
	code2, _ := stepup.Code(secret, time.Now())
	chal2 := strings.Replace(chal, code, code2, 1)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/stepup/challenge", strings.NewReader(chal2))
	req2 = withAdminClaims(req2, "admin-1")
	rec2 := httptest.NewRecorder()
	a.handleStepupChallenge(rec2, req2)
	var out2 struct {
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &out2)
	if rec := gatedReq(t, a, "admin-1", `{"amount_kobo":999}`, map[string]string{"X-Stepup-Ticket": out2.Ticket}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("payload tamper: got %d want 401", rec.Code)
	}
}

// Enrollment flow via HTTP handlers: enroll -> otpauth URI + recovery
// codes; confirm activates; recovery code is single-use; disable requires
// step-up.
func TestStepUpEnrollmentHTTPFlow(t *testing.T) {
	a := newStepupTestApp(t, "prod")
	// enroll
	req := httptest.NewRequest(http.MethodPost, "/v1/stepup/enroll", nil)
	req = withAdminClaims(req, "admin-1")
	rec := httptest.NewRecorder()
	a.handleStepupEnroll(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", rec.Code, rec.Body.String())
	}
	var enr struct {
		URI      string   `json:"otpauth_uri"`
		Recovery []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &enr); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enr.URI, "otpauth://totp/") || len(enr.Recovery) != stepup.RecoveryCount {
		t.Fatalf("bad enroll response: %v", enr)
	}
	// non-admin cannot enroll
	reqNA := httptest.NewRequest(http.MethodPost, "/v1/stepup/enroll", nil)
	reqNA = reqNA.WithContext(context.WithValue(reqNA.Context(), ctxClaims, &claims{Sub: "op-1", Roles: []string{"operator"}}))
	recNA := httptest.NewRecorder()
	a.handleStepupEnroll(recNA, reqNA)
	if recNA.Code != http.StatusForbidden {
		t.Fatalf("operator enroll: got %d want 403", recNA.Code)
	}
	// confirm with a code derived from the stored (sealed) secret
	stored, err := a.stepup.store.Get("admin-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.stepup.svc.Sealer.Open(stored.SealedSecret)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := stepup.Code(raw, time.Now())
	creq := httptest.NewRequest(http.MethodPost, "/v1/stepup/confirm", strings.NewReader(`{"code":"`+code+`"}`))
	creq = withAdminClaims(creq, "admin-1")
	crec := httptest.NewRecorder()
	a.handleStepupConfirm(crec, creq)
	if crec.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", crec.Code, crec.Body.String())
	}
	// recovery code works once as X-Stepup-Code
	if rec := gatedReq(t, a, "admin-1", `{}`, map[string]string{"X-Stepup-Code": enr.Recovery[0]}); rec.Code != http.StatusNoContent {
		t.Fatalf("recovery use: got %d", rec.Code)
	}
	if rec := gatedReq(t, a, "admin-1", `{}`, map[string]string{"X-Stepup-Code": enr.Recovery[0]}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("recovery replay: got %d want 401", rec.Code)
	}
	// disable without step-up -> 401; with valid TOTP -> 200
	dreq := httptest.NewRequest(http.MethodPost, "/v1/stepup/disable", nil)
	dreq = withAdminClaims(dreq, "admin-1")
	drec := httptest.NewRecorder()
	a.handleStepupDisable(drec, dreq)
	if drec.Code != http.StatusUnauthorized {
		t.Fatalf("disable without code: got %d want 401", drec.Code)
	}
	code2, _ := stepup.Code(raw, time.Now())
	dreq2 := httptest.NewRequest(http.MethodPost, "/v1/stepup/disable", nil)
	dreq2 = withAdminClaims(dreq2, "admin-1")
	dreq2.Header.Set("X-Stepup-Code", code2)
	drec2 := httptest.NewRecorder()
	a.handleStepupDisable(drec2, dreq2)
	if drec2.Code != http.StatusOK {
		t.Fatalf("disable with code: got %d", drec2.Code)
	}
	if a.stepup.svc.Enrolled("admin-1") {
		t.Fatal("still enrolled after disable")
	}
}
