package graph

import "testing"

func TestValidateTIN(t *testing.T) {
	valid := []string{"12345678-0001", "00000000-0000", "87654321-9999"}
	for _, tin := range valid {
		if !ValidateTIN(tin) {
			t.Errorf("ValidateTIN(%q) = false, want true", tin)
		}
	}
	invalid := []string{"", "12345678", "123456780001", "A12345678", // kyc variant
		"12345678-001", "12345678-00001", "abcd1234-0001", "12345678 0001"}
	for _, tin := range invalid {
		if ValidateTIN(tin) {
			t.Errorf("ValidateTIN(%q) = true, want false", tin)
		}
	}
}

func TestProvisionTINMintsCanonicalFormat(t *testing.T) {
	tin, err := ProvisionTIN("12345678901", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ValidateTIN(tin) {
		t.Fatalf("ProvisionTIN minted non-canonical TIN %q", tin)
	}
}
