package main

import (
	"testing"
	"time"

	"github.com/munisp/meridian-core-platform/services/ledger/internal/tb"
)

func mkTransfer(amount uint64) tb.Transfer {
	return tb.Transfer{
		ID:              tb.MakeID(100, 1),
		DebitAccountID:  tb.MakeID(100, 42),
		CreditAccountID: tb.MakeID(100, 43),
		Amount:          amount,
		Ledger:          100,
		Code:            1,
	}
}

func eventTypes(evs []thresholdEvent) map[string]int {
	out := map[string]int{}
	for _, e := range evs {
		out[e.Type]++
	}
	return out
}

func TestCTRSingleTransfer(t *testing.T) {
	tt := newThresholdTracker()
	evs := tt.Observe(mkTransfer(ctrThresholdKobo)) // exactly ₦10m
	if eventTypes(evs)["nrs.aml.ctr.v1"] != 1 {
		t.Fatalf("expected CTR event at threshold, got %v", eventTypes(evs))
	}
	if evs[0].Payload["decision"] != "ctr_reportable" {
		t.Fatalf("bad decision: %v", evs[0].Payload)
	}
	// below threshold: no CTR
	tt2 := newThresholdTracker()
	if evs := tt2.Observe(mkTransfer(ctrThresholdKobo - 1)); eventTypes(evs)["nrs.aml.ctr.v1"] != 0 {
		t.Fatalf("CTR fired below threshold")
	}
}

func TestNearThresholdReview(t *testing.T) {
	tt := newThresholdTracker()
	evs := tt.Observe(mkTransfer(900_000_000)) // ₦9m: in the ₦8m..₦10m band
	if eventTypes(evs)["nrs.aml.threshold_review.v1"] != 1 {
		t.Fatalf("expected review event, got %v", eventTypes(evs))
	}
}

func TestStructuringSplitDetection(t *testing.T) {
	tt := newThresholdTracker()
	base := time.Now()
	i := 0
	tt.now = func() time.Time { return base.Add(time.Duration(i) * time.Hour) }
	// 4 x ₦4m from the same account within 24h: total ₦16m, max ₦4m < ₦10m
	var fired int
	for ; i < 4; i++ {
		evs := tt.Observe(mkTransfer(400_000_000))
		fired += eventTypes(evs)["nrs.aml.structuring.v1"]
	}
	if fired != 1 {
		t.Fatalf("expected exactly one structuring event (deduped), got %d", fired)
	}
	// 2 x ₦6m: only 2 transfers — no structuring (count < 3)
	tt2 := newThresholdTracker()
	for i := 0; i < 2; i++ {
		if evs := tt2.Observe(mkTransfer(600_000_000)); eventTypes(evs)["nrs.aml.structuring.v1"] != 0 {
			t.Fatalf("structuring fired with only 2 transfers")
		}
	}
	// window expiry: transfers 25h apart don't accumulate
	tt3 := newThresholdTracker()
	j := 0
	tt3.now = func() time.Time { return base.Add(time.Duration(j) * 25 * time.Hour) }
	for ; j < 4; j++ {
		if evs := tt3.Observe(mkTransfer(400_000_000)); eventTypes(evs)["nrs.aml.structuring.v1"] != 0 {
			t.Fatalf("structuring fired across expired windows")
		}
	}
}
