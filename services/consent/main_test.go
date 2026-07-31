package main

import (
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
