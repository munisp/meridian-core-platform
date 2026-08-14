package main

import (
	"strings"
	"testing"
)

// Regression (F-7, W4 HIGH): booting the in-mem DevClient requires explicit
// PROFILE=dev; TIGERBEETLE_ADDRESSES unset in prod is a boot error.
func TestDevClientProfileOK(t *testing.T) {
	if err := devClientProfileOK("dev"); err != nil {
		t.Fatalf("explicit PROFILE=dev must allow the in-mem DevClient: %v", err)
	}
	if err := devClientProfileOK("prod"); err == nil || !strings.Contains(err.Error(), "TIGERBEETLE_ADDRESSES") {
		t.Fatalf("PROFILE=prod without TIGERBEETLE_ADDRESSES must be a boot error, got %v", err)
	}
	if err := devClientProfileOK(""); err == nil || !strings.Contains(err.Error(), "PROFILE=dev") {
		t.Fatalf("unset PROFILE must not implicitly boot the in-mem DevClient, got %v", err)
	}
	if err := devClientProfileOK("staging"); err == nil {
		t.Fatal("non-dev profile must not boot the in-mem DevClient")
	}
}
