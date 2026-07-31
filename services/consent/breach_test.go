package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
)

// bearerFor signs a dev HS256 token for the given claims.
func bearerFor(t *testing.T, c auth.Claims) string {
	t.Helper()
	tok, err := auth.SignHS256(c, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + tok
}

// withClaims runs h behind the real auth middleware; requests must carry the
// Authorization header produced by bearerFor.
func withClaims(h http.HandlerFunc, _ auth.Claims) http.HandlerFunc {
	return auth.Middleware(h).ServeHTTP
}

func doBreach(t *testing.T, h http.HandlerFunc, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestBreachRegisterAndDeadline(t *testing.T) {
	s := newTestServer(t)
	officer := auth.Claims{Sub: "dpo", Roles: []string{"privacy:officer"}}
	h := withClaims(s.breachCreate, officer)

	detected := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	rec := doBreach(t, h, "POST", "/v1/privacy/breaches", bearerFor(t, officer), map[string]any{
		"title": "ledger export leaked", "severity": "high",
		"affected_subjects": []string{"tin-hash-1", "tin-hash-2"},
		"detected_at":       detected,
	})
	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Breach Breach `json:"breach"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	b := out.Breach
	dt, _ := time.Parse(time.RFC3339, b.DetectedAt)
	dl, _ := time.Parse(time.RFC3339, b.NotifyDeadline)
	if dl.Sub(dt) != 72*time.Hour {
		t.Fatalf("notify_deadline must be detected_at + 72h, got %v", dl.Sub(dt))
	}
	if b.Status != "detected" {
		t.Fatalf("expected detected, got %q", b.Status)
	}

	// alert event recorded durably
	var alerts []map[string]any
	if err := s.st.ListInto("breach_alerts", &alerts); err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0]["event"] != BreachEventType {
		t.Fatalf("expected one %s alert, got %v", BreachEventType, alerts)
	}

	// validation failures
	rec = doBreach(t, h, "POST", "/v1/privacy/breaches", bearerFor(t, officer), map[string]any{
		"title": "x", "severity": "apocalyptic", "affected_subjects": []string{"s"}})
	if rec.Code != 400 {
		t.Fatalf("bad severity must be 400, got %d", rec.Code)
	}
	rec = doBreach(t, h, "POST", "/v1/privacy/breaches", bearerFor(t, officer), map[string]any{
		"title": "x", "severity": "low"})
	if rec.Code != 400 {
		t.Fatalf("missing affected_subjects must be 400, got %d", rec.Code)
	}
}

func TestBreachWorkflow(t *testing.T) {
	s := newTestServer(t)
	admin := auth.Claims{Sub: "root", Roles: []string{"admin"}}

	// detected in the past -> notification already late
	detected := time.Now().UTC().Add(-80 * time.Hour).Format(time.RFC3339)
	b := Breach{
		ID: "brc-wf", Title: "t", AffectedSubjects: []string{"s"}, Severity: "medium",
		DetectedAt: detected, Status: "detected",
		NotifyDeadline: time.Now().UTC().Add(-8 * time.Hour).Format(time.RFC3339),
	}
	if err := s.st.Put("breaches", b.ID, b); err != nil {
		t.Fatal(err)
	}
	tr := withClaims(s.breachTransition, admin)
	tok := bearerFor(t, admin)

	// illegal skip: detected -> notified
	req := httptest.NewRequest("POST", "/v1/privacy/breaches/brc-wf/transition",
		bytes.NewReader([]byte(`{"to":"notified"}`)))
	req.Header.Set("Authorization", tok)
	req.SetPathValue("id", "brc-wf")
	rec := httptest.NewRecorder()
	tr(rec, req)
	if rec.Code != 409 {
		t.Fatalf("skipping assessed must be 409, got %d", rec.Code)
	}

	step := func(to string, want int) Breach {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/privacy/breaches/brc-wf/transition",
			bytes.NewReader([]byte(`{"to":"`+to+`"}`)))
		req.Header.Set("Authorization", tok)
		req.SetPathValue("id", "brc-wf")
		rec := httptest.NewRecorder()
		tr(rec, req)
		if rec.Code != want {
			t.Fatalf("transition to %s: want %d got %d (%s)", to, want, rec.Code, rec.Body)
		}
		var out struct {
			Breach Breach `json:"breach"`
		}
		_ = json.NewDecoder(rec.Body).Decode(&out)
		return out.Breach
	}

	step("assessed", 200)
	got := step("notified", 200)
	if got.NotifiedAt == "" {
		t.Fatal("notified transition must record notified_at")
	}
	if !got.LateNotification {
		t.Fatal("notification past 72h deadline must be flagged late")
	}
	got = step("closed", 200)
	if len(got.History) != 3 {
		t.Fatalf("expected 3 transitions, got %d", len(got.History))
	}
	// closed is terminal
	step("assessed", 409)
}

func TestBreachRoleGating(t *testing.T) {
	s := newTestServer(t)
	h := withClaims(requireAnyRole(s.breachList, "privacy:officer", "admin"), auth.Claims{})

	// unauthenticated
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/v1/privacy/breaches", nil))
	if rec.Code != 401 {
		t.Fatalf("no claims must be 401, got %d", rec.Code)
	}
	// wrong role
	rec = doBreach(t, h, "GET", "/v1/privacy/breaches",
		bearerFor(t, auth.Claims{Sub: "op", Roles: []string{"operator"}}), nil)
	if rec.Code != 403 {
		t.Fatalf("operator must be 403, got %d", rec.Code)
	}
	// privacy:officer ok
	rec = doBreach(t, h, "GET", "/v1/privacy/breaches",
		bearerFor(t, auth.Claims{Sub: "dpo", Roles: []string{"privacy:officer"}}), nil)
	if rec.Code != 200 {
		t.Fatalf("privacy:officer must be 200, got %d", rec.Code)
	}
}
