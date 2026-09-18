package auth

// End-to-end wiring for the R4 tenant-attribution hook: otelx.TenantVerifier
// is registered by this package's init() and resolves tenant_id ONLY from
// verified tokens.

import (
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/packages/events/otelx"
)

func TestTenantVerifierHookRegistered(t *testing.T) {
	if otelx.TenantVerifier == nil {
		t.Fatal("otelx.TenantVerifier was not registered by auth init()")
	}
}

func TestTenantVerifierVerifiedHS256(t *testing.T) {
	tok, err := SignHS256(Claims{Sub: "u1", TenantID: "tenant-ng-01"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := otelx.TenantVerifier("Bearer " + tok); got != "tenant-ng-01" {
		t.Errorf("verified token tenant = %q, want tenant-ng-01", got)
	}
}

func TestTenantVerifierRejectsForgery(t *testing.T) {
	forgery := "Bearer eyJhbGciOiJub25lIn0.eyJ0ZW5hbnRfaWQiOiJ2aWN0aW0ifQ.sig"
	if got := otelx.TenantVerifier(forgery); got != "" {
		t.Errorf("forged token yielded tenant %q, want empty", got)
	}
	if got := otelx.TenantVerifier(""); got != "" {
		t.Errorf("missing auth header yielded tenant %q, want empty", got)
	}
}
