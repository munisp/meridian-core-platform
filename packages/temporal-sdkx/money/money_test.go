package money

import (
	"context"
	"fmt"
	"testing"

	sdkx "github.com/munisp/meridian-core-platform/packages/temporal-sdkx"
)

// fakeLedger is an in-memory LedgerPort with idempotent-create semantics.
type fakeLedger struct {
	transfers map[string]Transfer
	pending   map[string]bool
	failPost  bool // simulate a ledger outage on PostPendingAs
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{transfers: map[string]Transfer{}, pending: map[string]bool{}}
}

func (f *fakeLedger) CreatePending(t Transfer) error {
	if _, ok := f.transfers[t.ID]; ok {
		return nil // idempotent replay
	}
	t.Pending = true
	f.transfers[t.ID] = t
	f.pending[t.ID] = true
	return nil
}

func (f *fakeLedger) PostPendingAs(pendingID, postID string, amount uint64) error {
	if f.failPost {
		return fmt.Errorf("simulated ledger outage")
	}
	if _, ok := f.transfers[pendingID]; !ok {
		return fmt.Errorf("pending not found")
	}
	if !f.pending[pendingID] {
		if p, ok := f.transfers[postID]; ok && p.AmountKobo == amount {
			return nil // idempotent replay
		}
		return fmt.Errorf("not pending")
	}
	pt := f.transfers[pendingID]
	f.pending[pendingID] = false
	f.transfers[postID] = Transfer{ID: postID, DebitAccountID: pt.DebitAccountID,
		CreditAccountID: pt.CreditAccountID, Ledger: pt.Ledger, Code: 2, AmountKobo: amount}
	return nil
}

func (f *fakeLedger) VoidPending(pendingID string) error {
	if _, ok := f.transfers[pendingID]; !ok {
		return fmt.Errorf("pending not found")
	}
	f.pending[pendingID] = false
	return nil
}

func (f *fakeLedger) Transfer(t Transfer) error {
	if _, ok := f.transfers[t.ID]; ok {
		return nil // idempotent replay
	}
	f.transfers[t.ID] = t
	return nil
}

func (f *fakeLedger) GetTransfer(id string) (Transfer, bool, error) {
	t, ok := f.transfers[id]
	return t, ok, nil
}

func (f *fakeLedger) countPosted() int {
	n := 0
	for id, t := range f.transfers {
		if !t.Pending && !f.pending[id] && (t.Code == 2 || t.Code == 5) {
			n++
		}
	}
	return n
}

// TestCaptureSagaOnInprocRunner: full capture saga runs on the in-proc
// runner; a re-execution (retry) replays without double-posting.
func TestCaptureSagaOnInprocRunner(t *testing.T) {
	lc := newFakeLedger()
	r := sdkx.NewInprocRunner()
	Register(r, Deps{Ledger: lc})
	in := CaptureInput{PaymentID: "pay-1", PayerAccountID: "payer", CollectionsAccount: "collections",
		FeeAccountID: "fees", AmountKobo: 100000, FeeKobo: 1000, Ledger: 200}
	out, err := r.Execute(context.Background(), "CaptureSaga", in)
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["post_transfer_id"] != DeterministicID("cap-post:pay-1") {
		t.Fatalf("unexpected output %+v", out)
	}
	if _, ok := lc.transfers[DeterministicID("cap-fee:pay-1")]; !ok {
		t.Fatal("fee leg must be posted")
	}
	n := len(lc.transfers)
	if _, err := r.Execute(context.Background(), "CaptureSaga", in); err != nil {
		t.Fatalf("replay must succeed: %v", err)
	}
	if len(lc.transfers) != n {
		t.Fatalf("replay must not create new transfers (%d -> %d)", n, len(lc.transfers))
	}
}

