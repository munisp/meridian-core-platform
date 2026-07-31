package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/store"
)

func newTestServer(t *testing.T) *server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &server{st: st, receiptKey: []byte("test-key")}
}

func seedConsent(t *testing.T, s *server, c Consent) Consent {
	t.Helper()
	if c.ID == "" {
		c.ID = "con-test-1"
	}
	if c.GrantedAt == "" {
		c.GrantedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := s.st.Put("consents", c.ID, c); err != nil {
		t.Fatal(err)
	}
	return c
}

func doCheck(t *testing.T, s *server, body map[string]string) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/consents/check", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	s.check(rec, req)
	var out map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&out)
	return rec.Code, out
}

func TestCheckConsentFastPath(t *testing.T) {
	s := newTestServer(t)
	seedConsent(t, s, Consent{
		ID: "con-1", Subject: "nin-hash-1", Purpose: "nin_verification",
		LawfulBasis: "consent", Granted: true, Status: "active",
	})

	// allowed: active consent, matching basis
	code, out := doCheck(t, s, map[string]string{
		"subject": "nin-hash-1", "purpose": "nin_verification", "lawful_basis": "consent"})
	if code != 200 || out["allowed"] != true {
		t.Fatalf("expected allowed, got code=%d out=%v", code, out)
	}

	// fail-closed: unknown subject
	_, out = doCheck(t, s, map[string]string{"subject": "nope", "purpose": "nin_verification"})
	if out["allowed"] != false {
		t.Fatalf("unknown subject must be denied: %v", out)
	}

	// fail-closed: wrong purpose
	_, out = doCheck(t, s, map[string]string{"subject": "nin-hash-1", "purpose": "marketing"})
	if out["allowed"] != false {
		t.Fatalf("wrong purpose must be denied: %v", out)
	}

	// lawful-basis mismatch
	_, out = doCheck(t, s, map[string]string{
		"subject": "nin-hash-1", "purpose": "nin_verification", "lawful_basis": "contract"})
	if out["allowed"] != false {
		t.Fatalf("basis mismatch must be denied: %v", out)
	}

	// invalid basis rejected
	code, _ = doCheck(t, s, map[string]string{
		"subject": "nin-hash-1", "purpose": "nin_verification", "lawful_basis": "because-i-want-to"})
	if code != 400 {
		t.Fatalf("invalid basis must be 400, got %d", code)
	}

	// missing subject/purpose rejected
	code, _ = doCheck(t, s, map[string]string{"subject": "nin-hash-1"})
	if code != 400 {
		t.Fatalf("missing purpose must be 400, got %d", code)
	}
}

func TestCheckConsentRevokeHaltsImmediately(t *testing.T) {
	s := newTestServer(t)
	c := seedConsent(t, s, Consent{
		ID: "con-2", Subject: "tin-hash-9", Purpose: "tin_verification",
		LawfulBasis: "legal_obligation", Granted: true, Status: "active",
	})

	q := map[string]string{"subject": "tin-hash-9", "purpose": "tin_verification", "lawful_basis": "legal_obligation"}
	if _, out := doCheck(t, s, q); out["allowed"] != true {
		t.Fatalf("expected allowed before revoke: %v", out)
	}
	// revoke; re-check on next request must deny (no stale window)
	c.Status = "revoked"
	c.Granted = false
	if err := s.st.Put("consents", c.ID, c); err != nil {
		t.Fatal(err)
	}
	if _, out := doCheck(t, s, q); out["allowed"] != false {
		t.Fatalf("revoked consent must be denied on re-check: %v", out)
	}
}

func TestCheckConsentExpiredDenied(t *testing.T) {
	s := newTestServer(t)
	seedConsent(t, s, Consent{
		ID: "con-3", Subject: "s", Purpose: "p",
		LawfulBasis: "consent", Granted: true, Status: "active",
		ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	_, out := doCheck(t, s, map[string]string{"subject": "s", "purpose": "p"})
	if out["allowed"] != false {
		t.Fatalf("expired consent must be denied: %v", out)
	}
	// and the record must now be marked expired
	var c Consent
	if err := s.st.Get("consents", "con-3", &c); err != nil {
		t.Fatal(err)
	}
	if c.Status != "expired" {
		t.Fatalf("expected lazy expiry, got %q", c.Status)
	}
}
