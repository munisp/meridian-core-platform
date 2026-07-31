package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/munisp/meridian-core-platform/services/tin-graph/internal/graph"
)

type fakeConsent struct {
	allowed  bool
	err      error
	seenSub  string
	seenPurp string
	seenLB   string
}

func (f *fakeConsent) Check(_ context.Context, subject, purpose, lawfulBasis string) (bool, error) {
	f.seenSub, f.seenPurp, f.seenLB = subject, purpose, lawfulBasis
	return f.allowed, f.err
}

type fakeNIN struct{ called bool }

func (f *fakeNIN) Verify(nin string) (graph.NINVerification, error) {
	f.called = true
	return graph.NINVerification{NIN: nin, Valid: true, Verified: true, Provider: "fake"}, nil
}

func post(t *testing.T, h http.HandlerFunc, path string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestVerifyNINRequiresLawfulBasis(t *testing.T) {
	fc := &fakeConsent{allowed: true}
	s := &server{nin: &fakeNIN{}, consent: fc}
	rec := post(t, s.verifyNIN, "/v1/verify/nin", map[string]string{"nin": "12345678901"})
	if rec.Code != 400 {
		t.Fatalf("missing lawful_basis must be 400, got %d", rec.Code)
	}
	rec = post(t, s.verifyNIN, "/v1/verify/nin", map[string]string{"nin": "12345678901", "lawful_basis": "bogus"})
	if rec.Code != 400 {
		t.Fatalf("invalid lawful_basis must be 400, got %d", rec.Code)
	}
}

func TestVerifyNINConsentDeniedIs403RFC7807(t *testing.T) {
	nin := &fakeNIN{}
	fc := &fakeConsent{allowed: false}
	s := &server{nin: nin, consent: fc}
	rec := post(t, s.verifyNIN, "/v1/verify/nin",
		map[string]string{"nin": "12345678901", "lawful_basis": "consent"})
	if rec.Code != 403 {
		t.Fatalf("no consent must be 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
		t.Fatalf("expected RFC7807 problem+json, got %q", ct)
	}
	if nin.called {
		t.Fatal("verification must not run when consent is denied")
	}
	if fc.seenSub != graph.HashValue("12345678901") || fc.seenPurp != "nin_verification" {
		t.Fatalf("gate queried wrong subject/purpose: %q %q", fc.seenSub, fc.seenPurp)
	}
}

func TestVerifyNINConsentErrorFailsClosed(t *testing.T) {
	nin := &fakeNIN{}
	s := &server{nin: nin, consent: &fakeConsent{err: errors.New("consent down")}}
	rec := post(t, s.verifyNIN, "/v1/verify/nin",
		map[string]string{"nin": "12345678901", "lawful_basis": "consent"})
	if rec.Code != 503 {
		t.Fatalf("consent outage must fail closed (503), got %d", rec.Code)
	}
	if nin.called {
		t.Fatal("verification must not run when consent check errors")
	}
}

func TestVerifyNINConsentAllowed(t *testing.T) {
	nin := &fakeNIN{}
	s := &server{nin: nin, consent: &fakeConsent{allowed: true}}
	rec := post(t, s.verifyNIN, "/v1/verify/nin",
		map[string]string{"nin": "12345678901", "lawful_basis": "consent"})
	if rec.Code != 200 || !nin.called {
		t.Fatalf("valid consent must allow verification, got %d", rec.Code)
	}
}

func TestVerifyTINGate(t *testing.T) {
	fc := &fakeConsent{allowed: false}
	s := &server{consent: fc}
	rec := post(t, s.verifyTIN, "/v1/verify/tin",
		map[string]string{"tin": "12345678-0001", "lawful_basis": "legal_obligation"})
	if rec.Code != 403 {
		t.Fatalf("no consent must be 403, got %d", rec.Code)
	}
	if fc.seenSub != graph.HashTIN("12345678-0001") || fc.seenPurp != "tin_verification" {
		t.Fatalf("gate queried wrong subject/purpose: %q %q", fc.seenSub, fc.seenPurp)
	}
}

func TestHTTPConsentCheckerAgainstFastPath(t *testing.T) {
	// integration-ish: the checker speaks the consent/check.go contract
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		allowed := req["subject"] == "ok" && req["purpose"] == "p" && req["lawful_basis"] == "consent"
		_ = json.NewEncoder(w).Encode(map[string]any{"allowed": allowed})
	}))
	defer up.Close()
	c := &httpConsentChecker{base: up.URL, client: up.Client()}
	ok, err := c.Check(context.Background(), "ok", "p", "consent")
	if err != nil || !ok {
		t.Fatalf("expected allowed, got %v %v", ok, err)
	}
	ok, err = c.Check(context.Background(), "no", "p", "consent")
	if err != nil || ok {
		t.Fatalf("expected denied, got %v %v", ok, err)
	}
}
