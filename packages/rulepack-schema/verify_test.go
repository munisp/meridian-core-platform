package rulepackschema

// R4 contract-bridge regression tests against a REAL ceremony pack:
// rp-fmt-fct 1.0.0 copied byte-for-byte from munisp/meridian-rule-packs
// (packs/rp-fmt-fct/1.0.0.yaml @ main), signed by the governance ceremony
// (tools/ceremony.py, ed25519, key_id governance-board-2026) over the
// canonical YAML bytes (tools/rpcommon.canonical_bytes).
//
// These tests prove:
//  1. CanonicalSigningBytesFromYAML reproduces the ceremony's signed bytes
//     exactly — ed25519 verification succeeds only on a byte-identical
//     message.
//  2. A tampered pack (one content byte changed) FAILS.
//  3. A wrong/unknown key id FAILS.
//  4. A valid signature over a DIFFERENT artifact cannot be replayed
//     against this pack (binding check).

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

// Real pinned governance-board-2026 verify key (meridian-rule-packs
// tools/keys/governance-board-2026.ed25519.public).
const governanceBoard2026PubHex = "b2ff472e90baec10063060f374780f5e6df0c295a421af74d28b2e276f98c529"

// Real canonical pack artifact (with embedded signed block), unmodified.
const realPackYAML = `# rp-fmt-fct v1.0.0 — Meridian rule pack (SPEC §1.4 format)
# Filing & management template — FCT (FCT-IRS administered taxes)
id: rp-fmt-fct
version: 1.0.0
title: Filing & management template — FCT (FCT-IRS administered taxes)
description: 'FCT filing matrix: FCT-IRS administers PIT for FCT residents, WHT on individuals, entertainment levy; federal
  presumptive default table applies (rp-presumptive-federal).'
effective_from: '2026-01-01'
effective_to: null
status: published
subject_to_regazette: true
provenance:
  as_passed: FCT Internal Revenue Service Act 2015; PITA; NTA/NTAA 2025
  as_gazetted: null
  source_citation: FCT-IRS Act 2015; PITA; Nigeria Tax Act 2025
rules:
- id: fmt.fct.paye
  when:
    tax: PIT_PAYE
    employer_territory: fct
  then:
    frequency: monthly
    deadline_day_of_month: 10
    narrate: PAYE for FCT-resident employees to FCT-IRS by 10th of following month.
- id: fmt.fct.wht-individuals
  when:
    tax: WHT
    beneficiary: individual
    territory: fct
  then:
    frequency: monthly
    deadline_day_of_month: 30
    narrate: WHT on individuals in FCT remitted to FCT-IRS by 30th of following month.
- id: fmt.fct.direct-assessment
  when:
    tax: PIT_direct_assessment
  then:
    deadline: 31 March
    narrate: Direct assessment filings by 31 March.
- id: fmt.fct.presumptive
  when:
    tax: presumptive
    territory: fct
  then:
    decision: use_federal_table
    narrate: FCT informal operators use rp-presumptive-federal band amounts.
signed:
  algorithm: ed25519
  key_id: governance-board-2026
  signature: bb642bcea69318273698ad247160c3701c76efd54065e7552c11463ee6d578cda7c7611b2b911b53949957a898e8d3630d740325d65cc0905e05663759a2e906
`

func govKeys(t *testing.T) map[string]ed25519.PublicKey {
	t.Helper()
	pub, err := hex.DecodeString(governanceBoard2026PubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatal("bad test pubkey")
	}
	return map[string]ed25519.PublicKey{"governance-board-2026": ed25519.PublicKey(pub)}
}

func TestCeremonyCanonicalBytesRealSignatureVerifies(t *testing.T) {
	canonical, err := CanonicalSigningBytesFromYAML([]byte(realPackYAML))
	if err != nil {
		t.Fatalf("canonical emission failed: %v", err)
	}
	pack, err := ParsePackYAML([]byte(realPackYAML))
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := hex.DecodeString(pack.Signed.Signature)
	if !ed25519.Verify(govKeys(t)["governance-board-2026"], canonical, sig) {
		t.Fatal("REAL ceremony signature does not verify over Go-emitted canonical bytes — emitter is not byte-exact")
	}
}

func TestVerifyRealPackArtifactAccepted(t *testing.T) {
	pack, err := ParsePackYAML([]byte(realPackYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPackYAMLArtifact(pack, []byte(realPackYAML), govKeys(t)); err != nil {
		t.Fatalf("real ceremony pack must verify, got: %v", err)
	}
}

func TestVerifyTamperedPackRejected(t *testing.T) {
	// One content byte changed AFTER signing: 10th -> 11th.
	tampered := strings.Replace(realPackYAML, "by 10th of following month", "by 11th of following month", 1)
	if tampered == realPackYAML {
		t.Fatal("tamper did not apply")
	}
	pack, err := ParsePackYAML([]byte(tampered))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPackYAMLArtifact(pack, []byte(tampered), govKeys(t)); err == nil {
		t.Fatal("tampered pack must fail verification")
	}
}

func TestVerifyWrongKeyIDRejected(t *testing.T) {
	swapped := strings.Replace(realPackYAML, "key_id: governance-board-2026", "key_id: governance-board-1999", 1)
	pack, err := ParsePackYAML([]byte(swapped))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPackYAMLArtifact(pack, []byte(swapped), govKeys(t)); err == nil {
		t.Fatal("unknown key_id must fail verification")
	}
	// Right key_id, wrong pinned key material must also fail.
	otherPub, _, _ := ed25519.GenerateKey(strings.NewReader(strings.Repeat("x", 64)))
	pack2, _ := ParsePackYAML([]byte(realPackYAML))
	err = VerifyPackYAMLArtifact(pack2, []byte(realPackYAML), map[string]ed25519.PublicKey{"governance-board-2026": otherPub})
	if err == nil {
		t.Fatal("wrong key material must fail verification")
	}
}

func TestSignatureReplayAcrossArtifactsRejected(t *testing.T) {
	// Canonical bytes + signature from the REAL pack must not verify a
	// different pack body that carries a copied signed block.
	pack, err := ParsePackYAML([]byte(realPackYAML))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalSigningBytesFromYAML([]byte(realPackYAML))
	if err != nil {
		t.Fatal(err)
	}
	evil := map[string]any{
		"id": "rp-fmt-fct", "version": "1.0.0", "status": "published",
		"signed": map[string]any{
			"algorithm": "ed25519", "key_id": "governance-board-2026",
			"signature": pack.Signed.Signature,
		},
	}
	evilPack := &Pack{ID: "rp-fmt-fct", Version: "1.0.0", Signed: pack.Signed, Raw: evil}
	if err := VerifyPackSignature(evilPack, canonical, govKeys(t)); err == nil {
		t.Fatal("replayed signature over a mismatched artifact must fail the binding check")
	}
}
