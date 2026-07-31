// thresholds.go — I7: CTR ₦10m bank-threshold checks on ledger transfers.
//
// Implements the rp-bank-thresholds pack rules at the transfer hook:
//   - bank.ctr.single-transfer: amount >= ₦10,000,000 (1e9 kobo)
//     -> nrs.aml.ctr.v1 (currency transaction report)
//   - bank.structuring.sub-threshold-split: >= 3 transfers from one debit
//     account within a 24h window, each below the CTR threshold, totalling
//     >= 1e9 kobo -> nrs.aml.structuring.v1 (structuring suspicion)
//   - bank.structuring.near-threshold: single transfer in the ₦8m..₦10m
//     band -> nrs.aml.threshold_review.v1 (proximity review signal)
//
// The window state is in-memory per process (dev profile); in prod the same
// events are re-derivable from the nrs.ledger.transfers.v1 stream, so a
// restart degrades detection recall but never correctness of the ledger.
package main

import (
	"sync"
	"time"

	"github.com/munisp/meridian-core-platform/services/ledger/internal/tb"
)

const (
	// ctrThresholdKobo is ₦10,000,000 in kobo (MLPPA 2022 s.11 / NFIU CTR).
	ctrThresholdKobo uint64 = 1_000_000_000
	// nearThresholdKobo is the structuring-proximity band floor (₦8m).
	nearThresholdKobo uint64 = 800_000_000
	// structuringWindow is the lookback window for split detection.
	structuringWindow = 24 * time.Hour
)

type thresholdEvent struct {
	Type    string
	Payload map[string]any
}

type windowEntry struct {
	at     time.Time
	amount uint64
	max    uint64
}

type thresholdTracker struct {
	mu sync.Mutex
	// debit account hex -> recent sub-threshold transfers
	windows map[string][]windowEntry
	// accounts already flagged in the current window (dedupe alerts)
	flagged map[string]time.Time
	now     func() time.Time // injectable for tests
}

func newThresholdTracker() *thresholdTracker {
	return &thresholdTracker{
		windows: make(map[string][]windowEntry),
		flagged: make(map[string]time.Time),
		now:     time.Now,
	}
}

// Observe records a transfer and returns the threshold events it triggers.
func (tt *thresholdTracker) Observe(t tb.Transfer) []thresholdEvent {
	now := tt.now()
	base := map[string]any{
		"transfer_id":       t.ID.String(),
		"debit_account_id":  t.DebitAccountID.String(),
		"credit_account_id": t.CreditAccountID.String(),
		"amount_kobo":       t.Amount,
		"rule_pack":         "rp-bank-thresholds@1.0.0",
		"observed_at":       now.UTC().Format(time.RFC3339),
	}
	var out []thresholdEvent

	// Rule bank.ctr.single-transfer
	if t.Amount >= ctrThresholdKobo {
		p := map[string]any{"decision": "ctr_reportable", "threshold_kobo": ctrThresholdKobo}
		for k, v := range base {
			p[k] = v
		}
		out = append(out, thresholdEvent{Type: "nrs.aml.ctr.v1", Payload: p})
		return out // above threshold: not part of sub-threshold splitting stats
	}

	// Rule bank.structuring.near-threshold
	if t.Amount >= nearThresholdKobo {
		p := map[string]any{"decision": "review", "band_kobo": [2]uint64{nearThresholdKobo, ctrThresholdKobo}}
		for k, v := range base {
			p[k] = v
		}
		out = append(out, thresholdEvent{Type: "nrs.aml.threshold_review.v1", Payload: p})
	}

	// Rule bank.structuring.sub-threshold-split
	key := t.DebitAccountID.String()
	tt.mu.Lock()
	entries := tt.windows[key]
	kept := entries[:0]
	for _, e := range entries {
		if now.Sub(e.at) <= structuringWindow {
			kept = append(kept, e)
		}
	}
	kept = append(kept, windowEntry{at: now, amount: t.Amount, max: t.Amount})
	tt.windows[key] = kept
	var total, maxAmt uint64
	for _, e := range kept {
		total += e.amount
		if e.amount > maxAmt {
			maxAmt = e.amount
		}
	}
	count := len(kept)
	lastFlag, flagged := tt.flagged[key]
	shouldFlag := count >= 3 && total >= ctrThresholdKobo && maxAmt < ctrThresholdKobo &&
		(!flagged || now.Sub(lastFlag) > structuringWindow)
	if shouldFlag {
		tt.flagged[key] = now
	}
	tt.mu.Unlock()

	if shouldFlag {
		p := map[string]any{
			"decision":              "structuring_suspicion",
			"window_hours":          int(structuringWindow.Hours()),
			"window_transfer_count": count,
			"window_total_kobo":     total,
			"max_single_kobo":       maxAmt,
		}
		for k, v := range base {
			p[k] = v
		}
		out = append(out, thresholdEvent{Type: "nrs.aml.structuring.v1", Payload: p})
	}
	return out
}
