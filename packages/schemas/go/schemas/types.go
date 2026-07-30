// Package schemas holds Go types for the envelope and nrs.*.v1 payloads
// (SPEC 2 packages/schemas). JSON Schemas live in ../../jsonschema.
package schemas

import "encoding/json"

// Envelope mirrors SPEC 1.1.
type Envelope struct {
	ID              string          `json:"id"`
	Type            string          `json:"type"`
	Source          string          `json:"source"`
	Time            string          `json:"time"`
	TenantID        string          `json:"tenant_id"`
	TraceID         string          `json:"trace_id"`
	RulePackVersion string          `json:"rule_pack_version"`
	Data            json.RawMessage `json:"data"`
}

// PaymentEvent is nrs.psm.payments.v1 data.
type PaymentEvent struct {
	Reference   string `json:"reference"`
	AmountKobo  int64  `json:"amount_kobo"`
	TINHash     string `json:"tin_hash,omitempty"`
	Band        string `json:"band,omitempty"`
	State       string `json:"state,omitempty"`
	Certificate string `json:"certificate_serial,omitempty"`
}

// RulePackPublished is nrs.rulepacks.published.v1 data.
type RulePackPublished struct {
	PackID             string `json:"pack_id"`
	Version            string `json:"version"`
	Ref                string `json:"ref"`
	SHA256             string `json:"sha256"`
	EffectiveFrom      string `json:"effective_from"`
	SubjectToRegazette bool   `json:"subject_to_regazette"`
	PublishedBy        string `json:"published_by"`
}

// LedgerTransferEvent is nrs.ledger.transfers.v1 data.
type LedgerTransferEvent struct {
	TransferID      string `json:"transfer_id"`
	DebitAccountID  string `json:"debit_account_id"`
	CreditAccountID string `json:"credit_account_id"`
	AmountKobo      uint64 `json:"amount_kobo"`
	Ledger          uint64 `json:"ledger"`
	Code            uint16 `json:"code"`
	Pending         bool   `json:"pending"`
}

// CaseFeedEvent is nrs.cases.feed.v1 data (pseudonymised, T4).
type CaseFeedEvent struct {
	CaseID    string   `json:"case_id"`
	PseudoTIN string   `json:"pseudo_tin"`
	Score     float64  `json:"score"`
	Reasons   []string `json:"reasons"`
}
