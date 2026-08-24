package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/auth"
)

func TestReceiptHMACKeyed(t *testing.T) {
	s := &server{receiptKey: []byte("key-A")}
	r := Receipt{ReceiptID: "r1", ConsentID: "c1", Subject: "subj",
		Action: "granted", Time: "2024-01-01T00:00:00Z", Actor: "subj", SHA256: "abc"}
	r.HMAC = receiptHMAC(s.receiptKey, r)
	if !s.VerifyReceipt(r) {
		t.Fatal("valid receipt rejected")
	}
	// tampered receipt
	r2 := r
	r2.Action = "revoked"
	if s.VerifyReceipt(r2) {
		t.Fatal("tampered receipt accepted")
	}
	// wrong key
	s2 := &server{receiptKey: []byte("key-B")}
	if s2.VerifyReceipt(r) {
		t.Fatal("receipt verified under wrong key")
	}
	// different keys produce different seals
	if receiptHMAC([]byte("key-A"), r) == receiptHMAC([]byte("key-B"), r) {
		t.Fatal("receipt seal is unkeyed")
	}
}

func TestOwnsConsent(t *testing.T) {
	c := Consent{Subject: "tin-hash-1"}
	if !ownsConsent(auth.Claims{Sub: "tin-hash-1"}, c) {
		t.Fatal("subject should own their consent")
	}
	if ownsConsent(auth.Claims{Sub: "tin-hash-2"}, c) {
		t.Fatal("different subject must not own the consent")
	}
	if !ownsConsent(auth.Claims{Sub: "auditor", Roles: []string{"admin"}}, c) {
		t.Fatal("admin role must be allowed")
	}
	if ownsConsent(auth.Claims{Sub: "op", Roles: []string{"operator"}}, c) {
		t.Fatal("operator (non-admin) must not revoke others' consent")
	}
}

// --- B2-#14: consent subject binding / list IDOR ---

func consentReq(t *testing.T, h http.HandlerFunc, method, path, subject, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if subject != "" {
		req.SetPathValue("subject", subject)
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	rec := httptest.NewRecorder()
	auth.Middleware(h).ServeHTTP(rec, req)
	return rec
}

func TestCreateConsentSubjectBinding(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{"subject": "tin-hash-B", "purpose": "tin_verification"}

	// user A creating a consent for user B -> 403 (consent spoofing)
	rec := consentReq(t, s.create, "POST", "/v1/consents", "",
		bearerFor(t, auth.Claims{Sub: "tin-hash-A"}), body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-subject create: want 403, got %d (%s)", rec.Code, rec.Body.String())
	}

	// self create -> 201
	selfBody := map[string]any{"subject": "tin-hash-A", "purpose": "tin_verification"}
	rec = consentReq(t, s.create, "POST", "/v1/consents", "",
		bearerFor(t, auth.Claims{Sub: "tin-hash-A"}), selfBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("self create: want 201, got %d (%s)", rec.Code, rec.Body.String())
	}

	// admin cross-subject create -> 201 with admin actor on the receipt (audit)
	rec = consentReq(t, s.create, "POST", "/v1/consents", "",
		bearerFor(t, auth.Claims{Sub: "dpo-7", Roles: []string{"admin"}}), body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin cross-subject create: want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Receipt Receipt `json:"receipt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Receipt.Actor != "dpo-7" {
		t.Fatalf("receipt actor must record the admin, got %q", out.Receipt.Actor)
	}
}

func TestListBySubjectIDOR(t *testing.T) {
	s := newTestServer(t)
	seedConsent(t, s, Consent{ID: "con-a1", Subject: "tin-hash-A", Purpose: "nin_verification",
		LawfulBasis: "consent", Granted: true, Status: "active"})

	// user B listing A's consents -> 403
	rec := consentReq(t, s.listBySubject, "GET", "/v1/consents/subject/tin-hash-A", "tin-hash-A",
		bearerFor(t, auth.Claims{Sub: "tin-hash-B"}), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-subject list: want 403, got %d", rec.Code)
	}

	// A lists own -> 200 with the record
	rec = consentReq(t, s.listBySubject, "GET", "/v1/consents/subject/tin-hash-A", "tin-hash-A",
		bearerFor(t, auth.Claims{Sub: "tin-hash-A"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("self list: want 200, got %d", rec.Code)
	}
	var out struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 {
		t.Fatalf("self list: want 1 consent, got %d", out.Count)
	}

	// admin lists A -> 200
	rec = consentReq(t, s.listBySubject, "GET", "/v1/consents/subject/tin-hash-A", "tin-hash-A",
		bearerFor(t, auth.Claims{Sub: "dpo-7", Roles: []string{"admin"}}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list: want 200, got %d", rec.Code)
	}
}
