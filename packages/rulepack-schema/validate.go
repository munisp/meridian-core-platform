// Package rulepackschema validates rp-* rule pack files against the pack
// grammar (SPEC 1.4). The canonical machine-readable grammar is
// schema/rulepack.schema.json; this Go validator is a dependency-light
// structural validator covering the same rules.
package rulepackschema

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

var (
	idPattern      = regexp.MustCompile(`^rp-[a-z0-9][a-z0-9-]*$`)
	versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	datePattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	ruleIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	sigPattern     = regexp.MustCompile(`^[0-9a-f]*$`)
)

// Statuses allowed by the pack lifecycle (draft→review→simulation→published→retired).
var statuses = map[string]bool{
	"draft": true, "review": true, "simulation": true, "published": true, "retired": true,
}

// ruleKinds are the mutually-exclusive outcome kinds of a rule's `then`.
var ruleKinds = []string{"rate_bps", "threshold", "band", "formula", "decision_table"}

// Pack is the typed view of a validated pack file.
type Pack struct {
	ID                 string         `yaml:"id" json:"id"`
	Version            string         `yaml:"version" json:"version"`
	EffectiveFrom      string         `yaml:"effective_from" json:"effective_from"`
	EffectiveTo        *string        `yaml:"effective_to" json:"effective_to"`
	Status             string         `yaml:"status" json:"status"`
	SubjectToRegazette bool           `yaml:"subject_to_regazette" json:"subject_to_regazette"`
	Provenance         Provenance     `yaml:"provenance" json:"provenance"`
	Signed             *Signature     `yaml:"signed" json:"signed"`
	Rules              []Rule         `yaml:"rules" json:"rules"`
	Raw                map[string]any `yaml:"-" json:"-"`
}

// Provenance per SPEC 1.4.
type Provenance struct {
	AsPassed       string  `yaml:"as_passed" json:"as_passed"`
	AsGazetted     *string `yaml:"as_gazetted" json:"as_gazetted"`
	SourceCitation string  `yaml:"source_citation" json:"source_citation"`
}

// Signature is the ed25519 ceremony signature block.
type Signature struct {
	Algorithm string `yaml:"algorithm" json:"algorithm"`
	KeyID     string `yaml:"key_id" json:"key_id"`
	Signature string `yaml:"signature" json:"signature"`
}

// Rule is one when/then rule. When/Then stay generic maps: evaluation
// semantics live in the rules-engine service.
type Rule struct {
	ID          string         `yaml:"id" json:"id"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	When        map[string]any `yaml:"when" json:"when"`
	Then        map[string]any `yaml:"then" json:"then"`
}

// ParsePackYAML decodes a pack YAML file into a Pack (keeping Raw for
// signature/canonicalisation purposes).
func ParsePackYAML(b []byte) (*Pack, error) {
	var p Pack
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("yaml parse: %w", err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("yaml parse (raw): %w", err)
	}
	p.Raw = raw
	return &p, nil
}

// Validate checks a parsed pack against the grammar, returning all problems.
func (p *Pack) Validate() []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if !idPattern.MatchString(p.ID) {
		add("id %q must match %s", p.ID, idPattern)
	}
	if !versionPattern.MatchString(p.Version) {
		add("version %q must be semver X.Y.Z", p.Version)
	}
	if !datePattern.MatchString(p.EffectiveFrom) {
		add("effective_from %q must be YYYY-MM-DD", p.EffectiveFrom)
	}
	if p.EffectiveTo != nil && !datePattern.MatchString(*p.EffectiveTo) {
		add("effective_to %q must be YYYY-MM-DD or null", *p.EffectiveTo)
	}
	if !statuses[p.Status] {
		add("status %q not in draft|review|simulation|published|retired", p.Status)
	}
	if p.Provenance.AsPassed == "" {
		add("provenance.as_passed is required")
	}
	if p.Provenance.SourceCitation == "" {
		add("provenance.source_citation is required")
	}
	if p.Signed != nil {
		if p.Signed.Algorithm != "ed25519" {
			add("signed.algorithm %q must be ed25519", p.Signed.Algorithm)
		}
		if p.Signed.KeyID == "" {
			add("signed.key_id is required")
		}
		if !sigPattern.MatchString(p.Signed.Signature) {
			add("signed.signature must be lowercase hex")
		}
	}
	if len(p.Rules) == 0 {
		add("rules must contain at least one rule")
	}
	seen := map[string]bool{}
	for i, r := range p.Rules {
		if !ruleIDPattern.MatchString(r.ID) {
			add("rules[%d].id %q must match %s", i, r.ID, ruleIDPattern)
		}
		if seen[r.ID] {
			add("rules[%d].id %q duplicated", i, r.ID)
		}
		seen[r.ID] = true
		if r.When == nil {
			add("rules[%d] (%s).when is required (use {} to match all)", i, r.ID)
		}
		kinds := 0
		for _, k := range ruleKinds {
			if _, ok := r.Then[k]; ok {
				kinds++
			}
		}
		_, hasDecision := r.Then["decision"]
		_, hasSet := r.Then["set"]
		if kinds == 0 && !hasDecision && !hasSet {
			add("rules[%d] (%s).then must contain a rule kind (rate_bps|threshold|band|formula|decision_table), decision or set", i, r.ID)
		}
		if kinds > 1 {
			add("rules[%d] (%s).then must contain exactly one rule kind, found %d", i, r.ID, kinds)
		}
		for k := range r.Then {
			switch k {
			case "rate_bps", "threshold", "band", "formula", "decision_table", "decision", "set", "narrate":
			default:
				add("rules[%d] (%s).then has unknown key %q", i, r.ID, k)
			}
		}
	}
	return errs
}

// ValidateYAML parses and validates a pack file in one call.
func ValidateYAML(b []byte) (*Pack, []error) {
	p, err := ParsePackYAML(b)
	if err != nil {
		return nil, []error{err}
	}
	return p, p.Validate()
}

// Ref returns the canonical pack reference rp-x@1.2.0 (SPEC 1.1).
func (p *Pack) Ref() string { return p.ID + "@" + p.Version }