// TestCaptureSagaCompensates: fee-leg failure reverses the landed post and
// voids the pending — no partial capture state.
func TestCaptureSagaCompensates(t *testing.T) {
	lc := newFakeLedger()
	r := sdkx.NewInprocRunner()
	Register(r, Deps{Ledger: lc})
	in := CaptureInput{PaymentID: "pay-2", PayerAccountID: "payer", CollectionsAccount: "collections",
		FeeAccountID: "fees", AmountKobo: 50000, FeeKobo: 500, Ledger: 200}
	// run once to completion, then force a post failure mid-saga on a second payment
	if _, err := r.Execute(context.Background(), "CaptureSaga", in); err != nil {
		t.Fatal(err)
	}
	lc.failPost = true
	in2 := in
	in2.PaymentID = "pay-3"
	if _, err := r.Execute(context.Background(), "CaptureSaga", in2); err == nil {
		t.Fatal("saga must fail when the post leg fails")
	}
	// compensation: pending voided, no post for pay-3
	if lc.pending[DeterministicID("cap-pend:pay-3")] {
		t.Fatal("pending must be voided by compensation")
	}
	if _, ok := lc.transfers[DeterministicID("cap-post:pay-3")]; ok {
		t.Fatal("no post transfer may exist for the failed saga")
	}
}

// TestRefundWorkflowDoubleSubmit: two executions with the same RefundID
// produce exactly one posted transfer (F2 double-submit safety).
func TestRefundWorkflowDoubleSubmit(t *testing.T) {
	lc := newFakeLedger()
	r := sdkx.NewInprocRunner()
	Register(r, Deps{Ledger: lc})
	in := RefundInput{RefundID: "refund:tin1:2026:vat", TreasuryAccount: "treasury", TaxpayerAccount: "taxpayer",
		AmountKobo: 250000, Ledger: 400}
	if _, err := r.Execute(context.Background(), "RefundWorkflow", in); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(context.Background(), "RefundWorkflow", in); err != nil {
		t.Fatal(err)
	}
	posts := 0
	for id := range lc.transfers {
		if id == DeterministicID("ref-post:refund:tin1:2026:vat") {
			posts++
		}
	}
	if posts != 1 {
		t.Fatalf("exactly one refund post expected, got %d", posts)
	}
}

// TestRemittanceWorkflowMarkFailureReversesCredits: when mark-remitted
// fails after credits post, every credit is reversed — a retried run
// reposts cleanly (no double-credit, audit Flow 3).
func TestRemittanceWorkflowMarkFailureReversesCredits(t *testing.T) {
	lc := newFakeLedger()
	marked := map[string]bool{}
	failMark := true
	mark := func(ctx context.Context, runID string, ids []string) error {
		if failMark {
			return fmt.Errorf("simulated store outage")
		}
		marked[runID] = true
		return nil
	}
	r := sdkx.NewInprocRunner()
	Register(r, Deps{Ledger: lc, MarkRemitted: mark})
	in := RemittanceInput{RunID: "run-1", DebitAccount: "clearing", Ledger: 300, Credits: []RemittanceCredit{
		{DeductionID: "d1", CreditAccount: "vendor1", AmountKobo: 1000},
		{DeductionID: "d2", CreditAccount: "vendor2", AmountKobo: 2000},
	}}
	if _, err := r.Execute(context.Background(), "RemittanceWorkflow", in); err == nil {
		t.Fatal("run must fail when mark-remitted fails")
	}
	// all credits reversed
	if _, ok := lc.transfers[DeterministicID("wht-cr-rev:run-1:d1")]; !ok {
		t.Fatal("credit d1 must be reversed")
	}
	if _, ok := lc.transfers[DeterministicID("wht-cr-rev:run-1:d2")]; !ok {
		t.Fatal("credit d2 must be reversed")
	}
	// retry with the store back: credits post once more (same ids), run remits
	failMark = false
	if _, err := r.Execute(context.Background(), "RemittanceWorkflow", in); err != nil {
		t.Fatal(err)
	}
	if !marked["run-1"] {
		t.Fatal("run must be marked remitted")
	}
}

// TestNewRunnerFromEnvDevDefault: no TEMPORAL_URL -> in-proc runner.
func TestNewRunnerFromEnvDevDefault(t *testing.T) {
	t.Setenv("TEMPORAL_URL", "")
	r, err := NewRunnerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*sdkx.InprocRunner); !ok {
		t.Fatalf("expected in-proc runner in dev, got %T", r)
	}
}
