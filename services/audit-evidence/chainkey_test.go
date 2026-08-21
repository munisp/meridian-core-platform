package main

import "testing"

// TestChainKeyDomainSeparation (A1-12 regression): the chain HMAC key must
// never equal the seal key (pre-fix it fell back to the seal key raw).
func TestChainKeyDomainSeparation(t *testing.T) {
	seal := "test-seal-key"
	chain := deriveChainKey(seal)
	if chain == seal {
		t.Fatal("chain key must not equal the seal key (no domain separation)")
	}
	if len(chain) != 64 {
		t.Fatalf("derived chain key len = %d, want 64 hex chars", len(chain))
	}
	if deriveChainKey(seal) != chain {
		t.Fatal("derivation must be deterministic")
	}
	if deriveChainKey("other") == chain {
		t.Fatal("derivation must depend on the seal key")
	}
}
