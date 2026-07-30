package rulepackschema

import (
	"strings"
	"testing"
)

const goodPack = `
id: rp-wht-2024
version: 1.0.0
effective_from: 2025-01-01
effective_to: null
status: published
subject_to_regazette: true
provenance:
  as_passed: "Deduction of Tax at Source (Withholding) Regulations 2024, as passed"
  as_gazetted: null
  source_citation: "Deduction of Tax at Source (WHT) Regulations 2024"
signed:
  algorithm: ed25519
  key_id: governance-board-2026
  signature: "ab12cd"
rules:
  - id: wht.rate.dividend
    when: { payment_type: dividend, beneficiary: company }
    then: { rate_bps: 1000, narrate: "WHT 10% on dividends" }
  - id: wht.smallco.carveout
    when: { payment_type: [goods, services], beneficiary: small_company }
    then:
      threshold: { field: annual_turnover, op: lte, value: 200000000, decision_if_true: exempt, decision_if_false: apply_standard_rate }
  - id: wht.band.turnover
    when: { regime: presumptive }
    then:
      band:
        field: turnover
        bands:
          - { min: 0, max: 25000000, rate_bps: 0, label: micro }
          - { min: 25000000, max: 100000000, rate_bps: 100, label: small }
          - { min: 100000000, max: null, rate_bps: 200, label: medium }
  - id: wht.formula.relief
    when: { relief: rent }
    then:
      formula: { expression: "min(amount * 0.2, 50000000)", result_field: relief_amount, round: nearest }
  - id: wht.table.agent
    when: { channel: agent }
    then:
      decision_table:
        rows:
          - match: { state: lagos }
            output: { rate_bps: 150 }
          - match: { state: kano }
            output: { rate_bps: 120 }
        default: { rate_bps: 100 }
`

func TestValidateGoodPack(t *testing.T) {
	p, errs := ValidateYAML([]byte(goodPack))
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if p.Ref() != "rp-wht-2024@1.0.0" {
		t.Fatalf("ref = %s", p.Ref())
	}
	if len(p.Rules) != 5 {
		t.Fatalf("rules = %d", len(p.Rules))
	}
}

func TestValidateBadPacks(t *testing.T) {
	cases := map[string]string{
		"bad id":      strings.Replace(goodPack, "id: rp-wht-2024", "id: WHT", 1),
		"bad version": strings.Replace(goodPack, "version: 1.0.0", "version: 1.0", 1),
		"bad status":  strings.Replace(goodPack, "status: published", "status: live", 1),
		"no provenance": strings.Replace(goodPack, `provenance:
  as_passed: "Deduction of Tax at Source (Withholding) Regulations 2024, as passed"
  as_gazetted: null
  source_citation: "Deduction of Tax at Source (WHT) Regulations 2024"
`, "", 1),
		"two rule kinds": strings.Replace(goodPack,
			"then: { rate_bps: 1000, narrate: \"WHT 10% on dividends\" }",
			"then: { rate_bps: 1000, threshold: { field: x, op: gt, value: 1 } }", 1),
		"unknown then key": strings.Replace(goodPack,
			"then: { rate_bps: 1000, narrate: \"WHT 10% on dividends\" }",
			"then: { rate_bps: 1000, bogus: 1 }", 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, errs := ValidateYAML([]byte(doc))
			if len(errs) == 0 {
				t.Fatalf("expected validation errors for %s", name)
			}
		})
	}
}

func TestParseFailure(t *testing.T) {
	if _, err := ParsePackYAML([]byte(":\n  - [")); err == nil {
		t.Fatal("expected parse error")
	}
}
