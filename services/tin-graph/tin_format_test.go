package main

import (
	"net/http"
	"testing"

	"github.com/munisp/meridian-core-platform/packages/events/store"
)

func TestVerifyTINRejectsMalformedFormat(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeConsent{allowed: true}
	s := &server{st: st, consent: fc}
	rec := post(t, s.verifyTIN, "/v1/verify/tin",
		map[string]string{"tin": "not-a-tin", "lawful_basis": "legal_obligation"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed TIN must be 400, got %d", rec.Code)
	}
	// canonical format passes the format gate (consent then decides)
	rec = post(t, s.verifyTIN, "/v1/verify/tin",
		map[string]string{"tin": "12345678-0001", "lawful_basis": "legal_obligation"})
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("canonical TIN must not be 400, got %d", rec.Code)
	}
}
